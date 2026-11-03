// Package store holds the in-memory key-value data.
package store

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
)

// ErrNotImplemented is still used by the tests and the command handler,
// so keep it for now. We'll clean it up in a later milestone.
var ErrNotImplemented = errors.New("not implemented yet")

// Errors returned by IncrBy. The text matches real Redis, because the
// command handler sends it to the client as "ERR " + err.Error().
var (
	ErrNotInteger = errors.New("value is not an integer or out of range")
	ErrOverflow   = errors.New("increment or decrement would overflow")
)

// ErrOddPairs is returned by MSet when it is not given key/value pairs. The
// MSET handler checks this first, so a client never sees it.
var ErrOddPairs = errors.New("MSet needs an even number of arguments (key value pairs)")

// Logger records every change made to the Store, so it can be replayed
// later. *aof.AOF implements it.
type Logger interface {
	Append(args ...string) error
}

// Store is a thread-safe map from string keys to string values.
//
// Every client connection runs in its own goroutine, so many goroutines
// use the same Store at once. The RWMutex makes them take turns:
// many readers can hold it together, but a writer gets it alone.
type Store struct {
	mu     sync.RWMutex      // the lock that protects data and logger
	data   map[string]string // the actual key -> value storage
	logger Logger            // optional; nil means changes are not recorded
}

// SetLogger attaches a Logger. From then on, every write method calls
// l.Append WHILE HOLDING the write lock, before changing the map:
//
//	validate / compute  ->  l.Append  ->  apply to the map
//
// Holding the lock means the log order is always the same as the order the
// changes were applied in. Logging first means that if Append fails, the
// map is left unchanged, so memory and the file never disagree.
//
// Pass nil to stop logging. Attach the logger only AFTER replaying the AOF,
// or replay would write every command back into the file.
func (s *Store) SetLogger(l Logger) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger = l
}

// log records one change. The caller must hold s.mu.Lock().
func (s *Store) log(cmd string, args ...string) error {
	if s.logger == nil {
		return nil
	}
	return s.logger.Append(append([]string{cmd}, args...)...)
}

// New returns an empty Store.
func New() *Store {
	// make() creates the map. Writing to a nil map would panic.
	return &Store{data: make(map[string]string)}
}

// Set stores value under key, replacing any existing value.
//
// It returns an error because in milestone 5 Set will also append to a
// file on disk, and that write can fail.
func (s *Store) Set(key, value string) error {
	s.mu.Lock()         // write lock: everyone else waits
	defer s.mu.Unlock() // released automatically when we return

	if err := s.log("SET", key, value); err != nil {
		return err // not saved to disk, so don't change memory either
	}
	s.data[key] = value
	return nil
}

// Get returns the value stored under key. ok is false if the key does not
// exist.
func (s *Store) Get(key string) (value string, ok bool, err error) {
	s.mu.RLock() // read lock: other readers can run at the same time
	defer s.mu.RUnlock()

	value, ok = s.data[key] // "comma ok": ok tells us if the key exists
	return value, ok, nil
}

// Del removes the given keys and returns how many of them actually existed.
func (s *Store) Del(keys ...string) (deleted int, err error) {
	s.mu.Lock() // one lock for the whole thing, so DEL a b c happens all at once
	defer s.mu.Unlock()

	// 1. Work out which keys will actually be deleted (each one once,
	//    so DEL a a a deletes and counts "a" a single time).
	var toDelete []string
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if _, exists := s.data[key]; !exists {
			continue
		}
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		toDelete = append(toDelete, key)
	}
	if len(toDelete) == 0 {
		return 0, nil // nothing changes, so nothing to log
	}

	// 2. Log exactly what we are about to delete. 3. Then delete it.
	if err := s.log("DEL", toDelete...); err != nil {
		return 0, err
	}
	for _, key := range toDelete {
		delete(s.data, key)
	}
	return len(toDelete), nil
}

// Exists returns how many of the given keys exist. A key listed more than
// once is counted each time, like real Redis: EXISTS a a a returns 3.
func (s *Store) Exists(keys ...string) (count int, err error) {
	s.mu.RLock() // read lock: Exists only looks at the map, never changes it
	defer s.mu.RUnlock()

	for _, key := range keys {
		if _, exists := s.data[key]; exists {
			count++
		}
	}
	return count, nil
}

// IncrBy adds delta to the integer stored at key and returns the new value.
// INCR is IncrBy(key, 1) and DECR is IncrBy(key, -1).
//
// A missing key counts as 0. If the stored value is not an integer, or the
// result would not fit in an int64, it returns an error and leaves the value
// unchanged.
func (s *Store) IncrBy(key string, delta int64) (int64, error) {
	// One write lock around the whole read-modify-write. If we called Get()
	// and then Set(), another client could change the key in between and
	// one of the two increments would be lost.
	s.mu.Lock()
	defer s.mu.Unlock()

	var current int64 // a missing key counts as 0
	if str, ok := s.data[key]; ok {
		n, ok := parseInt(str)
		if !ok {
			return 0, ErrNotInteger
		}
		current = n
	}

	// Check for overflow BEFORE adding: in Go, int64 addition silently
	// wraps around (MaxInt64 + 1 becomes MinInt64), so afterwards it's too late.
	if delta > 0 && current > math.MaxInt64-delta {
		return 0, ErrOverflow
	}
	if delta < 0 && current < math.MinInt64-delta {
		return 0, ErrOverflow
	}

	next := current + delta
	str := strconv.FormatInt(next, 10)

	// Log the RESULT ("SET counter 11"), not the operation ("INCRBY counter
	// 1"). Replaying a SET gives the same value no matter what state the
	// replay happens to be in, so it can't drift.
	if err := s.log("SET", key, str); err != nil {
		return 0, err
	}
	s.data[key] = str
	return next, nil
}

// MSet stores several key/value pairs, given as a flat list:
// MSet("k1", "v1", "k2", "v2"). Pairs are applied in order, so if a key
// appears twice the last value wins, like real Redis.
//
// Every pair is set under one write lock, so MSET is atomic: no other
// client can ever see only some of the keys updated.
func (s *Store) MSet(pairs ...string) error {
	if len(pairs)%2 != 0 {
		return ErrOddPairs // checked before locking, and nothing is changed
	}
	if len(pairs) == 0 {
		return nil // nothing to set; logging a bare "MSET" would break replay
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.log("MSET", pairs...); err != nil {
		return err
	}
	for i := 0; i < len(pairs); i += 2 {
		s.data[pairs[i]] = pairs[i+1] // pairs[i] is a key, pairs[i+1] its value
	}
	return nil
}

// MGet returns the value of each key, in the same order as keys. A missing
// key gives a nil pointer, which is how we tell "missing" apart from an
// existing key whose value is the empty string "".
//
// All keys are read under one read lock, so the result is a consistent
// snapshot: an MSET from another client is either fully visible or not at all.
func (s *Store) MGet(keys ...string) ([]*string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	values := make([]*string, len(keys)) // starts as all nil, i.e. all missing
	for i, key := range keys {
		if v, ok := s.data[key]; ok {
			values[i] = &v // v is a new variable each iteration, so each pointer is distinct
		}
	}
	return values, nil
}

// Keys returns every key matching a Redis glob pattern (see globMatch),
// sorted so the output is deterministic. With no matches it returns an
// empty slice.
//
// It looks at every key in the store, which is why real Redis warns
// against using KEYS on a large production database.
func (s *Store) Keys(pattern string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	matches := []string{}
	for key := range s.data {
		if globMatch(pattern, key) {
			matches = append(matches, key)
		}
	}
	sort.Strings(matches) // Go map iteration order is random
	return matches, nil
}

// parseInt parses s as a base-10 int64 as strictly as Redis does.
//
// strconv.ParseInt alone is too lenient: it accepts "+5" and "05". So we
// also require that formatting the number again gives back exactly s. That
// rules out a leading "+", leading zeros and "-0", and ParseInt itself
// already rejects "", spaces, "1.5" and numbers outside the int64 range.
func parseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != s {
		return 0, false
	}
	return n, true
}
