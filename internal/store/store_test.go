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
