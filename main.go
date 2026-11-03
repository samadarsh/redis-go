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
	"time"

	"github.com/samadarsh/redis-go/internal/aof"
	"github.com/samadarsh/redis-go/internal/server"
	"github.com/samadarsh/redis-go/internal/store"
)

func main() {
	addr := flag.String("addr", ":6379", "address to listen on (host:port)")
	debug := flag.Bool("debug", false, "log every connection and command")
	aofPath := flag.String("aof", "appendonly.aof", `append-only file for persistence ("" disables it)`)
	fsyncFlag := flag.String("appendfsync", "everysec", "how often to fsync the AOF: always, everysec or no")
	flag.Parse()

	level := slog.LevelInfo
	if *debug {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log) // so warnings from internal packages (e.g. aof.Replay) use the same format

	policy, err := aof.ParsePolicy(*fsyncFlag)
	if err != nil {
		log.Error("bad -appendfsync flag", "err", err)
		os.Exit(1)
	}

	st := store.New()
	handler := server.NewHandler(st)

	// Persistence. The order matters:
	//  1. Replay the AOF into the store while NO logger is attached, so the
	//     replayed commands aren't written back into the file a second time.
	//  2. Only then attach the AOF, so every change from now on is logged.
	//  3. Only then start accepting clients (below).
	var appendLog *aof.AOF
	if *aofPath == "" {
		log.Warn("persistence disabled (-aof \"\"): data will be lost on restart")
	} else {
		start := time.Now()
		replayed := 0
		st.SetLoading(true) // nothing expires during replay (see store.SetLoading)
		err := aof.Replay(*aofPath, func(args []string) error {
			if err := handler.Apply(args); err != nil {
				return err
			}
			replayed++
			return nil
		})
		st.SetLoading(false) // now keys whose time has passed expire as normal
		if err != nil {
			log.Error("cannot load AOF; refusing to start with incomplete data", "file", *aofPath, "err", err)
			os.Exit(1)
		}
		log.Info("AOF loaded", "file", *aofPath, "commands", replayed, "took", time.Since(start).Round(time.Microsecond))

		appendLog, err = aof.Open(*aofPath, policy)
		if err != nil {
			log.Error("cannot open AOF", "file", *aofPath, "err", err)
			os.Exit(1)
		}
		st.SetLogger(appendLog)
		log.Info("persistence enabled", "file", *aofPath, "appendfsync", string(policy))
	}

	// Active expiry: every 100ms, delete a sample of expired keys that nobody
	// has read (Redis's default "hz 10" runs it 10 times a second). Started
	// after replay, so it never races with loading the file.
	stopExpiry := st.StartActiveExpiry(100 * time.Millisecond)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Error("cannot listen", "addr", *addr, "err", err)
		stopExpiry()
		closeAOF(log, appendLog)
		os.Exit(1)
	}

	srv := server.New(handler, log)

	// Stop cleanly on Ctrl+C or `kill`.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("redis-go is ready to accept connections", "addr", ln.Addr().String())

	exitCode := 0
	select {
	case <-ctx.Done():
		log.Info("shutting down")
		// Stop the server FIRST. Close waits for every client goroutine to
		// finish, so after it returns no command can still be writing to the AOF.
		srv.Close()
	case err := <-errc:
		if !errors.Is(err, server.ErrServerClosed) {
			log.Error("server stopped", "err", err)
			exitCode = 1
		}
	}

	stopExpiry() // stops the goroutine and waits for it to exit

	// THEN flush, fsync and close the AOF, so every acknowledged write is on disk.
	if !closeAOF(log, appendLog) {
		exitCode = 1
	}
	os.Exit(exitCode)
}

// closeAOF closes the AOF if there is one, and reports whether that worked.
func closeAOF(log *slog.Logger, a *aof.AOF) bool {
	if a == nil {
		return true
	}
	if err := a.Close(); err != nil {
		log.Error("closing AOF failed; recent writes may not be on disk", "err", err)
		return false
	}
	log.Info("AOF flushed and closed")
	return true
}
