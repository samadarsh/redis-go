package resp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrProtocol is wrapped by every error caused by malformed input, so the
// server can tell "client sent garbage" apart from "connection dropped".
var ErrProtocol = errors.New("Protocol error")

// Limits that protect the server from clients claiming absurd sizes.
const (
	maxLineLen  = 64 * 1024         // longest header or inline command
	maxBulkLen  = 512 * 1024 * 1024 // same limit as real Redis
	maxArrayLen = 1024 * 1024
)

// Reader decodes RESP values from a stream such as a TCP connection.
type Reader struct {
	rd *bufio.Reader
}

func NewReader(r io.Reader) *Reader {
	return &Reader{rd: bufio.NewReaderSize(r, maxLineLen)}
}

// Buffered reports how many bytes have been received but not parsed yet.
// The server uses it to batch replies for pipelined commands.
func (r *Reader) Buffered() int { return r.rd.Buffered() }

// Read decodes the next value.
//
// It returns io.EOF only when the stream ends cleanly between values.
// A stream that ends in the middle of a value gives io.ErrUnexpectedEOF.
//
// Lines that do not start with a type marker are parsed as "inline
// commands" (plain space-separated words, e.g. "PING\r\n"), which lets you
// test the server by hand with `nc localhost 6379`.
func (r *Reader) Read() (Value, error) {
	marker, err := r.rd.ReadByte()
	if err != nil {
		return Value{}, err
	}

	switch t := Type(marker); t {
	case SimpleString, Error:
		line, err := r.readLine()
		return Value{Type: t, Str: line}, err
	case Integer:
		n, err := r.readInt()
		return Value{Type: Integer, Int: n}, err
	case BulkString:
		return r.readBulk()
	case Array:
		return r.readArray()
	default:
		if err := r.rd.UnreadByte(); err != nil {
			return Value{}, err
		}
		return r.readInline()
	}
}

// readLine reads up to and including the next "\n" and returns the line
// without its "\r\n" (or bare "\n") terminator.
func (r *Reader) readLine() (string, error) {
	line, err := r.rd.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return "", fmt.Errorf("%w: line longer than %d bytes", ErrProtocol, maxLineLen)
		}
		return "", unexpected(err)
	}
	n := len(line) - 1
	if n > 0 && line[n-1] == '\r' {
		n--
	}
	return string(line[:n]), nil
}

func (r *Reader) readInt() (int64, error) {
	line, err := r.readLine()
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(line, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: invalid integer %q", ErrProtocol, line)
	}
	return n, nil
}

func (r *Reader) readBulk() (Value, error) {
	n, err := r.readInt()
	if err != nil {
		return Value{}, err
	}
	if n == -1 {
		return NullBulkString(), nil
	}
	if n < 0 || n > maxBulkLen {
		return Value{}, fmt.Errorf("%w: invalid bulk length %d", ErrProtocol, n)
	}

	buf := make([]byte, n+2) // payload + "\r\n"
	if _, err := io.ReadFull(r.rd, buf); err != nil {
		return Value{}, unexpected(err)
	}
	if buf[n] != '\r' || buf[n+1] != '\n' {
		return Value{}, fmt.Errorf("%w: bulk string not terminated by CRLF", ErrProtocol)
	}
	return NewBulkString(string(buf[:n])), nil
}

func (r *Reader) readArray() (Value, error) {
	n, err := r.readInt()
	if err != nil {
		return Value{}, err
	}
	if n == -1 {
		return Value{Type: Array, Null: true}, nil
	}
	if n < 0 || n > maxArrayLen {
		return Value{}, fmt.Errorf("%w: invalid array length %d", ErrProtocol, n)
	}

	elems := make([]Value, n)
	for i := range elems {
		if elems[i], err = r.Read(); err != nil {
			return Value{}, unexpected(err)
		}
	}
	return NewArray(elems...), nil
}

func (r *Reader) readInline() (Value, error) {
	line, err := r.readLine()
	if err != nil {
		return Value{}, err
	}
	words := strings.Fields(line)
	elems := make([]Value, len(words))
	for i, w := range words {
		elems[i] = NewBulkString(w)
	}
	return NewArray(elems...), nil
}

// unexpected turns io.EOF into io.ErrUnexpectedEOF: once we have started
// reading a value, running out of input is an error, not a clean close.
func unexpected(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
