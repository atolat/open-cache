package evictor_test

import (
	"testing"

	"github.com/atolat/open-cache/internal/cache/evictor"
)

func TestLRU_EvictsLeastRecentlyUsed(t *testing.T) {
	lru := evictor.NewLRU()

	// Add three entries in order: a, b, c.
	// "a" is the oldest (least recently used).
	lru.Track("a", 100)
	lru.Track("b", 200)
	lru.Track("c", 300)

	key, ok := lru.Evict()
	if !ok || key != "a" {
		t.Errorf("Evict() = %q, want %q", key, "a")
	}
}

func TestLRU_AccessMovesToFront(t *testing.T) {
	lru := evictor.NewLRU()

	lru.Track("a", 100)
	lru.Track("b", 200)
	lru.Track("c", 300)

	// Access "a" again — it moves to front, now "b" is oldest.
	lru.Track("a", 100)

	key, ok := lru.Evict()
	if !ok || key != "b" {
		t.Errorf("Evict() = %q, want %q (after accessing a)", key, "b")
	}
}

func TestLRU_RemoveThenEvict(t *testing.T) {
	lru := evictor.NewLRU()

	lru.Track("a", 100)
	lru.Track("b", 200)
	lru.Track("c", 300)

	// Remove "a" (the LRU candidate). Next eviction should return "b".
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
