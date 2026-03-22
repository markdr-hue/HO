/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package public

import (
	"fmt"
	"net/http"
	"sync"
	"time"
)

// apiCache is a simple in-memory cache for API GET responses.
// Keyed by "siteID:path:queryString", entries expire after a configurable TTL.
type apiCache struct {
	mu      sync.RWMutex
	entries map[string]*cacheEntry
}

type cacheEntry struct {
	body      []byte
	status    int
	expiresAt time.Time
}

func newAPICache() *apiCache {
	c := &apiCache{entries: make(map[string]*cacheEntry)}
	// Background cleanup every 60 seconds.
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			c.evict()
		}
	}()
	return c
}

// get returns a cached response if it exists and hasn't expired.
func (c *apiCache) get(key string) ([]byte, int, bool) {
	c.mu.RLock()
	entry, ok := c.entries[key]
	c.mu.RUnlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return nil, 0, false
	}
	return entry.body, entry.status, true
}

// set stores a response in the cache with the given TTL.
func (c *apiCache) set(key string, body []byte, status int, ttl time.Duration) {
	c.mu.Lock()
	c.entries[key] = &cacheEntry{
		body:      body,
		status:    status,
		expiresAt: time.Now().Add(ttl),
	}
	c.mu.Unlock()
}

// invalidateTable removes all cache entries for a specific site+table combination.
// Called on POST/PUT/DELETE to ensure stale data isn't served.
func (c *apiCache) invalidateTable(siteID int, tableName string) {
	prefix := formatInvalidationPrefix(siteID, tableName)
	c.mu.Lock()
	for key := range c.entries {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// evict removes all expired entries.
func (c *apiCache) evict() {
	now := time.Now()
	c.mu.Lock()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// formatCacheKey creates a cache key from site ID, table, path, and query string.
func formatCacheKey(siteID int, tableName, path, query string) string {
	return formatInvalidationPrefix(siteID, tableName) + path + "?" + query
}

// formatInvalidationPrefix creates the prefix used for table-level invalidation.
func formatInvalidationPrefix(siteID int, tableName string) string {
	return fmt.Sprintf("%d:%s:", siteID, tableName)
}

// responseRecorder captures HTTP responses for caching while writing through to the client.
type responseRecorder struct {
	http.ResponseWriter
	statusCode int
	body       []byte
}

func (r *responseRecorder) WriteHeader(code int) {
	r.statusCode = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return r.ResponseWriter.Write(b)
}
