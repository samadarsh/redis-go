package store

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock the test moves forward by hand, so expiry tests run
// instantly and give the same result every time.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

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

func (c *fakeClock) Ms() int64 { return c.Now().UnixMilli() }

// newClockedStore returns a Store using a fake clock that starts at a fixed time.
func newClockedStore() (*Store, *fakeClock) {
	c := &fakeClock{t: time.UnixMilli(1_700_000_000_000)}
	s := New()
	s.SetClock(c.Now)
	return s, c
}

// rawHas reports whether key is still physically in the maps, even if it
// has expired. Used to check that reads don't delete and writes do.
func rawHas(s *Store, key string) (inData, inExpires bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, inData = s.data[key]
	_, inExpires = s.expires[key]
	return inData, inExpires
}

func mustPTTL(t *testing.T, s *Store, key string) int64 {
	t.Helper()
	ms, err := s.PTTL(key)
	if err != nil {
		t.Fatal(err)
	}
	return ms
}

func TestPTTL(t *testing.T) {
	s, clock := newClockedStore()

	if got := mustPTTL(t, s, "missing"); got != -2 {
		t.Errorf("PTTL(missing) = %d, want -2", got)
	}
	if err := s.Set("forever", "v"); err != nil {
		t.Fatal(err)
	}
	if got := mustPTTL(t, s, "forever"); got != -1 {
		t.Errorf("PTTL(no expiry) = %d, want -1", got)
	}

	if err := s.SetExpireAt("k", "v", clock.Ms()+10_000); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		advance time.Duration
		want    int64
	}{
		{0, 10_000},
		{3 * time.Second, 7_000},
		{6999 * time.Millisecond, 1}, // 1ms left
		{time.Millisecond, -2},       // exactly at the expiry time: gone
	}
	for _, st := range steps {
		clock.Advance(st.advance)
		if got := mustPTTL(t, s, "k"); got != st.want {
			t.Errorf("after +%v: PTTL = %d, want %d", st.advance, got, st.want)
		}
	}
}

// Once the clock passes a key's expiry, EVERY method must treat it as
// missing. Reads (read lock) must leave it in the maps; writes (write lock)
// may delete it.
func TestExpiredKeyIsMissingEverywhere(t *testing.T) {
	s, clock := newClockedStore()
	if err := s.Set("keep", "v"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"get", "del", "incr", "persist", "expire", "set-px"} {
		if err := s.SetExpireAt(k, "5", clock.Ms()+1000); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Second) // now every key except "keep" has expired

	// Reads: the key looks missing...
	if _, ok, _ := s.Get("get"); ok {
		t.Error("Get returned an expired key")
	}
	if n, _ := s.Exists("get", "get"); n != 0 {
		t.Errorf("Exists(expired, expired) = %d, want 0", n)
	}
	if vals, _ := s.MGet("get", "keep"); vals[0] != nil || vals[1] == nil {
		t.Errorf("MGet(expired, keep) = %v, want [nil, value]", vals)
	}
	if keys, _ := s.Keys("*"); fmt.Sprint(keys) != "[keep]" {
		t.Errorf("Keys(*) = %v, want [keep]", keys)
	}
	if ms := mustPTTL(t, s, "get"); ms != -2 {
		t.Errorf("PTTL(expired) = %d, want -2", ms)
	}
	// ...but reads hold only the read lock, so they must not have deleted it.
	if inData, inExp := rawHas(s, "get"); !inData || !inExp {
		t.Error("a read method deleted an expired key (it only holds a read lock)")
	}

	// Writes: the key counts as missing, and is cleaned up.
	if n, _ := s.Del("del"); n != 0 {
		t.Errorf("Del(expired) = %d, want 0", n)
	}
	if n, err := s.IncrBy("incr", 1); err != nil || n != 1 {
		t.Errorf("IncrBy(expired) = %d, %v; want 1 (starts again from 0)", n, err)
	}
	if ms := mustPTTL(t, s, "incr"); ms != -1 {
		t.Errorf("after INCR on an expired key, PTTL = %d, want -1 (a brand-new key)", ms)
	}
	if ok, _ := s.Persist("persist"); ok {
		t.Error("Persist(expired) = true, want false")
	}
	if ok, _ := s.PExpireAt("expire", clock.Ms()+5000); ok {
		t.Error("PExpireAt(expired) = true, want false")
	}
	for _, k := range []string{"del", "persist", "expire"} {
		if inData, inExp := rawHas(s, k); inData || inExp {
			t.Errorf("%s: a write method left an expired key in the maps", k)
		}
	}
}

func TestTTLRules(t *testing.T) {
	s, clock := newClockedStore()
	later := clock.Ms() + 60_000

	setWithTTL := func(key, value string) {
		t.Helper()
		if err := s.SetExpireAt(key, value, later); err != nil {
			t.Fatal(err)
		}
	}

	// A plain SET removes the TTL.
	setWithTTL("a", "1")
	if err := s.Set("a", "2"); err != nil {
		t.Fatal(err)
	}
	if ms := mustPTTL(t, s, "a"); ms != -1 {
		t.Errorf("after SET, PTTL = %d, want -1 (TTL removed)", ms)
	}

	// MSET removes it too.
	setWithTTL("b", "1")
	if err := s.MSet("b", "2"); err != nil {
		t.Fatal(err)
	}
	if ms := mustPTTL(t, s, "b"); ms != -1 {
		t.Errorf("after MSET, PTTL = %d, want -1 (TTL removed)", ms)
	}

	// INCR/DECR keep it.
	setWithTTL("n", "10")
	if _, err := s.IncrBy("n", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.IncrBy("n", -5); err != nil {
		t.Fatal(err)
	}
	if ms := mustPTTL(t, s, "n"); ms != 60_000 {
		t.Errorf("after INCR/DECR, PTTL = %d, want 60000 (TTL kept)", ms)
	}

	// PERSIST removes it, once.
	if ok, _ := s.Persist("n"); !ok {
		t.Error("Persist = false, want true")
	}
	if ms := mustPTTL(t, s, "n"); ms != -1 {
		t.Errorf("after PERSIST, PTTL = %d, want -1", ms)
	}
	if ok, _ := s.Persist("n"); ok {
		t.Error("second Persist = true, want false (no TTL left to remove)")
	}
	if ok, _ := s.Persist("missing"); ok {
		t.Error("Persist(missing) = true, want false")
	}

	// DEL removes the TTL with the key: a new key with that name has none.
	setWithTTL("d", "1")
	if _, err := s.Del("d"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("d", "again"); err != nil {
		t.Fatal(err)
	}
	if ms := mustPTTL(t, s, "d"); ms != -1 {
		t.Errorf("SET after DEL: PTTL = %d, want -1 (old TTL must be gone)", ms)
	}
}

func TestExpireInThePastDeletes(t *testing.T) {
	s, clock := newClockedStore()
	for _, at := range []int64{clock.Ms(), clock.Ms() - 5000, 1} { // EXPIRE k 0, EXPIRE k -5, a long-gone time
		if err := s.Set("k", "v"); err != nil {
			t.Fatal(err)
		}
		ok, err := s.PExpireAt("k", at)
		if err != nil || !ok {
			t.Fatalf("PExpireAt(k, %d) = %v, %v; want true, nil", at, ok, err)
		}
		if inData, _ := rawHas(s, "k"); inData {
			t.Fatalf("PExpireAt(k, %d) in the past should delete the key", at)
		}
	}
	if ok, _ := s.PExpireAt("missing", clock.Ms()+1000); ok {
		t.Error("PExpireAt(missing) = true, want false")
	}

	// SET with an expiry already in the past deletes an existing key too.
	if err := s.Set("k", "v"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetExpireAt("k", "new", clock.Ms()-1); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("k"); ok {
		t.Error("SetExpireAt with a past time left the key in place")
	}
}

// Every expiry is logged as an ABSOLUTE time, and things that don't change
// what a replay would rebuild (lazy or active expiry, no-ops) log nothing.
func TestExpiryLogging(t *testing.T) {
	s, clock := newClockedStore()
	l := &fakeLogger{store: s}
	s.SetLogger(l)
	defer func() {
		for _, v := range l.violations {
			t.Errorf("logged %s without holding the store's write lock", v)
		}
	}()
	ms := func(d time.Duration) string { return strconv.FormatInt(clock.Ms()+d.Milliseconds(), 10) }

	steps := []struct {
		name string
		run  func() error
		want []string // nil = nothing logged
	}{
		{"SET EX is one SET ... PXAT with an absolute time",
			func() error { return s.SetExpireAt("k", "v", clock.Ms()+10_000) },
			[]string{"SET", "k", "v", "PXAT", ms(10 * time.Second)}},
		{"EXPIRE is PEXPIREAT with an absolute time",
			func() error { _, err := s.PExpireAt("k", clock.Ms()+20_000); return err },
			[]string{"PEXPIREAT", "k", ms(20 * time.Second)}},
		{"PERSIST", func() error { _, err := s.Persist("k"); return err },
			[]string{"PERSIST", "k"}},
		{"PERSIST with no TTL", func() error { _, err := s.Persist("k"); return err }, nil},

		{"setup counter with TTL",
			func() error { return s.SetExpireAt("c", "1", clock.Ms()+30_000) },
			[]string{"SET", "c", "1", "PXAT", ms(30 * time.Second)}},
		{"INCR of a key with a TTL logs the SAME absolute deadline",
			func() error { _, err := s.IncrBy("c", 1); return err },
			[]string{"SET", "c", "2", "PXAT", ms(30 * time.Second)}},
		{"INCR of a key without a TTL is a plain SET",
			func() error { _, err := s.IncrBy("plain", 1); return err },
			[]string{"SET", "plain", "1"}},

		{"EXPIRE in the past is a DEL",
			func() error { _, err := s.PExpireAt("k", clock.Ms()-1); return err },
			[]string{"DEL", "k"}},
		{"EXPIRE of a missing key", func() error { _, err := s.PExpireAt("nope", clock.Ms()+1000); return err }, nil},
		{"SET with a past expiry on an existing key is a DEL",
			func() error { return s.SetExpireAt("plain", "x", clock.Ms()-1) },
			[]string{"DEL", "plain"}},
		{"SET with a past expiry on a missing key",
			func() error { return s.SetExpireAt("nope", "x", clock.Ms()-1) }, nil},

		{"lazy and active expiry log nothing", func() error {
			clock.Advance(time.Minute) // "c" expires
			_, _, _ = s.Get("c")
			_, _ = s.Del("c")
			s.activeExpireCycle()
			return nil
		}, nil},
	}

	for _, st := range steps {
		if err := st.run(); err != nil {
			t.Fatalf("%s: %v", st.name, err)
		}
		got := l.take()
		var want [][]string
		if st.want != nil {
			want = [][]string{st.want}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) || len(got) != len(want) {
			t.Errorf("%s:\n  logged %q\n  want   %q", st.name, got, want)
		}
	}
}

// One cycle of the sampling algorithm: when almost every sampled key has
// expired, it keeps sampling until they are all gone, and it leaves keys
// that haven't expired (or have no expiry) alone.
func TestActiveExpireCycle(t *testing.T) {
	s, clock := newClockedStore()
	for i := 0; i < 100; i++ {
		if err := s.SetExpireAt(fmt.Sprintf("old%d", i), "v", clock.Ms()+1000); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := s.SetExpireAt(fmt.Sprintf("later%d", i), "v", clock.Ms()+time.Hour.Milliseconds()); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		if err := s.Set(fmt.Sprintf("forever%d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Second) // the 100 "old" keys expire

	s.activeExpireCycle() // nobody reads anything

	s.mu.RLock()
	dataLeft, expiresLeft := len(s.data), len(s.expires)
	s.mu.RUnlock()
	if dataLeft != 8 || expiresLeft != 3 {
		t.Fatalf("after one cycle: %d keys and %d expiries left, want 8 and 3", dataLeft, expiresLeft)
	}
}

// The real background goroutine, with a tiny interval. This is the only
// expiry test that waits on real time: it polls until the keys are gone.
func TestStartActiveExpiry(t *testing.T) {
	s, clock := newClockedStore()
	for i := 0; i < 50; i++ {
		if err := s.SetExpireAt(fmt.Sprintf("k%d", i), "v", clock.Ms()+1000); err != nil {
			t.Fatal(err)
		}
	}
	clock.Advance(time.Second)

	stop := s.StartActiveExpiry(time.Millisecond)
	deadline := time.Now().Add(2 * time.Second)
	for {
		s.mu.RLock()
		left := len(s.data)
		s.mu.RUnlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("active expiry left %d expired keys after 2s", left)
		}
		time.Sleep(time.Millisecond)
	}

	// stop() must return (it waits for the goroutine to exit), and calling it
	// twice must be safe.
	stopped := make(chan struct{})
	go func() { stop(); stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("stop() did not return: the goroutine didn't exit")
	}
}

// Everything at once, under -race: readers, writers, the clock moving and
// active expiry running. If any method touched the maps without the right
// lock (for example deleting under a read lock), the race detector fails it.
func TestExpiryConcurrent(t *testing.T) {
	s, clock := newClockedStore()
	stop := s.StartActiveExpiry(time.Millisecond)
	defer stop()

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				key := fmt.Sprintf("k%d", i%5)
				switch (g + i) % 7 {
				case 0:
					_ = s.SetExpireAt(key, "1", clock.Ms()+int64(i%3)*10)
				case 1:
					_, _, _ = s.Get(key)
				case 2:
					_, _ = s.Exists(key, "k0")
				case 3:
					_, _ = s.IncrBy(key, 1)
				case 4:
					_, _ = s.PTTL(key)
				case 5:
					_, _ = s.Keys("k*")
				case 6:
					clock.Advance(5 * time.Millisecond)
				}
			}
		}(g)
	}
	wg.Wait()
}

// While loading, nothing expires and past deadlines are stored, not acted
// on. After loading, those keys expire as normal.
func TestLoadingMode(t *testing.T) {
	s, clock := newClockedStore()
	past := clock.Ms() - 1000

	s.SetLoading(true)
	if err := s.SetExpireAt("k", "v", past); err != nil { // replaying "SET k v PXAT <past>"
		t.Fatal(err)
	}
	if ok, err := s.Persist("k"); err != nil || !ok { // replaying "PERSIST k" must still find k
		t.Fatalf("Persist while loading = %v, %v; want true (k must still exist)", ok, err)
	}
	if err := s.SetExpireAt("gone", "v", past); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.PExpireAt("gone", past); !ok { // must find it and store the past time
		t.Fatal("PExpireAt while loading didn't find the key")
	}
	s.activeExpireCycle() // must do nothing while loading
	if inData, _ := rawHas(s, "gone"); !inData {
		t.Fatal("a key expired during loading")
	}
	s.SetLoading(false)

	if v, ok, _ := s.Get("k"); !ok || v != "v" {
		t.Errorf("k (persisted during replay) = %q, %v; want \"v\", true", v, ok)
	}
	if _, ok, _ := s.Get("gone"); ok {
		t.Error("after loading, a key with a past deadline is still visible")
	}
	s.activeExpireCycle()
	if inData, _ := rawHas(s, "gone"); inData {
		t.Error("after loading, active expiry didn't remove a key with a past deadline")
	}
}
