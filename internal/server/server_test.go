package server

import (
	"bufio"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/samadarsh/redis-go/internal/store"
)

// startServer runs a server on a random free port and returns its address.
func startServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(NewHandler(store.New()), slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	t.Cleanup(func() {
		srv.Close()
		if err := <-done; err != ErrServerClosed {
			t.Errorf("Serve returned %v, want ErrServerClosed", err)
		}
	})
	return ln.Addr().String()
}

// dial connects to addr; every read and write fails after 2s so a broken
// server makes the test fail instead of hang.
func dial(t *testing.T, addr string) (net.Conn, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewReader(conn)
}

func send(t *testing.T, conn net.Conn, raw string) {
	t.Helper()
	if _, err := io.WriteString(conn, raw); err != nil {
		t.Fatal(err)
	}
}

func expectLine(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	got, err := r.ReadString('\n')
	if err != nil {
		t.Fatalf("reading reply: %v (want %q)", err, want)
	}
	if got != want {
		t.Fatalf("got reply %q, want %q", got, want)
	}
}

func TestPing(t *testing.T) {
	conn, r := dial(t, startServer(t))
	send(t, conn, "*1\r\n$4\r\nPING\r\n")
	expectLine(t, r, "+PONG\r\n")
}

func TestInlinePing(t *testing.T) {
	conn, r := dial(t, startServer(t))
	send(t, conn, "PING\r\n")
	expectLine(t, r, "+PONG\r\n")
}

func TestEcho(t *testing.T) {
	conn, r := dial(t, startServer(t))
	send(t, conn, "*2\r\n$4\r\nECHO\r\n$5\r\nhello\r\n")
	expectLine(t, r, "$5\r\n")
	expectLine(t, r, "hello\r\n")
}

func TestPipelining(t *testing.T) {
	conn, r := dial(t, startServer(t))
	// Three commands in a single write; we must get three replies.
	send(t, conn, strings.Repeat("*1\r\n$4\r\nPING\r\n", 3))
	for i := 0; i < 3; i++ {
		expectLine(t, r, "+PONG\r\n")
	}
}

func TestUnknownCommandKeepsConnectionOpen(t *testing.T) {
	conn, r := dial(t, startServer(t))
	send(t, conn, "FOO\r\n")
	expectLine(t, r, "-ERR unknown command 'FOO'\r\n")
	send(t, conn, "PING\r\n")
	expectLine(t, r, "+PONG\r\n")
}

func TestProtocolErrorClosesConnection(t *testing.T) {
	conn, r := dial(t, startServer(t))
	send(t, conn, "*abc\r\n")
	expectLine(t, r, "-ERR Protocol error: invalid integer \"abc\"\r\n")
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("expected server to close connection, got %v", err)
	}
}

func TestManyClients(t *testing.T) {
	addr := startServer(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, err := net.Dial("tcp", addr)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			r := bufio.NewReader(conn)
			for j := 0; j < 10; j++ {
				if _, err := io.WriteString(conn, "PING\r\n"); err != nil {
					t.Error(err)
					return
				}
				if line, err := r.ReadString('\n'); err != nil || line != "+PONG\r\n" {
					t.Errorf("got %q, %v", line, err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
