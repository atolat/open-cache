// Package cache defines the interface for cache storage backends.
//
// Any storage backend (in-memory, Valkey, groupcache) implements this
// interface. The tiered cache uses it to compose L1 and L2 layers
// without knowing what's behind them.
package cache

// Store is the interface for a cache backend.
// Implementations must be safe for concurrent use.
type Store interface {
	// Get returns the value for a key.
	// Returns nil, false if the key is not found.
	Get(key string) ([]byte, bool)

	// Put stores a value for a key.
	// If the store is full, it calls the evictor to make space.
	Put(key string, value []byte)

	// Has returns true if the key exists without returning the value.
	// Used for HEAD requests.
	Has(key string) bool

	// Len returns the number of entries in the store.
	Len() int

	// Size returns the total bytes used by all stored values.
	Size() int64
}
