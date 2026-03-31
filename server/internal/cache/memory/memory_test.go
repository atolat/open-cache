package memory_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/atolat/open-cache/internal/cache/evictor"
	"github.com/atolat/open-cache/internal/cache/memory"
)

func TestGetMiss(t *testing.T) {
	store := memory.New(1024, evictor.NewLRU())

	_, ok := store.Get("nonexistent")
	if ok {
		t.Error("Get on empty store should return false")
	}
}

func TestPutThenGet(t *testing.T) {
	store := memory.New(1024, evictor.NewLRU())

	store.Put("key1", []byte("hello"))

	val, ok := store.Get("key1")
	if !ok {
		t.Fatal("Get should return true for existing key")
	}
	if string(val) != "hello" {
		t.Errorf("Get = %q, want %q", string(val), "hello")
	}
}

func TestHas(t *testing.T) {
	store := memory.New(1024, evictor.NewLRU())

	if store.Has("key1") {
		t.Error("Has should return false for missing key")
	}

	store.Put("key1", []byte("data"))

	if !store.Has("key1") {
		t.Error("Has should return true after Put")
	}
}

func TestSizeTracking(t *testing.T) {
	store := memory.New(1024, evictor.NewLRU())

	store.Put("a", []byte("12345"))    // 5 bytes
	store.Put("b", []byte("1234567890")) // 10 bytes

	if store.Size() != 15 {
		t.Errorf("Size = %d, want 15", store.Size())
	}
	if store.Len() != 2 {
		t.Errorf("Len = %d, want 2", store.Len())
	}
}

func TestOverwriteUpdatesSize(t *testing.T) {
	store := memory.New(1024, evictor.NewLRU())

	store.Put("key", []byte("short"))          // 5 bytes
	store.Put("key", []byte("much longer val")) // 15 bytes

	if store.Size() != 15 {
		t.Errorf("Size after overwrite = %d, want 15", store.Size())
	}
	if store.Len() != 1 {
		t.Errorf("Len after overwrite = %d, want 1", store.Len())
	}
}

func TestEvictionWhenFull(t *testing.T) {
	// Max 20 bytes. Insert entries until eviction kicks in.
	store := memory.New(20, evictor.NewLRU())

	store.Put("a", []byte("1234567890")) // 10 bytes, total: 10
	store.Put("b", []byte("1234567890")) // 10 bytes, total: 20
	store.Put("c", []byte("1234567890")) // 10 bytes, total would be 30 → evicts "a"

	// "a" should have been evicted (LRU — oldest).
	if store.Has("a") {
		t.Error("'a' should have been evicted")
	}

	// "b" and "c" should still exist.
	if !store.Has("b") {
		t.Error("'b' should still exist")
	}
	if !store.Has("c") {
		t.Error("'c' should still exist")
	}

	if store.Size() != 20 {
		t.Errorf("Size after eviction = %d, want 20", store.Size())
	}
}

func TestEvictionRespectsAccessOrder(t *testing.T) {
	store := memory.New(20, evictor.NewLRU())

	store.Put("a", []byte("1234567890")) // 10 bytes
	store.Put("b", []byte("1234567890")) // 10 bytes

	// Access "a" so it becomes most recently used.
	store.Get("a")

	// Insert "c" — should evict "b" (least recently used), not "a".
	store.Put("c", []byte("1234567890"))

	if !store.Has("a") {
		t.Error("'a' was accessed recently, should not be evicted")
	}
	if store.Has("b") {
		t.Error("'b' should have been evicted (least recently used)")
	}
	if !store.Has("c") {
		t.Error("'c' was just inserted, should exist")
	}
}

func TestBlobLargerThanCache(t *testing.T) {
	// Cache max is 10 bytes. A 20-byte blob should be silently rejected.
	store := memory.New(10, evictor.NewLRU())

	store.Put("small", []byte("12345")) // 5 bytes — fits
	store.Put("huge", []byte("12345678901234567890")) // 20 bytes — too big

	if !store.Has("small") {
		t.Error("'small' should still exist")
	}
	if store.Has("huge") {
		t.Error("'huge' should have been rejected (larger than cache)")
	}
	if store.Size() != 5 {
		t.Errorf("Size = %d, want 5", store.Size())
	}
}

func TestConcurrentAccess(t *testing.T) {
	store := memory.New(1024*1024, evictor.NewLRU()) // 1MB

	var wg sync.WaitGroup
	// 100 goroutines writing and reading concurrently.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", n)
			store.Put(key, []byte("data"))
			store.Get(key)
			store.Has(key)
		}(i)
	}
	wg.Wait()

	// All 100 entries should exist (store is large enough).
	if store.Len() != 100 {
		t.Errorf("Len = %d, want 100 after concurrent writes", store.Len())
	}
}
