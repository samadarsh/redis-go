package server

import (
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/samadarsh/redis-go/internal/resp"
	"github.com/samadarsh/redis-go/internal/store"
)

// fakeClock is a clock the test moves forward by hand.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock { return &fakeClock{t: time.UnixMilli(1_700_000_000_000)} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// recordingLogger collects every command the store logs.
type recordingLogger struct{ cmds [][]string }

func (r *recordingLogger) Append(args ...string) error {
	r.cmds = append(r.cmds, append([]string(nil), args...))
	return nil
}

func newClockedHandler() (*Handler, *store.Store, *fakeClock) {
	st := store.New()
	clock := newFakeClock()
	st.SetClock(clock.Now)
	return NewHandler(st), st, clock
}

type step struct {
	args []string
	want resp.Value
}

func runSteps(t *testing.T, h *Handler, steps []step) {
	t.Helper()
	for _, s := range steps {
		if got := h.Handle(s.args); !reflect.DeepEqual(got, s.want) {
			t.Errorf("Handle(%q) = %#v, want %#v", s.args, got, s.want)
		}
	}
}

func cmd(args ...string) []string { return args }

func TestExpireTTLPersistCommands(t *testing.T) {
	h, _, clock := newClockedHandler()
	one, zero := resp.NewInteger(1), resp.NewInteger(0)

	runSteps(t, h, []step{
		{cmd("TTL", "k"), resp.NewInteger(-2)}, // missing
		{cmd("PTTL", "k"), resp.NewInteger(-2)},
		{cmd("SET", "k", "v"), resp.OK()},
		{cmd("TTL", "k"), resp.NewInteger(-1)}, // no expiry
		{cmd("PTTL", "k"), resp.NewInteger(-1)},
		{cmd("EXPIRE", "missing", "10"), zero},
		{cmd("EXPIRE", "k", "10"), one},
		{cmd("TTL", "k"), resp.NewInteger(10)},
		{cmd("PTTL", "k"), resp.NewInteger(10_000)},
	})

	clock.Advance(2500 * time.Millisecond)
	runSteps(t, h, []step{
		{cmd("PTTL", "k"), resp.NewInteger(7500)},
		{cmd("TTL", "k"), resp.NewInteger(8)}, // 7.5s rounds to 8, like Redis
		{cmd("PERSIST", "k"), one},
		{cmd("PERSIST", "k"), zero}, // no TTL left
		{cmd("PERSIST", "missing"), zero},
		{cmd("TTL", "k"), resp.NewInteger(-1)},
		{cmd("pexpire", "k", "1500"), one}, // case-insensitive
		{cmd("PTTL", "k"), resp.NewInteger(1500)},
	})

	clock.Advance(1500 * time.Millisecond) // exactly at the expiry time
	runSteps(t, h, []step{
		{cmd("GET", "k"), resp.NullBulkString()},
		{cmd("TTL", "k"), resp.NewInteger(-2)},
		{cmd("EXPIRE", "k", "10"), zero}, // expired keys don't exist

		// A zero or negative timeout deletes the key and returns 1.
		{cmd("SET", "k", "v"), resp.OK()},
		{cmd("EXPIRE", "k", "0"), one},
		{cmd("EXISTS", "k"), zero},
		{cmd("SET", "k", "v"), resp.OK()},
		{cmd("PEXPIRE", "k", "-5"), one},
		{cmd("GET", "k"), resp.NullBulkString()},

		// PEXPIREAT takes an absolute time.
		{cmd("SET", "k", "v"), resp.OK()},
		{cmd("PEXPIREAT", "k", strconv.FormatInt(clock.Now().UnixMilli()+5000, 10)), one},
		{cmd("PTTL", "k"), resp.NewInteger(5000)},
		{cmd("PEXPIREAT", "k", "1"), one}, // long ago: deleted
		{cmd("EXISTS", "k"), zero},
	})
}

func TestSetExpiryOptions(t *testing.T) {
	h, _, _ := newClockedHandler()
	notInt := resp.NewError("ERR value is not an integer or out of range")
	invalid := resp.NewError("ERR invalid expire time in 'set' command")
	syntax := resp.NewError("ERR syntax error")

	runSteps(t, h, []step{
		{cmd("SET", "k", "v", "EX", "10"), resp.OK()},
		{cmd("TTL", "k"), resp.NewInteger(10)},
		{cmd("SET", "k", "v", "PX", "1500"), resp.OK()},
		{cmd("PTTL", "k"), resp.NewInteger(1500)},
		{cmd("SET", "k", "v", "ex", "20"), resp.OK()}, // options are case-insensitive
		{cmd("TTL", "k"), resp.NewInteger(20)},
		{cmd("SET", "k", "v", "EX", "10", "EX", "30"), resp.OK()}, // same option twice: last wins, like Redis
		{cmd("TTL", "k"), resp.NewInteger(30)},
		{cmd("SET", "k", "v"), resp.OK()}, // a plain SET removes the TTL
		{cmd("TTL", "k"), resp.NewInteger(-1)},

		// INCR keeps the TTL.
		{cmd("SET", "n", "5", "EX", "100"), resp.OK()},
		{cmd("INCR", "n"), resp.NewInteger(6)},
		{cmd("DECR", "n"), resp.NewInteger(5)},
		{cmd("TTL", "n"), resp.NewInteger(100)},

		// Invalid values.
		{cmd("SET", "k", "v", "EX", "0"), invalid},
		{cmd("SET", "k", "v", "EX", "-1"), invalid},
		{cmd("SET", "k", "v", "PX", "0"), invalid},
		{cmd("SET", "k", "v", "PXAT", "0"), invalid},
		{cmd("SET", "k", "v", "EX", "abc"), notInt},
		{cmd("SET", "k", "v", "EX", "1.5"), notInt},
		{cmd("SET", "k", "v", "EX", "9223372036854775807"), invalid}, // * 1000 would overflow
		{cmd("SET", "k", "v", "PX", "9223372036854775807"), invalid}, // + now would overflow

		// Syntax errors.
		{cmd("SET", "k", "v", "EX"), syntax}, // no value
		{cmd("SET", "k", "v", "EX", "10", "PX", "100"), syntax},
		{cmd("SET", "k", "v", "PX", "100", "PXAT", "5"), syntax},
		{cmd("SET", "k", "v", "EX", "abc", "PX", "5"), syntax}, // syntax is checked before values
		{cmd("SET", "k", "v", "NX"), syntax},                   // not supported (yet)
		{cmd("SET", "k", "v", "FOO"), syntax},
		{cmd("SET", "k"), resp.NewError("ERR wrong number of arguments for 'set' command")},

		// None of the failed SETs changed "k".
		{cmd("GET", "k"), resp.NewBulkString("v")},
		{cmd("TTL", "k"), resp.NewInteger(-1)},
	})
}

func TestExpiryCommandErrors(t *testing.T) {
	h, _, _ := newClockedHandler()
	notInt := resp.NewError("ERR value is not an integer or out of range")
	runSteps(t, h, []step{
		{cmd("SET", "k", "v"), resp.OK()},
		{cmd("EXPIRE", "k", "abc"), notInt},
		{cmd("EXPIRE", "k", "9223372036854775807"), resp.NewError("ERR invalid expire time in 'expire' command")},
		{cmd("PEXPIRE", "k", "9223372036854775807"), resp.NewError("ERR invalid expire time in 'pexpire' command")},
		{cmd("PEXPIREAT", "k", "soon"), notInt},
		{cmd("EXPIRE", "k"), resp.NewError("ERR wrong number of arguments for 'expire' command")},
		{cmd("PEXPIRE", "k"), resp.NewError("ERR wrong number of arguments for 'pexpire' command")},
		{cmd("PEXPIREAT", "k"), resp.NewError("ERR wrong number of arguments for 'pexpireat' command")},
		{cmd("TTL"), resp.NewError("ERR wrong number of arguments for 'ttl' command")},
		{cmd("PTTL", "a", "b"), resp.NewError("ERR wrong number of arguments for 'pttl' command")},
		{cmd("PERSIST"), resp.NewError("ERR wrong number of arguments for 'persist' command")},
		{cmd("TTL", "k"), resp.NewInteger(-1)}, // nothing above changed the key
	})
}

// The whole point of absolute times: run 1 logs some commands, the server is
// "down" for an hour, and run 2 replays the log through Handler.Apply (the
// real replay path). Keys that expired during the downtime must stay gone,
// and the others must have exactly the right time left.
func TestReplayAfterDowntime(t *testing.T) {
	h1, st1, clock := newClockedHandler()
	log := &recordingLogger{}
	st1.SetLogger(log)

	runSteps(t, h1, []step{
		{cmd("SET", "short", "v", "EX", "10"), resp.OK()},
		{cmd("SET", "long", "v", "EX", "7200"), resp.OK()}, // 2 hours
		{cmd("SET", "counter", "5", "EX", "10"), resp.OK()},
		{cmd("INCR", "counter"), resp.NewInteger(6)}, // keeps the 10s TTL
		{cmd("SET", "forever", "v"), resp.OK()},
		{cmd("SET", "expire-cmd", "v"), resp.OK()},
		{cmd("EXPIRE", "expire-cmd", "10"), resp.NewInteger(1)},
		{cmd("SET", "persisted", "v", "EX", "10"), resp.OK()},
		{cmd("PERSIST", "persisted"), resp.NewInteger(1)},
		{cmd("SET", "extended", "v", "EX", "10"), resp.OK()},
		{cmd("EXPIRE", "extended", "7200"), resp.NewInteger(1)}, // extend to 2 hours
	})

	// The log holds absolute times only.
	t0 := clock.Now().UnixMilli()
	at := func(sec int64) string { return strconv.FormatInt(t0+sec*1000, 10) }
	wantLog := [][]string{
		{"SET", "short", "v", "PXAT", at(10)},
		{"SET", "long", "v", "PXAT", at(7200)},
		{"SET", "counter", "5", "PXAT", at(10)},
		{"SET", "counter", "6", "PXAT", at(10)},
		{"SET", "forever", "v"},
		{"SET", "expire-cmd", "v"},
		{"PEXPIREAT", "expire-cmd", at(10)},
		{"SET", "persisted", "v", "PXAT", at(10)},
		{"PERSIST", "persisted"},
		{"SET", "extended", "v", "PXAT", at(10)},
		{"PEXPIREAT", "extended", at(7200)},
	}
	if !reflect.DeepEqual(log.cmds, wantLog) {
		t.Fatalf("logged\n%q\nwant\n%q", log.cmds, wantLog)
	}

	// An hour of downtime, then a fresh store replays the log.
	clock.Advance(time.Hour)
	st2 := store.New()
	st2.SetClock(clock.Now)
	h2 := NewHandler(st2)
	st2.SetLoading(true) // like main.go: nothing expires during replay
	for _, c := range log.cmds {
		if err := h2.Apply(c); err != nil {
			t.Fatalf("Apply(%q): %v", c, err)
		}
	}
	st2.SetLoading(false)

	runSteps(t, h2, []step{
		{cmd("EXISTS", "short", "counter", "expire-cmd"), resp.NewInteger(0)}, // expired while down
		{cmd("TTL", "long"), resp.NewInteger(3600)},                           // exactly 1 hour left, not 2
		{cmd("TTL", "forever"), resp.NewInteger(-1)},
		{cmd("TTL", "persisted"), resp.NewInteger(-1)},
		{cmd("GET", "persisted"), resp.NewBulkString("v")},
		{cmd("TTL", "extended"), resp.NewInteger(3600)}, // its first deadline passed, but it was extended
	})
}
