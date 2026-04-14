package main

import (
	"sync"
	"time"
)

// DocCache is a simple TTL + size-capped cache for 'go doc' text output.
// On Put, if the cache is full, the oldest entry by insertion time is evicted.
// Expired entries are filtered on Get.
type DocCache struct {
	mu      sync.Mutex
	entries map[string]docCacheEntry
	max     int
	ttl     time.Duration
}

type docCacheEntry struct {
	value string
	added time.Time
}

func NewDocCache(max int, ttl time.Duration) *DocCache {
	return &DocCache{
		entries: make(map[string]docCacheEntry),
		max:     max,
		ttl:     ttl,
	}
}

// Get returns the cached value if present and not expired.
func (c *DocCache) Get(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		return "", false
	}
	if time.Since(e.added) > c.ttl {
		delete(c.entries, key)
		return "", false
	}
	return e.value, true
}

// Put inserts a value, evicting the oldest entry if the cache is full.
func (c *DocCache) Put(key, value string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= c.max {
		var oldestKey string
		var oldestTime time.Time
		for k, e := range c.entries {
			if oldestKey == "" || e.added.Before(oldestTime) {
				oldestKey = k
				oldestTime = e.added
			}
		}
		if oldestKey != "" {
			delete(c.entries, oldestKey)
		}
	}
	c.entries[key] = docCacheEntry{value: value, added: time.Now()}
}
