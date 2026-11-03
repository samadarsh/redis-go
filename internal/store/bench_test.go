package store

import (
	"strconv"
	"testing"
)

// benchKeys are made once, up front, so the benchmarks measure the store
// and not fmt.Sprintf.
var benchKeys = func() []string {
	keys := make([]string, 1024)
	for i := range keys {
		keys[i] = "key:" + strconv.Itoa(i)
	}
	return keys
}()

func filledStore(b *testing.B) *Store {
	b.Helper()
	s := New()
	for _, k := range benchKeys {
		if err := s.Set(k, "value"); err != nil {
			b.Fatal(err)
		}
	}
	return s
}

func BenchmarkSet(b *testing.B) {
	s := New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = s.Set(benchKeys[i%len(benchKeys)], "value")
	}
}

func BenchmarkGet(b *testing.B) {
	s := filledStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = s.Get(benchKeys[i%len(benchKeys)])
	}
}

func BenchmarkIncrBy(b *testing.B) {
	s := New()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = s.IncrBy(benchKeys[i%len(benchKeys)], 1)
	}
}

// Many goroutines reading at once. With an RWMutex, readers share the lock,
// so this should scale with cores better than writes do (compare with
// BenchmarkSetParallel).
func BenchmarkGetParallel(b *testing.B) {
	s := filledStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_, _, _ = s.Get(benchKeys[i%len(benchKeys)])
			i++
		}
	})
}

// Many goroutines writing at once: every write needs the lock to itself, so
// they queue up. This is the cost of one lock for the whole store.
func BenchmarkSetParallel(b *testing.B) {
	s := filledStore(b)
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			_ = s.Set(benchKeys[i%len(benchKeys)], "value")
			i++
		}
	})
}
