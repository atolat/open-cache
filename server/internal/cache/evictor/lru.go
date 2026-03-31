package evictor

import (
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"time"
)

// LRU implements approximate least-recently-used eviction.
//
// Instead of maintaining a sorted linked list (expensive under contention),
// each key stores a last-access timestamp. On eviction, we sample N random
// keys and evict the one with the oldest timestamp.
//
// This is the same approach Redis/Valkey uses. With a sample size of 10,
// eviction quality is nearly identical to true LRU, but reads don't
// need to acquire a lock to update ordering.
//
// Performance characteristics:
//   - Track: O(1), atomic timestamp write, no lock
//   - Evict: O(sampleSize), lock held briefly to sample
//   - Remove: O(1)
type LRU struct {
	mu      sync.Mutex
	entries map[string]*lruEntry
	keys    []string // all keys, for random sampling

	sampleSize int // how many random keys to compare on eviction
}

// lruEntry stores the last access time for a key.
// Using atomic int64 so Track() doesn't need a lock.
type lruEntry struct {
	lastAccess atomic.Int64 // unix nanoseconds
	sizeBytes  int64
	keyIndex   int // position in the keys slice, for O(1) removal
}

// NewLRU creates an approximate LRU evictor.
//
// sampleSize controls eviction accuracy vs speed. Higher values give
// better eviction choices but take longer. Redis defaults to 5.
// 10 is nearly identical to true LRU.
func NewLRU() *LRU {
	return &LRU{
		entries:    make(map[string]*lruEntry),
		keys:       make([]string, 0),
		sampleSize: 10,
	}
}

// Track records that a key was accessed.
// Updates the last-access timestamp atomically — no lock needed.
// If the key is new, a lock is briefly held to add it to the index.
func (l *LRU) Track(key string, sizeBytes int64) {
	now := time.Now().UnixNano()

	// Fast path: key already exists. Just update the timestamp atomically.
	l.mu.Lock()
	entry, exists := l.entries[key]
	l.mu.Unlock()

	if exists {
		entry.lastAccess.Store(now)
		return
	}

	// Slow path: new key. Need the lock to add to the map and keys slice.
	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring lock (another goroutine may have added it).
	if entry, exists = l.entries[key]; exists {
		entry.lastAccess.Store(now)
		return
	}

	entry = &lruEntry{
		sizeBytes: sizeBytes,
		keyIndex:  len(l.keys),
	}
	entry.lastAccess.Store(now)

	l.entries[key] = entry
	l.keys = append(l.keys, key)
}

// Evict samples random keys and returns the one with the oldest
// last-access timestamp.
func (l *LRU) Evict() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	n := len(l.keys)
	if n == 0 {
		return "", false
	}

	// Sample up to sampleSize random keys. Pick the oldest.
	var oldestKey string
	var oldestTime int64 = 1<<63 - 1 // max int64

	samples := l.sampleSize
	if samples > n {
		samples = n
	}

	for i := 0; i < samples; i++ {
		key := l.keys[rand.IntN(n)]
		entry := l.entries[key]
		t := entry.lastAccess.Load()
		if t < oldestTime {
			oldestTime = t
			oldestKey = key
		}
	}

	return oldestKey, true
}

// Remove removes a key from the evictor.
// Uses swap-with-last for O(1) removal from the keys slice.
func (l *LRU) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	entry, ok := l.entries[key]
	if !ok {
		return
	}

	// Swap this key with the last key in the slice, then truncate.
	// This avoids shifting all elements after the removed index.
	lastIdx := len(l.keys) - 1
	lastKey := l.keys[lastIdx]

	l.keys[entry.keyIndex] = lastKey
	l.entries[lastKey].keyIndex = entry.keyIndex
	l.keys = l.keys[:lastIdx]

	delete(l.entries, key)
}
