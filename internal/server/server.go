// Package server accepts TCP connections and runs the commands clients send.
package server

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/samadarsh/redis-go/internal/resp"
)

// ErrServerClosed is returned by Serve after Close has been called.
var ErrServerClosed = errors.New("server closed")

// Server is a Redis-compatible TCP server. Each client connection gets its
// own goroutine; they all share one Handler (and therefore one Store).
type Server struct {
	handler *Handler
	log     *slog.Logger

	mu       sync.Mutex
	listener net.Listener
	conns    map[net.Conn]struct{}
	closed   bool
	wg       sync.WaitGroup // one per live connection
}

func New(h *Handler, log *slog.Logger) *Server {
	return &Server{
		handler: h,
		log:     log,
		conns:   make(map[net.Conn]struct{}),
	}
}

// Serve accepts connections on ln until Close is called, then returns
// ErrServerClosed. Any other return value is an accept error.
func (s *Server) Serve(ln net.Listener) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		ln.Close()
		return ErrServerClosed
	}
	s.listener = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if s.isClosed() {
				return ErrServerClosed
			}
			return fmt.Errorf("accept: %w", err)
		}
		if !s.track(conn) {
			conn.Close()
			return ErrServerClosed
		}
		go s.handleConn(conn)
	}
}

// Close stops accepting connections, disconnects every client and waits
// for their goroutines to finish.
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	var err error
	if s.listener != nil {
		err = s.listener.Close()
	}
	for c := range s.conns {
		c.Close()
	}
	s.mu.Unlock()

	s.wg.Wait()
	return err
}

// handleConn is the read → run → reply loop for one client.
func (s *Server) handleConn(conn net.Conn) {
	defer s.untrack(conn)
	log := s.log.With("client", conn.RemoteAddr().String())
	log.Debug("client connected")
	defer log.Debug("client disconnected")

	r := resp.NewReader(conn)
	w := resp.NewWriter(conn)

	for {
		v, err := r.Read()
		if err != nil {
			if errors.Is(err, resp.ErrProtocol) {
				// Like Redis: report the problem, then hang up, because we
				// can no longer tell where the next command starts.
				_ = w.Write(resp.NewError("ERR " + err.Error()))
				_ = w.Flush()
			} else if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				log.Warn("read failed", "err", err)
			}
			return
		}

		args, err := commandArgs(v)
		if err != nil {
			_ = w.Write(resp.NewError("ERR Protocol error: " + err.Error()))
			_ = w.Flush()
			return
		}

		if len(args) > 0 { // a blank inline line is simply ignored
			log.Debug("command", "args", args)
			if err := w.Write(s.handler.Handle(args)); err != nil {
				return
			}
		}

		// Pipelining: if the client already sent more commands, keep going
		// and send all the replies in one go. Otherwise, send them now.
		if r.Buffered() == 0 {
			if err := w.Flush(); err != nil {
				return
			}
		}
	}
}

// commandArgs turns a command (an array of bulk strings) into plain strings.
func commandArgs(v resp.Value) ([]string, error) {
	if v.Type != resp.Array || v.Null {
		return nil, errors.New("expected an array of bulk strings")
	}
	args := make([]string, len(v.Array))
	for i, elem := range v.Array {
		if elem.Type != resp.BulkString || elem.Null {
			return nil, errors.New("expected an array of bulk strings")
		}
		args[i] = elem.Str
	}
	return args, nil
}

func (s *Server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	s.wg.Add(1)
	return true
}

func (s *Server) untrack(conn net.Conn) {
	conn.Close()
	s.mu.Lock()
	delete(s.conns, conn)
	s.mu.Unlock()
	s.wg.Done()
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}
