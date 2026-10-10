// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package lru_test

import (
	"strconv"
	"sync"
	"testing"

	"github.com/myriarch/myriarch/internal/lru"
)

func mustNew[K comparable, V any](t *testing.T, size int) *lru.Cache[K, V] {
	t.Helper()
	c, err := lru.New[K, V](size)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestNewRejectsNonPositiveSize(t *testing.T) {
	for _, size := range []int{0, -1} {
		c, err := lru.New[string, int](size)
		if err == nil || c != nil {
			t.Fatalf("New(%d) = %v, %v; want nil cache and an error", size, c, err)
		}
	}
}

func TestEvictionOrder(t *testing.T) {
	c := mustNew[string, int](t, 3)
	for i, k := range []string{"a", "b", "c"} {
		if c.Add(k, i) {
			t.Fatalf("Add(%q) reported an eviction below capacity", k)
		}
	}
	if !c.Add("d", 3) {
		t.Fatal("Add over capacity must report an eviction")
	}
	if c.Contains("a") {
		t.Fatal("oldest entry a should have been evicted")
	}
	for _, k := range []string{"b", "c", "d"} {
		if !c.Contains(k) {
			t.Fatalf("%q should still be cached", k)
		}
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
}

func TestGetUpdatesRecencyPeekDoesNot(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)

	// Peek must not protect "a" from eviction.
	if v, ok := c.Peek("a"); !ok || v != 1 {
		t.Fatalf("Peek(a) = %d, %v", v, ok)
	}
	c.Add("c", 3)
	if c.Contains("a") || !c.Contains("b") {
		t.Fatal("after Peek(a), a must be evicted first")
	}

	// Get must protect "b": now c is the least recently used.
	if v, ok := c.Get("b"); !ok || v != 2 {
		t.Fatalf("Get(b) = %d, %v", v, ok)
	}
	c.Add("d", 4)
	if c.Contains("c") || !c.Contains("b") {
		t.Fatal("after Get(b), c must be evicted first")
	}

	if _, ok := c.Get("missing"); ok {
		t.Fatal("Get of a missing key must report false")
	}
	if _, ok := c.Peek("missing"); ok {
		t.Fatal("Peek of a missing key must report false")
	}
}

func TestContainsDoesNotUpdateRecency(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)
	_ = c.Contains("a")
	c.Add("c", 3)
	if c.Contains("a") {
		t.Fatal("Contains must not refresh recency")
	}
}

func TestAddExistingKeyUpdatesValueAndRecency(t *testing.T) {
	c := mustNew[string, int](t, 2)
	c.Add("a", 1)
	c.Add("b", 2)
	if c.Add("a", 10) {
		t.Fatal("updating an existing key must not report an eviction")
	}
	if c.Len() != 2 {
		t.Fatalf("Len = %d, want 2", c.Len())
	}
	if v, _ := c.Peek("a"); v != 10 {
		t.Fatalf("a = %d, want 10", v)
	}
	c.Add("c", 3) // evicts b, because the update refreshed a
	if c.Contains("b") || !c.Contains("a") {
		t.Fatal("the update of a must have made b the eviction candidate")
	}
}

func TestRemovePurgeLen(t *testing.T) {
	c := mustNew[int, string](t, 4)
	for i := 0; i < 4; i++ {
		c.Add(i, strconv.Itoa(i))
	}
	if !c.Remove(2) {
		t.Fatal("Remove of a present key must report true")
	}
	if c.Remove(2) {
		t.Fatal("Remove of an absent key must report false")
	}
	if c.Len() != 3 {
		t.Fatalf("Len = %d, want 3", c.Len())
	}
	// the freed slot is reusable without eviction
	if c.Add(9, "9") {
		t.Fatal("Add into a freed slot must not evict")
	}
	c.Purge()
	if c.Len() != 0 || c.Contains(0) {
		t.Fatal("Purge must empty the cache")
	}
	if c.Add(1, "x") || c.Len() != 1 {
		t.Fatal("cache must be usable after Purge")
	}
}

func TestPointerValues(t *testing.T) {
	type payload struct{ n int }
	c := mustNew[string, *payload](t, 1)
	p := &payload{n: 7}
	c.Add("k", p)
	got, ok := c.Get("k")
	if !ok || got != p {
		t.Fatal("pointer values must round-trip unchanged")
	}
}

func TestConcurrentUse(t *testing.T) {
	const (
		workers = 16
		ops     = 2000
		size    = 64
	)
	c := mustNew[int, int](t, size)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < ops; i++ {
				k := (w*31 + i) % 200
				switch i % 6 {
				case 0, 1:
					c.Add(k, i)
				case 2:
					c.Get(k)
				case 3:
					c.Peek(k)
				case 4:
					c.Contains(k)
				case 5:
					if i%60 == 5 {
						c.Remove(k)
					}
					if i%600 == 5 {
						c.Purge()
					}
				}
				if n := c.Len(); n > size {
					t.Errorf("Len = %d exceeds size %d", n, size)
					return
				}
			}
		}(w)
	}
	wg.Wait()
}
