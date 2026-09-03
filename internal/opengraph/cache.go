package opengraph

import (
	"context"
	"sync"
	"time"
)

// Cache is a simple in-memory, TTL-based cache of Data keyed by URL. It is
// intentionally NOT shared across instances (no Redis/Postgres backing) —
// og-service is meant to be cheap to run several of behind a load balancer
// (see deploy/docker-compose.yml), and a cache miss just costs one more
// outbound fetch of the same page, which is exactly the "best-effort"
// standard url_enrichment has held itself to everywhere else in this
// codebase. A shared cache would trade that simplicity for a new
// dependency this narrow a service doesn't otherwise need.
type Cache struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	data      Data
	expiresAt time.Time
}

func NewCache(ttl time.Duration) *Cache {
	return &Cache{ttl: ttl, entries: make(map[string]cacheEntry)}
}

// Get returns the cached Data for url, and whether it was found and still
// fresh. An expired entry is treated as a miss (and lazily dropped) rather
// than returned stale.
func (c *Cache) Get(url string) (Data, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[url]
	if !ok {
		return Data{}, false
	}
	if time.Now().After(entry.expiresAt) {
		delete(c.entries, url)
		return Data{}, false
	}
	return entry.data, true
}

// Set stores data for url, valid for the Cache's configured TTL from now —
// including a "fetched fine, nothing there" empty result, so a page that
// legitimately has no OpenGraph tags doesn't get re-fetched on every
// request within the hour (see Fetch's doc comment).
func (c *Cache) Set(url string, data Data) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[url] = cacheEntry{data: data, expiresAt: time.Now().Add(c.ttl)}
}

// Len reports the current entry count, expired or not — used only for the
// /healthz and debug surface, not for any cache-eviction decision.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Run periodically sweeps out expired entries so a steady trickle of
// distinct, rarely-repeated URLs doesn't grow the map forever between the
// (much rarer) Gets that would otherwise lazily evict them. Blocks until
// ctx is done — call it as `go cache.Run(ctx, interval)`.
func (c *Cache) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sweep()
		}
	}
}

func (c *Cache) sweep() {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for url, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, url)
		}
	}
}
