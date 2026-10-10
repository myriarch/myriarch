// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Package lru is a small generic least-recently-used cache.
//
// It covers the subset of github.com/hashicorp/golang-lru/v2 that this
// project uses (New, Add, Get, Peek, Remove, Contains, Purge, Len), built on
// container/list and a map. All methods are safe for concurrent use; a single
// mutex guards the cache, which is sufficient for the small, bounded caches
// of the server (sessions, rate limiters, rendered templates).
package lru

import (
	"container/list"
	"errors"
	"sync"
)

// Cache is a fixed-size LRU cache safe for concurrent use.
type Cache[K comparable, V any] struct {
	mu    sync.Mutex
	size  int
	order *list.List // front = most recently used
	items map[K]*list.Element
}

type entry[K comparable, V any] struct {
	key K
	val V
}

// New returns a cache that holds at most size entries. It returns an error
// if size is not positive.
func New[K comparable, V any](size int) (*Cache[K, V], error) {
	if size <= 0 {
		return nil, errors.New("lru: size must be positive")
	}
	return &Cache[K, V]{
		size:  size,
		order: list.New(),
		items: make(map[K]*list.Element, size),
	}, nil
}

// Add stores v under k and marks it as most recently used. If k is already
// present its value is replaced. It reports whether the least recently used
// entry was evicted to make room.
func (c *Cache[K, V]) Add(k K, v V) (evicted bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.items[k]; ok {
		el.Value.(*entry[K, V]).val = v
		c.order.MoveToFront(el)
		return false
	}
	c.items[k] = c.order.PushFront(&entry[K, V]{key: k, val: v})
	if c.order.Len() > c.size {
		oldest := c.order.Back()
		c.order.Remove(oldest)
		delete(c.items, oldest.Value.(*entry[K, V]).key)
		return true
	}
	return false
}

// Get returns the value stored under k and marks it as most recently used.
func (c *Cache[K, V]) Get(k K) (v V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, found := c.items[k]
	if !found {
		return v, false
	}
	c.order.MoveToFront(el)
	return el.Value.(*entry[K, V]).val, true
}

// Peek returns the value stored under k without updating its recency.
func (c *Cache[K, V]) Peek(k K) (v V, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, found := c.items[k]
	if !found {
		return v, false
	}
	return el.Value.(*entry[K, V]).val, true
}

// Contains reports whether k is cached, without updating its recency.
func (c *Cache[K, V]) Contains(k K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.items[k]
	return ok
}

// Remove deletes k and reports whether it was present.
func (c *Cache[K, V]) Remove(k K) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return false
	}
	c.order.Remove(el)
	delete(c.items, k)
	return true
}

// Purge removes all entries.
func (c *Cache[K, V]) Purge() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.order.Init()
	clear(c.items)
}

// Len returns the number of cached entries.
func (c *Cache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}
