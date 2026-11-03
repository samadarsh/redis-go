package store

import (
	"errors"
	"fmt"
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
