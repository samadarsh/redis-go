// Package store holds the in-memory key-value data.
package store

import (
	"errors"
	"sync"
)

// ErrNotImplemented is still used by the tests and the command handler,
// so keep it for now. We'll clean it up in a later milestone.
var ErrNotImplemented = errors.New("not implemented yet")

// Store is a thread-safe map from string keys to string values.
//
// Every client connection runs in its own goroutine, so many goroutines
// use the same Store at once. The RWMutex makes them take turns:
// many readers can hold it together, but a writer gets it alone.
type Store struct {
	mu   sync.RWMutex      // the lock that protects data
	data map[string]string // the actual key -> value storage
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
	s.mu.Lock() // one lock for the whole loop, so DEL a b c happens all at once
	defer s.mu.Unlock()

	for _, key := range keys {
		if _, exists := s.data[key]; exists {
			delete(s.data, key)
			deleted++
		}
	}
	return deleted, nil
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
