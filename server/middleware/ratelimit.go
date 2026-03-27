/*
 * Created by Mark Durlinger. MIT License.
 * 50% human, 50% AI, 100% chaos.
 */

package middleware

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// bucket is a simple token bucket for a single IP.
type bucket struct {
	tokens    float64
	lastCheck time.Time
}

// RateLimiter is a per-IP token bucket rate limiter.
type RateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*bucket
	rate      float64 // tokens per second
	burst     float64 // max tokens (bucket capacity)
	lastSweep time.Time
}

// NewRateLimiter creates a rate limiter that allows rate requests per second
// with the given burst capacity.
func NewRateLimiter(rate float64, burst int) *RateLimiter {
	return &RateLimiter{
		buckets:   make(map[string]*bucket),
		rate:      rate,
		burst:     float64(burst),
		lastSweep: time.Now(),
	}
}

// allow checks whether the given IP is allowed to proceed.
func (rl *RateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Sweep stale entries every minute.
	if now.Sub(rl.lastSweep) > time.Minute {
		for k, b := range rl.buckets {
			if now.Sub(b.lastCheck) > 2*time.Minute {
				delete(rl.buckets, k)
			}
		}
		rl.lastSweep = now
	}

	b, ok := rl.buckets[ip]
	if !ok {
		b = &bucket{tokens: rl.burst, lastCheck: now}
		rl.buckets[ip] = b
	}

	// Refill tokens based on elapsed time.
	elapsed := now.Sub(b.lastCheck).Seconds()
	b.tokens += elapsed * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastCheck = now

	if b.tokens < 1 {
		return false
	}

	b.tokens--
	return true
}

// Handler returns HTTP middleware that rate limits by client IP.
func (rl *RateLimiter) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := ClientIP(r)
		if !rl.allow(ip) {
			http.Error(w, `{"error":"rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ClientIP extracts the client IP from the request. Only trusts
// X-Forwarded-For / X-Real-IP when the direct connection comes from
// localhost (i.e. a local reverse proxy like Caddy).
func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	// Only trust proxy headers from loopback addresses.
	if host == "127.0.0.1" || host == "::1" || host == "localhost" {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// X-Forwarded-For can be comma-separated; take the first (client) IP.
			if idx := strings.Index(xff, ","); idx != -1 {
				return strings.TrimSpace(xff[:idx])
			}
			return strings.TrimSpace(xff)
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return strings.TrimSpace(xri)
		}
	}

	return host
}
