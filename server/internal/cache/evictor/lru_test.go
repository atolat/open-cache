package evictor_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/atolat/open-cache/internal/cache/evictor"
)

func TestLRU_EvictsOldest(t *testing.T) {
	lru := evictor.NewLRU()

	// Add entries with time gaps so timestamps are clearly different.
	lru.Track("old", 100)
	time.Sleep(time.Millisecond)
	lru.Track("mid", 100)
	time.Sleep(time.Millisecond)
	lru.Track("new", 100)

	// With sample size 10 and only 3 keys, all keys are sampled.
	// The oldest ("old") should always be picked.
	key, ok := lru.Evict()
	if !ok {
		t.Fatal("Evict() should return true")
	}
	if key != "old" {
		t.Errorf("Evict() = %q, want %q", key, "old")
	}
}

func TestLRU_AccessUpdatesTimestamp(t *testing.T) {
	lru := evictor.NewLRU()

	lru.Track("a", 100)
	time.Sleep(time.Millisecond)
	lru.Track("b", 100)
	time.Sleep(time.Millisecond)

	// Access "a" again — its timestamp is now newest.
	lru.Track("a", 100)

	// "b" should be evicted (oldest timestamp).
	key, ok := lru.Evict()
	if !ok || key != "b" {
		t.Errorf("Evict() = %q, want %q (after accessing a)", key, "b")
	}
}

func TestLRU_RemoveThenEvict(t *testing.T) {
	lru := evictor.NewLRU()

	lru.Track("a", 100)
	time.Sleep(time.Millisecond)
	lru.Track("b", 100)
	time.Sleep(time.Millisecond)
	lru.Track("c", 100)

	// Remove "a". Next eviction should return "b".
	lru.Remove("a")

	key, ok := lru.Evict()
	if !ok || key != "b" {
		t.Errorf("Evict() = %q, want %q (after removing a)", key, "b")
	}
}

func TestLRU_EvictEmpty(t *testing.T) {
	lru := evictor.NewLRU()

	_, ok := lru.Evict()
	if ok {
		t.Error("Evict() on empty LRU should return false")
	}
}

func TestLRU_RemoveNonexistent(t *testing.T) {
	lru := evictor.NewLRU()

	// Should not panic.
	lru.Remove("nonexistent")
}

func TestLRU_RemoveLastKey(t *testing.T) {
	lru := evictor.NewLRU()

	lru.Track("only", 100)
	lru.Remove("only")

	_, ok := lru.Evict()
	if ok {
		t.Error("Evict() should return false after removing the only key")
	}
}

func TestLRU_ConcurrentAccess(t *testing.T) {
	lru := evictor.NewLRU()

	var wg sync.WaitGroup
	// 100 goroutines tracking, evicting, and removing concurrently.
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", n)
			lru.Track(key, 100)
			lru.Evict()
			lru.Track(key, 100) // re-track after potential eviction
		}(i)
	}
	wg.Wait()
	// No panics or races = pass.
}
