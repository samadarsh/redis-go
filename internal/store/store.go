// Package store holds the in-memory key-value data.
package store

import (
	"errors"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"
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

// Store is a thread-safe map from string keys to string values, where a
// key can optionally have an expiry time.
//
// Every client connection runs in its own goroutine, so many goroutines
// use the same Store at once. The RWMutex makes them take turns:
// many readers can hold it together, but a writer gets it alone.
//
// Expiry works in two ways, like Redis:
//   - Lazy: every command treats a key whose time has passed as missing.
//   - Active: a background goroutine (StartActiveExpiry) deletes expired
//     keys that nobody reads, so they don't use memory forever.
type Store struct {
	mu      sync.RWMutex      // protects everything below
	data    map[string]string // the actual key -> value storage
	expires map[string]int64  // key -> expiry time, in absolute unix milliseconds
	logger  Logger            // optional; nil means changes are not recorded
	now     func() time.Time  // the clock; tests replace it with a fake one
	loading bool              // true while replaying the AOF: nothing expires (see SetLoading)
}

// New returns an empty Store that uses the real clock.
func New() *Store {
	// make() creates the maps. Writing to a nil map would panic.
	return &Store{
		data:    make(map[string]string),
		expires: make(map[string]int64),
		now:     time.Now,
	}
}

// SetClock replaces the clock the Store uses to decide whether keys have
// expired. Tests pass a fake clock they can move forward instantly, so
// they never have to sleep.
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.now = now
}

// SetLoading turns "loading mode" on or off. Turn it on while replaying the
// AOF, and off when replay is done.
//
// While loading, NOTHING expires: every key keeps its stored deadline, even
// one that is already in the past, and commands with a past expiry time
// store that time instead of deleting the key. This is what real Redis does.
//
// Why: each command in the file must be replayed against exactly the state
// it originally saw. Take "SET k v EX 10", then "PERSIST k", replayed an hour
// later. If the first command deleted k because its 10 seconds are long
// gone, the PERSIST would find no key, and a key that should live forever
// would be lost. With expiry paused, the PERSIST works as it did originally.
// Keys whose time really has passed are removed by normal lazy and active
// expiry as soon as loading is turned off.
func (s *Store) SetLoading(loading bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loading = loading
}

// NowMs returns the Store's current time in unix milliseconds. The command
// handler uses it to turn "EX 10" (relative) into an absolute expiry time.
func (s *Store) NowMs() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nowMs()
}

// nowMs reads the clock. The caller must hold s.mu (read or write).
func (s *Store) nowMs() int64 { return s.now().UnixMilli() }

// SetLogger attaches a Logger. From then on, every write method calls
// l.Append WHILE HOLDING the write lock, before changing the map:
//
//	validate / compute  ->  l.Append  ->  apply to the map
//
// Holding the lock means the log order is always the same as the order the
// changes were applied in. Logging first means that if Append fails, the
// map is left unchanged, so memory and the file never disagree.
//
// Expiry times are always logged as ABSOLUTE unix milliseconds (PEXPIREAT,
// SET ... PXAT), never as "in 10 seconds", so replaying the file later
// can't give a key extra life.
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

// --- Expiry helpers ---------------------------------------------------------

// isExpired reports whether key has an expiry time that has been reached.
// It only reads, so it is safe under s.mu.RLock(). While loading, nothing
// counts as expired (see SetLoading).
func (s *Store) isExpired(key string, now int64) bool {
	at, ok := s.expires[key]
	return ok && at <= now && !s.loading
}

// inPast reports whether an expiry time given to a command has already
// passed, meaning the key should be deleted instead. Never while loading.
func (s *Store) inPast(atMs, now int64) bool {
	return atMs <= now && !s.loading
}

// lookup returns key's value, treating an expired key as missing. It only
// reads, so it is safe under s.mu.RLock(). It must NOT delete the expired
// key: deleting is a write, and under a read lock other readers are using
// the maps at the same time.
func (s *Store) lookup(key string, now int64) (string, bool) {
	v, ok := s.data[key]
	if !ok || s.isExpired(key, now) {
		return "", false
	}
	return v, true
}

// removeIfExpired deletes key if its time has passed. This is the "lazy"
// half of expiry. The caller must hold s.mu.Lock() (the write lock).
//
// The deletion is not logged: the AOF already holds the key's absolute
// expiry time, so replaying it removes the key again by itself.
func (s *Store) removeIfExpired(key string, now int64) {
	if s.isExpired(key, now) {
		delete(s.data, key)
		delete(s.expires, key)
	}
}

// --- Writes -----------------------------------------------------------------

// Set stores value under key, replacing any existing value. Like Redis, a
// plain SET also REMOVES any expiry the key had.
func (s *Store) Set(key, value string) error {
	s.mu.Lock()         // write lock: everyone else waits
	defer s.mu.Unlock() // released automatically when we return

	if err := s.log("SET", key, value); err != nil {
		return err // not saved to disk, so don't change memory either
	}
	s.data[key] = value
	delete(s.expires, key)
	return nil
}

// SetExpireAt stores value under key and makes it expire at atMs (absolute
// unix milliseconds). This is SET key value EX/PX/PXAT.
//
// It is logged as ONE command, "SET key value PXAT atMs", rather than a SET
// followed by a PEXPIREAT. Two appends aren't atomic: if the server died
// between them, the file would hold a SET with no expiry, and the key would
// come back as permanent after a restart.
//
// If atMs is already in the past, the key is deleted instead (like Redis),
// and that is logged as a DEL. While loading, the past time is stored
// instead, and expiry deletes the key once loading is done.
func (s *Store) SetExpireAt(key, value string, atMs int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.nowMs()
	if s.inPast(atMs, now) {
		s.removeIfExpired(key, now)
		if _, exists := s.data[key]; !exists {
			return nil // nothing to set and nothing to delete
		}
		if err := s.log("DEL", key); err != nil {
			return err
		}
		delete(s.data, key)
		delete(s.expires, key)
		return nil
	}

	if err := s.log("SET", key, value, "PXAT", strconv.FormatInt(atMs, 10)); err != nil {
		return err
	}
	s.data[key] = value
	s.expires[key] = atMs
	return nil
}

// Del removes the given keys and returns how many of them actually existed.
// An expired key counts as not existing. Its expiry is removed with it.
func (s *Store) Del(keys ...string) (deleted int, err error) {
	s.mu.Lock() // one lock for the whole thing, so DEL a b c happens all at once
	defer s.mu.Unlock()
	now := s.nowMs()

	// 1. Work out which keys will actually be deleted (each one once,
	//    so DEL a a a deletes and counts "a" a single time).
	var toDelete []string
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		s.removeIfExpired(key, now)
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
		delete(s.expires, key)
	}
	return len(toDelete), nil
}

// IncrBy adds delta to the integer stored at key and returns the new value.
// INCR is IncrBy(key, 1) and DECR is IncrBy(key, -1).
//
// A missing (or expired) key counts as 0. If the stored value is not an
// integer, or the result would not fit in an int64, it returns an error and
// leaves the value unchanged. Like Redis, INCR KEEPS the key's expiry.
func (s *Store) IncrBy(key string, delta int64) (int64, error) {
	// One write lock around the whole read-modify-write. If we called Get()
	// and then Set(), another client could change the key in between and
	// one of the two increments would be lost.
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeIfExpired(key, s.nowMs())

	var current int64 // a missing key counts as 0
	if str, ok := s.data[key]; ok {
		n, ok := ParseInt(str)
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
	//
	// But a plain SET removes the expiry, and INCR must keep it. So if the
	// key has one, we log it as part of the same SET, as an absolute time:
	// "SET counter 11 PXAT 1767225600000". That makes each logged command
	// complete on its own: replaying it gives the right value AND deadline
	// without depending on what earlier commands left behind.
	args := []string{key, str}
	if at, ok := s.expires[key]; ok {
		args = append(args, "PXAT", strconv.FormatInt(at, 10))
	}
	if err := s.log("SET", args...); err != nil {
		return 0, err
	}
	s.data[key] = str // s.expires[key] is left exactly as it was
	return next, nil
}

// MSet stores several key/value pairs, given as a flat list:
// MSet("k1", "v1", "k2", "v2"). Pairs are applied in order, so if a key
// appears twice the last value wins, like real Redis.
//
// Every pair is set under one write lock, so MSET is atomic: no other
// client can ever see only some of the keys updated. Like SET, MSET removes
// any expiry the keys had.
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
		delete(s.expires, pairs[i])
	}
	return nil
}

// PExpireAt makes key expire at atMs (absolute unix milliseconds). It is
// what EXPIRE, PEXPIRE and PEXPIREAT all use. It returns false if the key
// doesn't exist.
//
// If atMs is already in the past (e.g. EXPIRE key 0, or EXPIRE key -5), the
// key is deleted straight away, like Redis, and that is logged as a DEL
// (except while loading, see SetLoading). Otherwise it is logged as
// "PEXPIREAT key atMs": an absolute time.
func (s *Store) PExpireAt(key string, atMs int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowMs()
	s.removeIfExpired(key, now)

	if _, exists := s.data[key]; !exists {
		return false, nil
	}
	if s.inPast(atMs, now) {
		if err := s.log("DEL", key); err != nil {
			return false, err
		}
		delete(s.data, key)
		delete(s.expires, key)
		return true, nil
	}

	if err := s.log("PEXPIREAT", key, strconv.FormatInt(atMs, 10)); err != nil {
		return false, err
	}
	s.expires[key] = atMs
	return true, nil
}

// Persist removes key's expiry, so it lives forever. It returns true if an
// expiry was removed, and false if the key doesn't exist or had none.
func (s *Store) Persist(key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeIfExpired(key, s.nowMs())

	if _, hasTTL := s.expires[key]; !hasTTL {
		return false, nil // missing, or already permanent: nothing to log
	}
	if err := s.log("PERSIST", key); err != nil {
		return false, err
	}
	delete(s.expires, key)
	return true, nil
}

// --- Reads ------------------------------------------------------------------
//
// These hold only the read lock, so they use lookup/isExpired to treat an
// expired key as missing, and never delete anything.

// Get returns the value stored under key. ok is false if the key does not
// exist or has expired.
func (s *Store) Get(key string) (value string, ok bool, err error) {
	s.mu.RLock() // read lock: other readers can run at the same time
	defer s.mu.RUnlock()

	value, ok = s.lookup(key, s.nowMs())
	return value, ok, nil
}

// Exists returns how many of the given keys exist. A key listed more than
// once is counted each time, like real Redis: EXISTS a a a returns 3.
func (s *Store) Exists(keys ...string) (count int, err error) {
	s.mu.RLock() // read lock: Exists only looks at the map, never changes it
	defer s.mu.RUnlock()
	now := s.nowMs()

	for _, key := range keys {
		if _, ok := s.lookup(key, now); ok {
			count++
		}
	}
	return count, nil
}

// MGet returns the value of each key, in the same order as keys. A missing
// or expired key gives a nil pointer, which is how we tell "missing" apart
// from an existing key whose value is the empty string "".
//
// All keys are read under one read lock, so the result is a consistent
// snapshot: an MSET from another client is either fully visible or not at all.
func (s *Store) MGet(keys ...string) ([]*string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowMs()

	values := make([]*string, len(keys)) // starts as all nil, i.e. all missing
	for i, key := range keys {
		if v, ok := s.lookup(key, now); ok {
			values[i] = &v // v is a new variable each iteration, so each pointer is distinct
		}
	}
	return values, nil
}

// Keys returns every key matching a Redis glob pattern (see globMatch),
// sorted so the output is deterministic. With no matches it returns an
// empty slice. Expired keys are left out.
//
// It looks at every key in the store, which is why real Redis warns
// against using KEYS on a large production database.
func (s *Store) Keys(pattern string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowMs()

	matches := []string{}
	for key := range s.data {
		if !s.isExpired(key, now) && globMatch(pattern, key) {
			matches = append(matches, key)
		}
	}
	sort.Strings(matches) // Go map iteration order is random
	return matches, nil
}

// PTTL returns how many milliseconds key has left to live, like Redis:
// -2 if the key doesn't exist (or has expired), -1 if it has no expiry.
func (s *Store) PTTL(key string) (int64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.nowMs()

	if _, ok := s.lookup(key, now); !ok {
		return -2, nil
	}
	at, hasTTL := s.expires[key]
	if !hasTTL {
		return -1, nil
	}
	return at - now, nil // > 0, because lookup already ruled out at <= now
}

// --- Active expiry ----------------------------------------------------------

// Redis's active expiry settings: look at 20 keys with an expiry at a time,
// and keep going straight away while more than 25% of them had expired.
const (
	expireSampleSize   = 20
	expireRepeatAbove  = expireSampleSize / 4 // more than 5 of 20 = more than 25%
	expireMaxCycleTime = 25 * time.Millisecond
)

// StartActiveExpiry starts a background goroutine that, every interval,
// deletes expired keys that nobody has read. Without it, a key that is
// never touched again would use memory forever (lazy expiry only removes
// keys when they are accessed).
//
// It returns a stop function, which stops the goroutine and waits until it
// has exited, so no goroutine is leaked. Calling stop more than once is fine.
func (s *Store) StartActiveExpiry(interval time.Duration) (stop func()) {
	quit := make(chan struct{})
	done := make(chan struct{})

	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ticker.C:
				s.activeExpireCycle()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() { close(quit) })
		<-done
	}
}

// activeExpireCycle is one run of Redis's sampling algorithm. Checking every
// key would mean holding the write lock for a long time on a big database,
// so instead it checks a small sample, and only keeps going if that sample
// suggests there are many more expired keys to find.
func (s *Store) activeExpireCycle() {
	start := time.Now() // real time: a budget for this cycle, not part of the data
	for {
		sampled, expired := s.expireSample()
		if expired <= expireRepeatAbove || sampled == 0 {
			return // 25% or fewer were expired: probably not worth looking further
		}
		if time.Since(start) > expireMaxCycleTime {
			return // don't spend too long in one cycle; the next tick will continue
		}
	}
}

// expireSample looks at up to expireSampleSize keys that have an expiry,
// deletes the ones whose time has passed, and reports how many it looked at
// and how many it deleted. It takes the write lock only for this one sample,
// so clients get a turn between samples.
//
// Go map iteration starts at a random position, so ranging over s.expires
// and stopping after 20 keys gives a different sample each time.
func (s *Store) expireSample() (sampled, expired int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loading {
		return 0, 0 // nothing expires while loading
	}
	now := s.nowMs()

	for key, at := range s.expires {
		if sampled == expireSampleSize {
			break
		}
		sampled++
		if at <= now {
			delete(s.data, key) // deleting while ranging over a map is allowed in Go
			delete(s.expires, key)
			expired++
		}
	}
	return sampled, expired
}

// --- Helpers ----------------------------------------------------------------

// ParseInt parses s as a base-10 int64 as strictly as Redis does.
//
// strconv.ParseInt alone is too lenient: it accepts "+5" and "05". So we
// also require that formatting the number again gives back exactly s. That
// rules out a leading "+", leading zeros and "-0", and ParseInt itself
// already rejects "", spaces, "1.5" and numbers outside the int64 range.
func ParseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || strconv.FormatInt(n, 10) != s {
		return 0, false
	}
	return n, true
}
