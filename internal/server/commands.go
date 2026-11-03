package server

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samadarsh/redis-go/internal/resp"
	"github.com/samadarsh/redis-go/internal/store"
)

// commandFunc runs one command. args excludes the command name itself, so
// for "SET name redis" args is ["name", "redis"].
type commandFunc func(args []string) resp.Value

// Handler maps command names to the code that runs them.
//
// To add a command: write a method with the commandFunc signature and
// register it in NewHandler.
type Handler struct {
	store    *store.Store
	commands map[string]commandFunc
}

func NewHandler(s *store.Store) *Handler {
	h := &Handler{store: s}
	h.commands = map[string]commandFunc{
		"PING":    h.ping,
		"ECHO":    h.echo,
		"COMMAND": h.command,
		"SET":     h.set,
		"GET":     h.get,
		"DEL":     h.del,
		// TODO(milestone 4): "EXISTS", "INCR", "EXPIRE", "TTL", ...
	}
	return h
}

// Handle runs one command, e.g. ["SET", "name", "redis"], and returns the
// reply to send to the client.
func (h *Handler) Handle(args []string) resp.Value {
	if len(args) == 0 {
		return resp.NewError("ERR empty command")
	}
	name := strings.ToUpper(args[0]) // Redis commands are case-insensitive
	cmd, ok := h.commands[name]
	if !ok {
		return resp.NewError(fmt.Sprintf("ERR unknown command '%s'", args[0]))
	}
	return cmd(args[1:])
}

// PING          -> PONG
// PING message  -> message
func (h *Handler) ping(args []string) resp.Value {
	switch len(args) {
	case 0:
		return resp.NewSimpleString("PONG")
	case 1:
		return resp.NewBulkString(args[0])
	default:
		return wrongArgs("ping")
	}
}

// ECHO message -> message
func (h *Handler) echo(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("echo")
	}
	return resp.NewBulkString(args[0])
}

// COMMAND is sent by redis-cli when it connects, to fetch command docs for
// its autocomplete hints. An empty reply keeps it happy.
func (h *Handler) command(args []string) resp.Value {
	return resp.NewArray()
}

// SET key value -> OK
//
// TODO(milestone 6): support options like "SET key value EX 10".
func (h *Handler) set(args []string) resp.Value {
	if len(args) != 2 {
		return wrongArgs("set")
	}
	if err := h.store.Set(args[0], args[1]); err != nil {
		return storeError("SET", err)
	}
	return resp.OK()
}

// GET key -> value, or null if the key does not exist
func (h *Handler) get(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("get")
	}
	value, ok, err := h.store.Get(args[0])
	if err != nil {
		return storeError("GET", err)
	}
	if !ok {
		return resp.NullBulkString()
	}
	return resp.NewBulkString(value)
}

// DEL key [key ...] -> number of keys that were deleted
func (h *Handler) del(args []string) resp.Value {
	if len(args) == 0 {
		return wrongArgs("del")
	}
	n, err := h.store.Del(args...)
	if err != nil {
		return storeError("DEL", err)
	}
	return resp.NewInteger(int64(n))
}

func wrongArgs(cmd string) resp.Value {
	return resp.NewError(fmt.Sprintf("ERR wrong number of arguments for '%s' command", cmd))
}

func storeError(cmd string, err error) resp.Value {
	if errors.Is(err, store.ErrNotImplemented) {
		return resp.NewError(fmt.Sprintf(
			"ERR %s is not implemented yet: see internal/store/store.go (milestone 3)", cmd))
	}
	return resp.NewError("ERR " + err.Error())
}
