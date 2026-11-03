package store

import (
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
)

// skipIfTODO skips the test while the method under test still returns
// ErrNotImplemented. Once you implement it, the test runs for real.
func skipIfTODO(t *testing.T, err error) {
	t.Helper()
	if errors.Is(err, ErrNotImplemented) {
		t.Skip("milestone 3: not implemented yet (see internal/store/store.go)")
	}
}

func TestSetThenGet(t *testing.T) {
	s := New()
	err := s.Set("name", "redis")
	skipIfTODO(t, err)
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, ok, err := s.Get("name")
	skipIfTODO(t, err)
	if err != nil || !ok || got != "redis" {
		t.Fatalf(`Get("name") = %q, %v, %v; want "redis", true, nil`, got, ok, err)
	}
}

func TestGetMissingKey(t *testing.T) {
	s := New()
	got, ok, err := s.Get("does-not-exist")
	skipIfTODO(t, err)
	if err != nil || ok || got != "" {
		t.Fatalf(`Get(missing) = %q, %v, %v; want "", false, nil`, got, ok, err)
	}
}

func TestSetOverwrites(t *testing.T) {
	s := New()
	skipIfTODO(t, s.Set("k", "old"))
	if err := s.Set("k", "new"); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get("k")
	skipIfTODO(t, err)
	if got != "new" {
		t.Fatalf("got %q after overwrite, want %q", got, "new")
	}
}

func TestDel(t *testing.T) {
	s := New()
	skipIfTODO(t, s.Set("a", "1"))
	if err := s.Set("b", "2"); err != nil {
		t.Fatal(err)
	}

	n, err := s.Del("a", "b", "never-existed")
	skipIfTODO(t, err)
	if err != nil || n != 2 {
		t.Fatalf("Del = %d, %v; want 2, nil", n, err)
	}

	if _, ok, _ := s.Get("a"); ok {
		t.Error(`key "a" still exists after Del`)
	}
}

func TestExists(t *testing.T) {
	s := New()
	if err := s.Set("a", "1"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("b", "2"); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		keys []string
		want int
	}{
		{"missing key", []string{"nope"}, 0},
		{"existing key", []string{"a"}, 1},
		{"mixed keys", []string{"a", "nope", "b"}, 2},
		{"duplicates counted each time", []string{"a", "a", "a"}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := s.Exists(tt.keys...)
			if err != nil || n != tt.want {
				t.Fatalf("Exists(%q) = %d, %v; want %d, nil", tt.keys, n, err, tt.want)
			}
		})
	}
}

// Run with `go test -race` (make test does this): if Store has no lock,
// the race detector fails this test.
func TestConcurrentAccess(t *testing.T) {
	s := New()
	skipIfTODO(t, s.Set("probe", "x"))

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", i%5)
			_ = s.Set(key, "v")
			_, _, _ = s.Get(key)
			_, _ = s.Exists(key)
			_, _ = s.Del(key)
		}(i)
	}
	wg.Wait()
}

func TestIncrBy(t *testing.T) {
	const (
		maxInt = "9223372036854775807"  // math.MaxInt64
		minInt = "-9223372036854775808" // math.MinInt64
	)

	tests := []struct {
		name    string
		start   *string // value stored before the call; nil means the key is missing
		delta   int64
		want    int64
		wantErr error
	}{
		{"new key +1", nil, 1, 1, nil},
		{"existing +1", ptr("10"), 1, 11, nil},
		{"new key -1", nil, -1, -1, nil},
		{"negative value", ptr("-5"), 1, -4, nil},
		{"zero", ptr("0"), -1, -1, nil},
		{"max -1 is fine", ptr(maxInt), -1, 9223372036854775806, nil},

		{"not a number", ptr("abc"), 1, 0, ErrNotInteger},
		{"leading plus", ptr("+5"), 1, 0, ErrNotInteger},
		{"leading space", ptr(" 5"), 1, 0, ErrNotInteger},
		{"trailing space", ptr("5 "), 1, 0, ErrNotInteger},
		{"leading zero", ptr("05"), 1, 0, ErrNotInteger},
		{"minus zero", ptr("-0"), 1, 0, ErrNotInteger},
		{"decimal", ptr("1.5"), 1, 0, ErrNotInteger},
		{"empty string", ptr(""), 1, 0, ErrNotInteger},
		{"bigger than int64", ptr("9223372036854775808"), -1, 0, ErrNotInteger},

		{"INCR at max int64", ptr(maxInt), 1, 0, ErrOverflow},
		{"DECR at min int64", ptr(minInt), -1, 0, ErrOverflow},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New()
			if tt.start != nil {
				if err := s.Set("k", *tt.start); err != nil {
					t.Fatal(err)
				}
			}

			got, err := s.IncrBy("k", tt.delta)
			if !errors.Is(err, tt.wantErr) || got != tt.want {
				t.Fatalf("IncrBy(%d) = %d, %v; want %d, %v", tt.delta, got, err, tt.want, tt.wantErr)
			}

			// On error the stored value must be untouched.
			if tt.wantErr != nil {
				val, ok, _ := s.Get("k")
				if !ok || val != *tt.start {
					t.Errorf("after error, value = %q (exists=%v); want %q unchanged", val, ok, *tt.start)
				}
				return
			}
			// On success the new value is stored as a decimal string.
			if val, _, _ := s.Get("k"); val != fmt.Sprint(tt.want) {
				t.Errorf("stored value = %q, want %q", val, fmt.Sprint(tt.want))
			}
		})
	}
}

func ptr(s string) *string { return &s }

// If IncrBy did Get() then Set() without holding the lock the whole time,
// increments would be lost and the final count would be less than 100.
// Run with -race (make test does this).
func TestIncrConcurrent(t *testing.T) {
	s := New()
	const n = 100

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.IncrBy("counter", 1); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()

	got, _, _ := s.Get("counter")
	if got != fmt.Sprint(n) {
		t.Fatalf("counter = %s after %d concurrent INCRs, want %d", got, n, n)
	}
}

func TestMSet(t *testing.T) {
	s := New()
	if err := s.MSet("a", "1", "b", "2", "a", "3"); err != nil {
		t.Fatalf("MSet: %v", err)
	}
	if v, _, _ := s.Get("a"); v != "3" {
		t.Errorf(`a = %q after MSet a 1 b 2 a 3, want "3" (last one wins)`, v)
	}
	if v, _, _ := s.Get("b"); v != "2" {
		t.Errorf(`b = %q, want "2"`, v)
	}

	// An odd number of arguments is rejected, and nothing is changed.
	if err := s.MSet("a", "9", "c"); !errors.Is(err, ErrOddPairs) {
		t.Fatalf("MSet with odd args: err = %v, want ErrOddPairs", err)
	}
	if v, _, _ := s.Get("a"); v != "3" {
		t.Errorf(`a = %q after a rejected MSet, want "3" unchanged`, v)
	}
	if _, ok, _ := s.Get("c"); ok {
		t.Error(`key "c" was set by a rejected MSet`)
	}
}

func TestMGet(t *testing.T) {
	s := New()
	if err := s.MSet("a", "1", "empty", ""); err != nil {
		t.Fatal(err)
	}

	got, err := s.MGet("missing", "a", "empty", "a", "also-missing")
	if err != nil {
		t.Fatal(err)
	}
	want := []*string{nil, ptr("1"), ptr(""), ptr("1"), nil}

	if len(got) != len(want) {
		t.Fatalf("MGet returned %d values, want %d", len(got), len(want))
	}
	for i := range want {
		switch {
		case want[i] == nil && got[i] != nil:
			t.Errorf("value %d = %q, want nil (missing)", i, *got[i])
		case want[i] != nil && got[i] == nil:
			t.Errorf("value %d = nil, want %q", i, *want[i])
		case want[i] != nil && *got[i] != *want[i]:
			t.Errorf("value %d = %q, want %q", i, *got[i], *want[i])
		}
	}
}

func TestKeys(t *testing.T) {
	s := New()

	// An empty store gives an empty (non-nil) slice.
	got, err := s.Keys("*")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("Keys(*) on empty store = %#v, %v; want []string{}, nil", got, err)
	}

	if err := s.MSet("user:2", "x", "user:1", "x", "session:1", "x", "a/b", "x"); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		pattern string
		want    []string
	}{
		{"*", []string{"a/b", "session:1", "user:1", "user:2"}}, // sorted
		{"user:*", []string{"user:1", "user:2"}},
		{"*:1", []string{"session:1", "user:1"}},
		{"user:[2-9]", []string{"user:2"}},
		{"nope*", []string{}},
	}
	for _, tt := range tests {
		got, err := s.Keys(tt.pattern)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(tt.want) || got == nil {
			t.Errorf("Keys(%q) = %#v, %v; want %#v", tt.pattern, got, err, tt.want)
		}
	}
}

// MSET must be atomic: a reader must never see a half-applied MSET. One
// goroutine flips a and b between "x" and "y" together; readers check that
// a and b always agree. With a Set() loop instead of one lock, they would
// sometimes see a="y", b="x".
func TestMSetAtomic(t *testing.T) {
	s := New()
	if err := s.MSet("a", "x", "b", "x"); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() { // the writer
		defer wg.Done()
		defer close(done) // tell the readers to stop
		for i := 0; i < 2000; i++ {
			_ = s.MSet("a", "x", "b", "x")
			_ = s.MSet("a", "y", "b", "y")
		}
	}()

	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() { // the readers
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				vals, _ := s.MGet("a", "b")
				if *vals[0] != *vals[1] {
					t.Errorf("saw a half-applied MSET: a=%q b=%q", *vals[0], *vals[1])
					return
				}
			}
		}()
	}
	wg.Wait()
}

// fakeLogger records every Append call. If err is set, it fails instead.
type fakeLogger struct {
	mu    sync.Mutex
	calls [][]string
	err   error

	// store, if set, makes Append check that store's write lock is held,
	// and record a violation if it isn't.
	store      *Store
	violations []string

	// yield makes Append let other goroutines run first, so that a store
	// that logs outside its lock gets caught reordering writes.
	yield bool
}

func (f *fakeLogger) Append(args ...string) error {
	if f.store != nil && f.store.mu.TryRLock() {
		// We could take a read lock, so nobody holds the write lock: the
		// store is logging outside its lock.
		f.store.mu.RUnlock()
		f.mu.Lock()
		f.violations = append(f.violations, fmt.Sprint(args))
		f.mu.Unlock()
	}
	if f.yield {
		runtime.Gosched()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.calls = append(f.calls, append([]string(nil), args...))
	return nil
}

// take returns the calls recorded so far and clears them.
func (f *fakeLogger) take() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.calls
	f.calls = nil
	return calls
}

// snapshot returns every key and value in s.
func snapshot(t *testing.T, s *Store) map[string]string {
	t.Helper()
	keys, err := s.Keys("*")
	if err != nil {
		t.Fatal(err)
	}
	vals, err := s.MGet(keys...)
	if err != nil {
		t.Fatal(err)
	}
	m := make(map[string]string, len(keys))
	for i, k := range keys {
		m[k] = *vals[i]
	}
	return m
}

func TestWritesAreLogged(t *testing.T) {
	s := New()
	l := &fakeLogger{}
	l.store = s // also check that every write logs while holding the lock
	s.SetLogger(l)
	defer func() {
		for _, v := range l.violations {
			t.Errorf("logged %s without holding the store's write lock", v)
		}
	}()

	steps := []struct {
		name    string
		run     func() error
		wantErr error
		want    []string // the one logged command, or nil for "nothing logged"
	}{
		{"SET", func() error { return s.Set("a", "1") }, nil,
			[]string{"SET", "a", "1"}},
		{"MSET", func() error { return s.MSet("x", "1", "word", "abc") }, nil,
			[]string{"MSET", "x", "1", "word", "abc"}},
		{"INCR is logged as SET of the new value", func() error { _, err := s.IncrBy("x", 1); return err }, nil,
			[]string{"SET", "x", "2"}},
		{"DECR of a new key", func() error { _, err := s.IncrBy("n", -1); return err }, nil,
			[]string{"SET", "n", "-1"}},
		{"DEL logs only keys that existed, once each", func() error { _, err := s.Del("a", "missing", "a"); return err }, nil,
			[]string{"DEL", "a"}},
		{"SET max int64", func() error { return s.Set("big", "9223372036854775807") }, nil,
			[]string{"SET", "big", "9223372036854775807"}},

		// No-ops and failures change nothing, so they must log nothing.
		{"DEL of missing keys", func() error { _, err := s.Del("missing", "a"); return err }, nil, nil},
		{"DEL with no keys", func() error { _, err := s.Del(); return err }, nil, nil},
		{"INCR of a non-integer", func() error { _, err := s.IncrBy("word", 1); return err }, ErrNotInteger, nil},
		{"INCR overflow", func() error { _, err := s.IncrBy("big", 1); return err }, ErrOverflow, nil},
		{"MSET with an odd number of args", func() error { return s.MSet("z") }, ErrOddPairs, nil},
		{"MSET with no args", func() error { return s.MSet() }, nil, nil},
		{"reads", func() error {
			_, _, _ = s.Get("x")
			_, _ = s.Exists("x")
			_, _ = s.MGet("x", "n")
			_, _ = s.Keys("*")
			return nil
		}, nil, nil},
	}

	for _, st := range steps {
		err := st.run()
		if !errors.Is(err, st.wantErr) {
			t.Fatalf("%s: err = %v, want %v", st.name, err, st.wantErr)
		}
		got := l.take()
		var want [][]string
		if st.want != nil {
			want = [][]string{st.want}
		}
		if fmt.Sprint(got) != fmt.Sprint(want) || len(got) != len(want) {
			t.Errorf("%s: logged %q, want %q", st.name, got, want)
		}
	}
}

// If the log can't be written, the change must not happen in memory either.
func TestLoggerErrorLeavesMapUnchanged(t *testing.T) {
	s := New()
	if err := s.MSet("a", "1", "n", "5"); err != nil { // before a logger is attached
		t.Fatal(err)
	}
	before := snapshot(t, s)

	diskFull := errors.New("no space left on device")
	s.SetLogger(&fakeLogger{err: diskFull})

	ops := []struct {
		name string
		run  func() error
	}{
		{"SET existing", func() error { return s.Set("a", "2") }},
		{"SET new", func() error { return s.Set("new", "x") }},
		{"DEL", func() error { _, err := s.Del("a"); return err }},
		{"INCR", func() error { _, err := s.IncrBy("n", 1); return err }},
		{"INCR new key", func() error { _, err := s.IncrBy("fresh", 1); return err }},
		{"MSET", func() error { return s.MSet("a", "9", "b", "9") }},
	}
	for _, op := range ops {
		if err := op.run(); !errors.Is(err, diskFull) {
			t.Errorf("%s: err = %v, want the logger's error", op.name, err)
		}
		if after := snapshot(t, s); fmt.Sprint(after) != fmt.Sprint(before) {
			t.Fatalf("%s: data changed to %v after a failed log, want %v", op.name, after, before)
		}
	}
}

// The log must record changes in exactly the order they were applied, even
// with many goroutines writing at once. Replaying the log into a fresh store
// must rebuild exactly the same data.
//
// A wrong order only shows up at the end if it hits the LAST writes to a
// key (any later write hides it). So instead of one long run, we do many
// short rounds where every goroutine writes the same hot key, and check
// after each round. TestWritesAreLogged also checks the lock directly.
func TestLogOrderMatchesApplyOrder(t *testing.T) {
	for round := 0; round < 200; round++ {
		s := New()
		l := &fakeLogger{yield: true} // widens the window a buggy store would leave open
		s.SetLogger(l)

		var wg sync.WaitGroup
		for g := 0; g < 8; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 8; i++ {
					switch (g + i) % 4 {
					case 0:
						_ = s.Set("k", fmt.Sprint(g*100+i))
					case 1:
						_, _ = s.IncrBy("k", 1) // may fail if "k" was just deleted; that's fine
					case 2:
						_ = s.MSet("k", fmt.Sprint(g), "other", fmt.Sprint(i))
					case 3:
						_, _ = s.Del("k")
					}
				}
			}(g)
		}
		wg.Wait()

		replayed := New()
		for _, c := range l.take() {
			var err error
			switch c[0] {
			case "SET":
				err = replayed.Set(c[1], c[2])
			case "MSET":
				err = replayed.MSet(c[1:]...)
			case "DEL":
				_, err = replayed.Del(c[1:]...)
			default:
				t.Fatalf("unexpected logged command %q", c)
			}
			if err != nil {
				t.Fatalf("replaying %q: %v", c, err)
			}
		}

		if got, want := snapshot(t, replayed), snapshot(t, s); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("round %d: replaying the log gave %v, but the live store has %v", round, got, want)
		}
	}
}
