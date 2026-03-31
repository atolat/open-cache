// Package memory provides an in-memory cache store.
//
// It's a concurrent-safe hashmap with a configurable size limit.
// When the limit is reached, it asks an evictor to choose which
// entry to remove. The evictor is pluggable — LRU ships first,
// DAG-aware RL agent comes later.
package memory

import (
	"sync"

	"github.com/atolat/open-cache/internal/cache/evictor"
)

// Store is an in-memory key-value store with bounded size.
type Store struct {
	mu       sync.RWMutex
	data     map[string][]byte
	evictor  evictor.Evictor
	maxBytes int64 // maximum total size of all stored values
	curBytes int64 // current total size of all stored values
}

// New creates an in-memory store.
//
// maxBytes is the maximum total size of all stored values.
// When exceeded, the evictor is called to free space.
// ev is the eviction policy (e.g., evictor.NewLRU()).
func New(maxBytes int64, ev evictor.Evictor) *Store {
	return &Store{
		data:     make(map[string][]byte),
		evictor:  ev,
		maxBytes: maxBytes,
	}
}

// Get returns the value for a key.
// Returns nil, false if the key is not found.
func (s *Store) Get(key string) ([]byte, bool) {
	s.mu.RLock()
	val, ok := s.data[key]
	s.mu.RUnlock()

	if ok {
		// Tell the evictor this key was accessed.
		// This keeps it "fresh" in LRU ordering.
		s.evictor.Track(key, int64(len(val)))
	}
	return val, ok
}

// Put stores a value. If the store is over its size limit after
// inserting, it evicts entries until it's under the limit.
func (s *Store) Put(key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	size := int64(len(value))

	// If the key already exists, subtract its old size first.
	if old, exists := s.data[key]; exists {
		s.curBytes -= int64(len(old))
	}

	// Store the entry.
	s.data[key] = value
	s.curBytes += size

	// Tell the evictor about this entry.
	s.evictor.Track(key, size)

	// Evict until we're under the limit.
	for s.curBytes > s.maxBytes {
		victim, ok := s.evictor.Evict()
		if !ok {
			break // nothing left to evict
		}
		s.removeLocked(victim)
	}
}

// Has returns true if the key exists.
func (s *Store) Has(key string) bool {
	s.mu.RLock()
	_, ok := s.data[key]
	s.mu.RUnlock()
	return ok
}

// Len returns the number of entries.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.data)
}

// Size returns the total bytes used by stored values.
func (s *Store) Size() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.curBytes
}

// removeLocked deletes an entry from the store and updates the size.
// Caller must hold s.mu write lock.
func (s *Store) removeLocked(key string) {
	if val, ok := s.data[key]; ok {
		s.curBytes -= int64(len(val))
		delete(s.data, key)
		s.evictor.Remove(key)
	}
}
