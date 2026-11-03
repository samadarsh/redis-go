// Command redis-go is a Redis-compatible in-memory key-value server.
//
//	go run . -addr :6380
//	redis-cli -p 6380 ping
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/samadarsh/redis-go/internal/server"
	"github.com/samadarsh/redis-go/internal/store"
)

func main() {
	addr := flag.String("addr", ":6379", "address to listen on (host:port)")
	debug := flag.Bool("debug", false, "log every connection and command")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("cannot listen", "addr", *addr, "err", err)
		os.Exit(1)
	}

	srv := server.New(server.NewHandler(store.New()), log)

	// Stop cleanly on Ctrl+C or `kill`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("redis-go is ready to accept connections", "addr", ln.Addr().String())

	select {
	case <-ctx.Done():
		log.Info("shutting down")
		srv.Close()
	case err := <-errc:
		if !errors.Is(err, server.ErrServerClosed) {
			log.Error("server stopped", "err", err)
			os.Exit(1)
		}
	}
}
