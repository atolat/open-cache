package evictor

import (
	"container/list"
	"sync"
)

// LRU implements least-recently-used eviction.
//
// It maintains a doubly-linked list ordered by access time. The front
// of the list is the most recently used, the back is the least recently
// used. On eviction, we remove from the back.
//
// This is the classic LRU data structure: a linked list for ordering
// plus a map for O(1) lookups by key.
type LRU struct {
	mu    sync.Mutex
	order *list.List            // front = most recent, back = least recent
	index map[string]*list.Element // key → list element for O(1) access
}

// lruEntry is what we store in each list element.
type lruEntry struct {
	key       string
	sizeBytes int64
}

// NewLRU creates an LRU evictor.
func NewLRU() *LRU {
	return &LRU{
		order: list.New(),
		index: make(map[string]*list.Element),
	}
}

// Track records that a key was accessed. If the key already exists,
// it moves to the front (most recent). If new, it's added to the front.
func (l *LRU) Track(key string, sizeBytes int64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// Key already tracked — move to front (most recently used).
	if elem, ok := l.index[key]; ok {
		l.order.MoveToFront(elem)
		return
	}

	// New key — add to front.
	entry := &lruEntry{key: key, sizeBytes: sizeBytes}
	elem := l.order.PushFront(entry)
	l.index[key] = elem
}

// Evict returns the least recently used key.
// Does not remove it — the caller should call Remove after deleting
// the entry from the cache.
func (l *LRU) Evict() (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	// The back of the list is the least recently used.
	elem := l.order.Back()
	if elem == nil {
		return "", false
	}

	entry := elem.Value.(*lruEntry)
	return entry.key, true
}

// Remove removes a key from the LRU tracking.
// Called after the cache has deleted the actual data.
func (l *LRU) Remove(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, ok := l.index[key]; ok {
		l.order.Remove(elem)
		delete(l.index, key)
	}
}
