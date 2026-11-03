package server

import (
	"errors"
	"fmt"
	"math"
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

		// Expiry (milestone 6).
		"EXPIRE":    h.expire,
		"PEXPIRE":   h.pexpire,
		"PEXPIREAT": h.pexpireat,
		"TTL":       h.ttl,
		"PTTL":      h.pttl,
		"PERSIST":   h.persist,
		// TODO: more commands (stretch goals in the README).
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
// SET key value [EX seconds | PX milliseconds | PXAT unix-milliseconds] -> OK
//
// EX and PX are relative ("in 10 seconds"); PXAT is absolute. The AOF logs
// every SET with an expiry as PXAT, so replay needs PXAT too.
func (h *Handler) set(args []string) resp.Value {
	if len(args) < 2 {
		return wrongArgs("set")
	}
	key, value := args[0], args[1]

	// 1. Read the options. Like Redis, check the syntax of ALL of them
	//    before looking at any value, so "SET k v EX abc PX 5" is a syntax
	//    error (EX and PX together), not "not an integer".
	var unit, raw string // e.g. "EX", "10"
	for i := 2; i < len(args); i++ {
		switch opt := strings.ToUpper(args[i]); opt {
		case "EX", "PX", "PXAT":
			// Two DIFFERENT expiry options (EX with PX) is a syntax error, but
			// repeating the same one is allowed and the last value wins,
			// exactly like Redis. No value after the option is also an error.
			if (unit != "" && unit != opt) || i+1 >= len(args) {
				return syntaxError()
			}
			unit, raw = opt, args[i+1]
			i++ // skip the value we just used
		default:
			return syntaxError() // unknown option (NX, XX, GET... aren't supported)
		}
	}

	if unit == "" {
		if err := h.store.Set(key, value); err != nil {
			return storeError("SET", err)
		}
		return resp.OK()
	}

	// 2. Turn the option into an absolute expiry time.
	n, ok := store.ParseInt(raw)
	if !ok {
		return resp.NewError("ERR value is not an integer or out of range")
	}
	if n <= 0 {
		return invalidExpire("set")
	}
	var atMs int64
	switch unit {
	case "EX":
		atMs, ok = addMs(h.store.NowMs(), n, 1000)
	case "PX":
		atMs, ok = addMs(h.store.NowMs(), n, 1)
	case "PXAT":
		atMs, ok = n, true
	}
	if !ok {
		return invalidExpire("set") // so far in the future it doesn't fit in an int64
	}

	if err := h.store.SetExpireAt(key, value, atMs); err != nil {
		return storeError("SET", err)
	}
	return resp.OK()
}

// EXPIRE key seconds -> 1 if the timeout was set, 0 if the key doesn't exist
func (h *Handler) expire(args []string) resp.Value {
	return h.expireGeneric("expire", args, 1000, false)
}

// PEXPIRE key milliseconds -> like EXPIRE, in milliseconds
func (h *Handler) pexpire(args []string) resp.Value {
	return h.expireGeneric("pexpire", args, 1, false)
}

// PEXPIREAT key unix-milliseconds -> like PEXPIRE, but with an absolute time.
// This is how the AOF records every expiry.
func (h *Handler) pexpireat(args []string) resp.Value {
	return h.expireGeneric("pexpireat", args, 1, true)
}

// expireGeneric is the shared part of EXPIRE, PEXPIRE and PEXPIREAT.
// unitMs is how many milliseconds one unit is (1000 for seconds). A zero or
// negative timeout, or a time in the past, deletes the key (and returns 1).
func (h *Handler) expireGeneric(cmd string, args []string, unitMs int64, absolute bool) resp.Value {
	if len(args) != 2 {
		return wrongArgs(cmd)
	}
	n, ok := store.ParseInt(args[1])
	if !ok {
		return resp.NewError("ERR value is not an integer or out of range")
	}

	atMs := n
	if !absolute {
		if atMs, ok = addMs(h.store.NowMs(), n, unitMs); !ok {
			return invalidExpire(cmd)
		}
	}

	set, err := h.store.PExpireAt(args[0], atMs)
	if err != nil {
		return storeError(strings.ToUpper(cmd), err)
	}
	return boolReply(set)
}

// TTL key -> seconds left, -1 if the key has no expiry, -2 if it doesn't exist
func (h *Handler) ttl(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("ttl")
	}
	ms, err := h.store.PTTL(args[0])
	if err != nil {
		return storeError("TTL", err)
	}
	if ms < 0 {
		return resp.NewInteger(ms) // -1 or -2
	}
	return resp.NewInteger((ms + 500) / 1000) // rounded to the nearest second, like Redis
}

// PTTL key -> milliseconds left, -1 if the key has no expiry, -2 if it doesn't exist
func (h *Handler) pttl(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("pttl")
	}
	ms, err := h.store.PTTL(args[0])
	if err != nil {
		return storeError("PTTL", err)
	}
	return resp.NewInteger(ms)
}

// PERSIST key -> 1 if a timeout was removed, 0 otherwise
func (h *Handler) persist(args []string) resp.Value {
	if len(args) != 1 {
		return wrongArgs("persist")
	}
	removed, err := h.store.Persist(args[0])
	if err != nil {
		return storeError("PERSIST", err)
	}
	return boolReply(removed)
}

// addMs returns nowMs + n*unitMs, or ok=false if that doesn't fit in an
// int64. The checks come BEFORE the arithmetic, for the same reason as in
// INCR: Go integers silently wrap around on overflow.
//
// (Real Redis 8 accepts "SET k v PX 9223372036854775807": its C code checks
// for overflow AFTER adding, which is undefined behaviour in C, so the
// compiler may remove the check. Rejecting it is what that check intends.)
func addMs(nowMs, n, unitMs int64) (int64, bool) {
	if n > math.MaxInt64/unitMs || n < math.MinInt64/unitMs {
		return 0, false
	}
	ms := n * unitMs
	if ms > 0 && nowMs > math.MaxInt64-ms {
		return 0, false
	}
	return nowMs + ms, true // nowMs is positive, so adding a negative ms can't underflow
}

func boolReply(b bool) resp.Value {
	if b {
		return resp.NewInteger(1)
	}
	return resp.NewInteger(0)
}

func syntaxError() resp.Value { return resp.NewError("ERR syntax error") }

func invalidExpire(cmd string) resp.Value {
	return resp.NewError(fmt.Sprintf("ERR invalid expire time in '%s' command", cmd))
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
