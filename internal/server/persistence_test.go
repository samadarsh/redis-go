package server

import (
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/samadarsh/redis-go/internal/aof"
	"github.com/samadarsh/redis-go/internal/resp"
	"github.com/samadarsh/redis-go/internal/store"
)

// startPersistentServer wires things up the same way main.go does: replay
// the AOF with no logger attached, then attach the AOF, then serve. The
// returned stop function shuts down in main.go's order: server first, then AOF.
func startPersistentServer(t *testing.T, path string) (addr string, stop func()) {
	t.Helper()
	st := store.New()
	h := NewHandler(st)
	if err := aof.Replay(path, h.Apply); err != nil {
		t.Fatalf("Replay: %v", err)
	}
	a, err := aof.Open(path, aof.EverySec)
	if err != nil {
		t.Fatal(err)
	}
	st.SetLogger(a)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(h, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()

	var once sync.Once
	stop = func() {
		once.Do(func() {
			srv.Close()
			if err := <-done; err != ErrServerClosed {
				t.Errorf("Serve returned %v, want ErrServerClosed", err)
			}
			if err := a.Close(); err != nil {
				t.Errorf("closing AOF: %v", err)
			}
		})
	}
	t.Cleanup(stop)
	return ln.Addr().String(), stop
}

// client is a tiny RESP client over a real TCP connection.
type client struct {
	t    *testing.T
	conn net.Conn
	r    *resp.Reader
}

func dialClient(t *testing.T, addr string) *client {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	t.Cleanup(func() { conn.Close() })
	return &client{t: t, conn: conn, r: resp.NewReader(conn)}
}

func (c *client) do(args ...string) resp.Value {
	c.t.Helper()
	vals := make([]resp.Value, len(args))
	for i, a := range args {
		vals[i] = resp.NewBulkString(a)
	}
	if _, err := c.conn.Write(resp.NewArray(vals...).Marshal()); err != nil {
		c.t.Fatal(err)
	}
	reply, err := c.r.Read()
	if err != nil {
		c.t.Fatalf("reading reply to %q: %v", args, err)
	}
	return reply
}

func TestDataSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appendonly.aof")

	// First run: change some data over a real TCP connection.
	addr, stop := startPersistentServer(t, path)
	c := dialClient(t, addr)
	writes := []struct {
		args []string
		want resp.Value
	}{
		{[]string{"SET", "name", "sam"}, resp.OK()},
		{[]string{"INCR", "counter"}, resp.NewInteger(1)},
		{[]string{"INCR", "counter"}, resp.NewInteger(2)},
		{[]string{"INCR", "counter"}, resp.NewInteger(3)},
		{[]string{"DECR", "counter"}, resp.NewInteger(2)},
		{[]string{"INCR", "name"}, resp.NewError("ERR value is not an integer or out of range")},
		{[]string{"SET", "tmp", "x"}, resp.OK()},
		{[]string{"DEL", "tmp"}, resp.NewInteger(1)},
		{[]string{"DEL", "tmp"}, resp.NewInteger(0)},
		{[]string{"MSET", "a", "1", "b", "2"}, resp.OK()},
		{[]string{"SET", "multiline", "line1\r\nline2"}, resp.OK()},
	}
	for _, w := range writes {
		if got := c.do(w.args...); !reflect.DeepEqual(got, w.want) {
			t.Fatalf("first run: %q = %#v, want %#v", w.args, got, w.want)
		}
	}
	stop() // like Ctrl+C: server first, then the AOF is flushed and closed

	// Second run: a brand-new server and store, loading the same file.
	addr2, _ := startPersistentServer(t, path)
	c2 := dialClient(t, addr2)
	reads := []struct {
		args []string
		want resp.Value
	}{
		{[]string{"GET", "name"}, resp.NewBulkString("sam")},
		{[]string{"GET", "counter"}, resp.NewBulkString("2")},
		{[]string{"EXISTS", "tmp"}, resp.NewInteger(0)},
		{[]string{"MGET", "a", "b", "multiline"}, resp.NewArray(
			resp.NewBulkString("1"),
			resp.NewBulkString("2"),
			resp.NewBulkString("line1\r\nline2"),
		)},
		{[]string{"KEYS", "*"}, resp.NewArray(
			resp.NewBulkString("a"),
			resp.NewBulkString("b"),
			resp.NewBulkString("counter"),
			resp.NewBulkString("multiline"),
			resp.NewBulkString("name"),
		)},
	}
	for _, r := range reads {
		if got := c2.do(r.args...); !reflect.DeepEqual(got, r.want) {
			t.Errorf("after restart: %q = %#v, want %#v", r.args, got, r.want)
		}
	}

	// The file holds exactly the successful writes, once each: INCR/DECR are
	// logged as SET of the result, the failed INCR and the no-op DEL are not
	// logged at all, and the restart didn't write the replayed commands back
	// into the file a second time.
	var logged [][]string
	if err := aof.Replay(path, func(args []string) error { logged = append(logged, args); return nil }); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"SET", "name", "sam"},
		{"SET", "counter", "1"},
		{"SET", "counter", "2"},
		{"SET", "counter", "3"},
		{"SET", "counter", "2"},
		{"SET", "tmp", "x"},
		{"DEL", "tmp"},
		{"MSET", "a", "1", "b", "2"},
		{"SET", "multiline", "line1\r\nline2"},
	}
	if !reflect.DeepEqual(logged, want) {
		t.Errorf("AOF after a restart holds\n%q\nwant\n%q", logged, want)
	}
}
