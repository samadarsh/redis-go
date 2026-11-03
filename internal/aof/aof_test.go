package aof

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// replayAll replays the file at path and returns every command apply received.
func replayAll(t *testing.T, path string) [][]string {
	t.Helper()
	var got [][]string
	err := Replay(path, func(args []string) error {
		got = append(got, args)
		return nil
	})
	if err != nil {
		t.Fatalf("Replay: %v", err)
	}
	return got
}

// encodeAll returns the AOF bytes for cmds.
func encodeAll(cmds ...[]string) []byte {
	var b []byte
	for _, c := range cmds {
		b = encode(b, c)
	}
	return b
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func TestAppendThenReplay(t *testing.T) {
	cmds := [][]string{
		{"SET", "name", "sam"},
		{"MSET", "a", "1", "b", "2"},
		{"SET", "tricky", "line1\r\nline2 with spaces"}, // length-prefixed, so any bytes are fine
		{"SET", "empty", ""},
		{"DEL", "a"},
	}

	for _, policy := range []Policy{Always, EverySec, No} {
		t.Run(string(policy), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "appendonly.aof")
			a, err := Open(path, policy)
			if err != nil {
				t.Fatal(err)
			}
			for _, c := range cmds {
				if err := a.Append(c...); err != nil {
					t.Fatalf("Append(%q): %v", c, err)
				}
			}
			if err := a.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if got := replayAll(t, path); !reflect.DeepEqual(got, cmds) {
				t.Fatalf("replayed %q\nwant %q", got, cmds)
			}
		})
	}
}

// The file holds exactly the bytes a client would send.
func TestFileFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	a, err := Open(path, No)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Append("SET", "name", "sam"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(path)
	want := "*3\r\n$3\r\nSET\r\n$4\r\nname\r\n$3\r\nsam\r\n"
	if string(got) != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
}

// Reopening an existing file adds to the end instead of overwriting it.
func TestReopenAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	for _, c := range [][]string{{"SET", "a", "1"}, {"SET", "b", "2"}} {
		a, err := Open(path, EverySec)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.Append(c...); err != nil {
			t.Fatal(err)
		}
		if err := a.Close(); err != nil {
			t.Fatal(err)
		}
	}

	want := [][]string{{"SET", "a", "1"}, {"SET", "b", "2"}}
	if got := replayAll(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("replayed %q, want %q", got, want)
	}
}

// Append hands the bytes to the OS straight away, even with everysec, so a
// process that dies without calling Close (a crash, kill -9) loses nothing.
func TestAppendReachesOSBeforeClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	a, err := Open(path, EverySec)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	if err := a.Append("SET", "name", "sam"); err != nil {
		t.Fatal(err)
	}
	// Read the file with a separate handle, without closing the AOF.
	want := [][]string{{"SET", "name", "sam"}}
	if got := replayAll(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("before Close, file replays as %q, want %q", got, want)
	}
}

func TestAppendAfterClose(t *testing.T) {
	a, err := Open(filepath.Join(t.TempDir(), "appendonly.aof"), EverySec)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Append("SET", "a", "1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Append after Close = %v, want ErrClosed", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close = %v, want nil", err)
	}
}

func TestReplayMissingFile(t *testing.T) {
	err := Replay(filepath.Join(t.TempDir(), "does-not-exist.aof"), func([]string) error {
		t.Fatal("apply called for a file that doesn't exist")
		return nil
	})
	if err != nil {
		t.Fatalf("Replay(missing) = %v, want nil", err)
	}
}

// A crash in the middle of writing the last command: every cut point inside
// it must keep the earlier commands, and truncate the file back to them.
func TestReplayTruncatedTail(t *testing.T) {
	complete := [][]string{{"SET", "a", "1"}, {"MSET", "b", "2", "c", "3"}}
	good := encodeAll(complete...)
	full := encodeAll(append(complete, []string{"SET", "last", "lost"})...)

	for cut := len(good) + 1; cut < len(full); cut++ {
		path := filepath.Join(t.TempDir(), "appendonly.aof")
		if err := os.WriteFile(path, full[:cut], 0o644); err != nil {
			t.Fatal(err)
		}

		if got := replayAll(t, path); !reflect.DeepEqual(got, complete) {
			t.Fatalf("cut at byte %d (%q): replayed %q, want %q", cut, full[len(good):cut], got, complete)
		}
		if size := fileSize(t, path); size != int64(len(good)) {
			t.Fatalf("cut at byte %d: file is %d bytes after replay, want %d (truncated)", cut, size, len(good))
		}
	}

	// After the repair, the file is clean: we can append to it and replay again.
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	if err := os.WriteFile(path, full[:len(full)-3], 0o644); err != nil {
		t.Fatal(err)
	}
	replayAll(t, path)
	a, err := Open(path, Always)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Append("SET", "after", "repair"); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	want := append(complete, []string{"SET", "after", "repair"})
	if got := replayAll(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("after repair and append: replayed %q, want %q", got, want)
	}
}

// Damage anywhere except a cut-off tail is real corruption: Replay must
// return an error and must NOT modify the file.
func TestReplayCorruptMiddle(t *testing.T) {
	first := encodeAll([]string{"SET", "a", "1"})
	last := encodeAll([]string{"SET", "c", "3"})

	tests := []struct {
		name   string
		middle string
	}{
		{"bad array length", "*X\r\n$3\r\nSET\r\n$1\r\nb\r\n$1\r\n2\r\n"},
		{"bad bulk terminator", "*3\r\n$3\r\nSETxx$1\r\nb\r\n$1\r\n2\r\n"},
		{"integer instead of bulk string", "*2\r\n$3\r\nDEL\r\n:5\r\n"},
		{"empty command", "*0\r\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "appendonly.aof")
			data := string(first) + tt.middle + string(last)
			if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
				t.Fatal(err)
			}

			err := Replay(path, func([]string) error { return nil })
			if err == nil || !strings.Contains(err.Error(), "corrupt") {
				t.Fatalf("Replay = %v, want a corruption error", err)
			}
			if size := fileSize(t, path); size != int64(len(data)) {
				t.Fatalf("file is %d bytes after a failed replay, want %d (unchanged)", size, len(data))
			}
		})
	}
}

func TestReplayStopsOnApplyError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")
	if err := os.WriteFile(path, encodeAll([]string{"SET", "a", "1"}, []string{"BOOM"}, []string{"SET", "b", "2"}), 0o644); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("unknown command")
	var applied [][]string
	err := Replay(path, func(args []string) error {
		if args[0] == "BOOM" {
			return boom
		}
		applied = append(applied, args)
		return nil
	})
	if !errors.Is(err, boom) {
		t.Fatalf("Replay = %v, want it to wrap the apply error", err)
	}
	if want := [][]string{{"SET", "a", "1"}}; !reflect.DeepEqual(applied, want) {
		t.Fatalf("applied %q before the error, want %q", applied, want)
	}
}

func TestParsePolicy(t *testing.T) {
	for _, s := range []string{"always", "everysec", "no"} {
		if p, err := ParsePolicy(s); err != nil || string(p) != s {
			t.Errorf("ParsePolicy(%q) = %q, %v", s, p, err)
		}
	}
	for _, s := range []string{"", "sometimes", "EVERYSEC"} {
		if _, err := ParsePolicy(s); err == nil {
			t.Errorf("ParsePolicy(%q) succeeded, want an error", s)
		}
	}
	if _, err := Open(filepath.Join(t.TempDir(), "x.aof"), Policy("sometimes")); err == nil {
		t.Error("Open with a bad policy succeeded, want an error")
	}
}
