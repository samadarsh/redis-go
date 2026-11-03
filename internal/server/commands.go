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
		"EXISTS":  h.exists,
		"INCR":    h.incr,
		"DECR":    h.decr,
		"MSET":    h.mset,
		"MGET":    h.mget,
		"KEYS":    h.keys,
		// TODO(milestone 4): "EXISTS", "INCR", "EXPIRE", "TTL", ...
	}
	return h
}

// Apply runs one command read back from the AOF on startup, using the same
// code path as a client command. Only successful changes are ever logged,
// so an error reply here means the file holds something this server can't
// run, and replay must stop rather than load data that is silently wrong.
func (h *Handler) Apply(args []string) error {
	if reply := h.Handle(args); reply.Type == resp.Error {
		return errors.New(reply.Str)
	}
	return nil
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

// EXISTS key [key ...] -> number of the given keys that exist
func (h *Handler) exists(args []string) resp.Value {
	if len(args) == 0 {
		return wrongArgs("exists")
	}
	n, err := h.store.Exists(args...)
	if err != nil {
		return storeError("EXISTS", err)
	}
	return resp.NewInteger(int64(n))
}

// INCR key -> the value after adding 1 (a missing key starts at 0)
func (h *Handler) incr(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("incr")
	}
	return h.incrBy("INCR", args[0], 1)
}

// DECR key -> the value after subtracting 1 (a missing key starts at 0)
func (h *Handler) decr(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("decr")
	}
	return h.incrBy("DECR", args[0], -1)
}

// incrBy is the part INCR and DECR share. storeError turns
// store.ErrNotInteger and store.ErrOverflow into the exact Redis replies,
// e.g. "ERR value is not an integer or out of range".
func (h *Handler) incrBy(cmd, key string, delta int64) resp.Value {
	n, err := h.store.IncrBy(key, delta)
	if err != nil {
		return storeError(cmd, err)
	}
	return resp.NewInteger(n)
}

// MSET key value [key value ...] -> OK
func (h *Handler) mset(args []string) resp.Value {
	if len(args) == 0 || len(args)%2 != 0 {
		return wrongArgs("mset")
	}
	if err := h.store.MSet(args...); err != nil {
		return storeError("MSET", err)
	}
	return resp.OK()
}

// MGET key [key ...] -> array with one entry per key, null where a key is missing
func (h *Handler) mget(args []string) resp.Value {
	if len(args) == 0 {
		return wrongArgs("mget")
	}
	values, err := h.store.MGet(args...)
	if err != nil {
		return storeError("MGET", err)
	}

	reply := make([]resp.Value, len(values))
	for i, v := range values {
		if v == nil {
			reply[i] = resp.NullBulkString()
		} else {
			reply[i] = resp.NewBulkString(*v)
		}
	}
	return resp.NewArray(reply...)
}

// KEYS pattern -> sorted array of the keys matching pattern (empty if none)
func (h *Handler) keys(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("keys")
	}
	keys, err := h.store.Keys(args[0])
	if err != nil {
		return storeError("KEYS", err)
	}

	reply := make([]resp.Value, len(keys))
	for i, k := range keys {
		reply[i] = resp.NewBulkString(k)
	}
	return resp.NewArray(reply...) // NewArray turns "no values" into an empty array, not null
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
