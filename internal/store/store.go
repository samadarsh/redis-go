// Package store holds the in-memory key-value data.
//
// ┌──────────────────────────────────────────────────────────────────────┐
// │ YOUR JOB (Milestone 3): implement the methods in this file.          │
// │                                                                      │
// │ Each method currently returns ErrNotImplemented. The tests in        │
// │ store_test.go skip themselves while that's true, so as soon as you   │
// │ implement a method its tests start running and checking your work.   │
// │ Run them with:  make test                                            │
// └──────────────────────────────────────────────────────────────────────┘
package store

import "errors"

// ErrNotImplemented marks the parts of the skeleton you still have to write.
// Delete it once every method is implemented.
var ErrNotImplemented = errors.New("not implemented yet")

// Store is a thread-safe map from string keys to string values.
type Store struct {
	// TODO(milestone 3): add the fields you need:
	//
	//   1. A map[string]string to hold the data.
	//   2. A sync.RWMutex to protect it. Every client connection runs in
	//      its own goroutine, so many goroutines read and write the map
	//      at the same time. Without a lock, Go will crash with
	//      "concurrent map writes" (and `go test -race` will catch it).
}

// New returns an empty Store.
func New() *Store {
	// TODO(milestone 3): initialise the map, e.g. data: make(map[string]string)
	return &Store{}
}

// Set stores value under key, replacing any existing value.
//
// It returns an error because in milestone 5 Set will also append to a
// file on disk, and that write can fail.
func (s *Store) Set(key, value string) error {
	// TODO(milestone 3): take the write lock (s.mu.Lock / defer s.mu.Unlock)
	// and store the value.
	return ErrNotImplemented
}

// Get returns the value stored under key. ok is false if the key does not
// exist.
func (s *Store) Get(key string) (value string, ok bool, err error) {
	// TODO(milestone 3): take the read lock (s.mu.RLock) and look up the key.
	return "", false, ErrNotImplemented
}

// Del removes the given keys and returns how many of them actually existed.
func (s *Store) Del(keys ...string) (deleted int, err error) {
	// TODO(milestone 3): take the write lock, delete each key that exists,
	// and count them.
	return 0, ErrNotImplemented
}
