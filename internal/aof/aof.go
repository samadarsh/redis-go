// Package aof implements Redis-style append-only file persistence.
//
// Every command that changes data is appended to a file, in the same RESP
// format a client sends. On startup the file is replayed, command by
// command, to rebuild the data exactly as it was.
//
// Docs: https://redis.io/docs/latest/operate/oss_and_stack/management/persistence/
package aof

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/samadarsh/redis-go/internal/resp"
)

// Policy says how often the file is fsynced, i.e. forced from the
// operating system's memory onto the physical disk. It is a trade-off
// between speed and how much data a power cut can lose.
type Policy string

const (
	Always   Policy = "always"   // fsync after every command: safest, slowest
	EverySec Policy = "everysec" // fsync once a second in the background (the default)
	No       Policy = "no"       // never fsync; the OS writes to disk when it wants
)

// ParsePolicy converts a flag value such as "everysec" into a Policy.
func ParsePolicy(s string) (Policy, error) {
	switch p := Policy(s); p {
	case Always, EverySec, No:
		return p, nil
	}
	return "", fmt.Errorf("invalid appendfsync policy %q (want always, everysec or no)", s)
}

// ErrClosed is returned by Append after Close.
var ErrClosed = errors.New("aof: file is closed")

// AOF is an open append-only file. It is safe for concurrent use.
type AOF struct {
	mu     sync.Mutex
	file   *os.File
	w      *bufio.Writer
	policy Policy
	buf    []byte // reused for encoding, so Append doesn't allocate a new buffer each time
	size   int64  // file size up to the end of the last complete command
	err    error  // set after a failed write or fsync; every later Append returns it
	closed bool

	stop     chan struct{} // closed by Close to stop the everysec goroutine
	stopped  chan struct{} // closed when that goroutine has exited
	stopOnce sync.Once
}

// Open opens the AOF at path for appending, creating it if it doesn't exist.
func Open(path string, policy Policy) (*AOF, error) {
	if _, err := ParsePolicy(string(policy)); err != nil {
		return nil, err
	}
	// O_APPEND: every write goes to the end of the file, never over old data.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("aof: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("aof: %w", err)
	}

	a := &AOF{
		file:    f,
		w:       bufio.NewWriter(f),
		policy:  policy,
		size:    info.Size(),
		stop:    make(chan struct{}),
		stopped: make(chan struct{}),
	}
	if policy == EverySec {
		go a.syncEverySecond()
	} else {
		close(a.stopped) // no background goroutine to wait for
	}
	return a, nil
}

// Append writes one command, e.g. Append("SET", "name", "sam"), as a RESP
// array: the exact bytes a client would send.
//
// The bytes are handed to the operating system before Append returns, with
// every policy. So if only the server process dies (a crash, kill -9),
// nothing is lost. The policy only decides when the OS is forced to put
// them on the physical disk, which is what matters for a power cut.
func (a *AOF) Append(args ...string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.closed {
		return ErrClosed
	}
	if a.err != nil {
		return a.err
	}

	a.buf = encode(a.buf[:0], args)
	_, err := a.w.Write(a.buf)
	if err == nil {
		err = a.w.Flush() // bufio -> OS
	}
	if err == nil && a.policy == Always {
		err = a.file.Sync() // OS -> disk, before we report success
	}
	if err != nil {
		return a.fail(err)
	}
	a.size += int64(len(a.buf))
	return nil
}

// fail handles a failed write or fsync. It cuts the file back to the last
// complete command (best effort), so a half-written command can't sit in
// the middle of the file, and then refuses all further writes: if the disk
// is failing, we stop accepting changes rather than let memory and disk
// disagree. Even if the truncate fails, the half command is the last thing
// in the file, and Replay repairs a cut-off last command.
func (a *AOF) fail(err error) error {
	_ = a.file.Truncate(a.size)
	a.err = fmt.Errorf("aof: write failed, refusing further writes until restart: %w", err)
	return a.err
}

// syncEverySecond is the background goroutine for the everysec policy.
func (a *AOF) syncEverySecond() {
	defer close(a.stopped)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-a.stop:
			return
		case <-ticker.C:
			// We deliberately don't hold a.mu while syncing: os.File is safe
			// for concurrent use, and Append has already handed every command
			// to the OS. So writes never wait for the disk, which is the whole
			// point of everysec.
			if err := a.file.Sync(); err != nil {
				a.mu.Lock()
				if a.err == nil {
					a.err = fmt.Errorf("aof: fsync failed, refusing further writes until restart: %w", err)
				}
				a.mu.Unlock()
			}
		}
	}
}

// Close stops the background fsync, then flushes, fsyncs and closes the
// file. Call it on shutdown, after the server has stopped running commands.
// Calling it again does nothing.
func (a *AOF) Close() error {
	a.stopOnce.Do(func() { close(a.stop) })
	<-a.stopped // wait until the everysec goroutine is no longer using the file

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return nil
	}
	a.closed = true
	if err := errors.Join(a.w.Flush(), a.file.Sync(), a.file.Close()); err != nil {
		return fmt.Errorf("aof: closing: %w", err)
	}
	return nil
}

// encode appends args to b as a RESP array of bulk strings. Bulk strings are
// length-prefixed, so values may contain any bytes, even "\r\n".
func encode(b []byte, args []string) []byte {
	vals := make([]resp.Value, len(args))
	for i, arg := range args {
		vals[i] = resp.NewBulkString(arg)
	}
	return resp.NewArray(vals...).AppendTo(b)
}

// Replay reads every command in the AOF at path and calls apply for each
// one, in order. A missing file is fine: there is simply nothing to load.
//
// If the LAST command is cut off (the server or machine died in the middle
// of writing it), Replay logs a warning, truncates the file back to the end
// of the last complete command, and returns nil, like Redis's
// aof-load-truncated option. Anything else that can't be parsed is real
// corruption, and Replay returns an error without changing the file.
func Replay(path string, apply func(args []string) error) error {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // first start: nothing saved yet
	}
	if err != nil {
		return fmt.Errorf("aof: %w", err)
	}
	defer f.Close()

	counter := &countingReader{r: f}
	r := resp.NewReader(counter)
	var good int64 // byte offset just after the last complete command

	for n := 1; ; n++ {
		v, err := r.Read()
		switch {
		case errors.Is(err, io.EOF):
			return nil // ended cleanly, exactly on a command boundary

		case errors.Is(err, io.ErrUnexpectedEOF):
			// The file ends in the middle of a command. That can only be the
			// last one, so cut it off and keep everything before it.
			return truncateTail(path, good)

		case err != nil:
			return fmt.Errorf("aof: %s is corrupt at byte %d (command %d): %w", path, good, n, err)
		}

		args, err := commandArgs(v)
		if err != nil {
			return fmt.Errorf("aof: %s is corrupt at byte %d (command %d): %w", path, good, n, err)
		}
		if err := apply(args); err != nil {
			return fmt.Errorf("aof: replaying command %d %q: %w", n, args, err)
		}

		// resp.Reader reads ahead into a buffer, so "bytes read from the
		// file" overshoots. Subtracting what is still buffered (read but not
		// parsed yet) gives the exact end of the command we just applied.
		good = counter.n - int64(r.Buffered())
	}
}

func truncateTail(path string, good int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("aof: %w", err)
	}
	slog.Warn("AOF ends with an incomplete command (the server probably stopped mid-write); truncating it",
		"file", path, "kept_bytes", good, "discarded_bytes", info.Size()-good)
	if err := os.Truncate(path, good); err != nil {
		return fmt.Errorf("aof: truncating incomplete last command: %w", err)
	}
	return nil
}

// commandArgs checks that v is a non-empty array of bulk strings and returns
// them as plain strings.
func commandArgs(v resp.Value) ([]string, error) {
	if v.Type != resp.Array || v.Null || len(v.Array) == 0 {
		return nil, errors.New("expected a command (an array of bulk strings)")
	}
	args := make([]string, len(v.Array))
	for i, elem := range v.Array {
		if elem.Type != resp.BulkString || elem.Null {
			return nil, errors.New("expected a command (an array of bulk strings)")
		}
		args[i] = elem.Str
	}
	return args, nil
}

// countingReader counts how many bytes have been read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}
