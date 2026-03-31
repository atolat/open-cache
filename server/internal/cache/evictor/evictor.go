// Package evictor defines the interface for cache eviction policies.
//
// The cache calls the evictor when it needs to free space. Different
// implementations make different tradeoffs:
//   - LRU: simple, evicts least recently used (ships first)
//   - GDSF: size-aware, better for mixed workloads (planned)
//   - RL agent: DAG-aware learned policy (research phase)
package evictor

// Entry represents a cached item that the evictor can reason about.
// The evictor doesn't see the actual data — just metadata about each entry.
type Entry struct {
	Key       string
	SizeBytes int64
}

// Evictor decides which entries to remove when the cache is full.
// Implementations must be safe for concurrent use.
type Evictor interface {
	// Track tells the evictor about a new or accessed entry.
	// Called on every Get (hit) and Put.
	Track(key string, sizeBytes int64)

	// Evict returns the key that should be removed to free space.
	// Called when the cache exceeds its size limit.
	// Returns the key to evict and true, or "", false if nothing to evict.
	Evict() (key string, ok bool)

	// Remove tells the evictor an entry was removed from the cache.
	// Called after the cache deletes the entry.
	Remove(key string)
}
