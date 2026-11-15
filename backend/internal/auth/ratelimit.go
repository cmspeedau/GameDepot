package auth

import (
	"sync"
	"time"
)

type RateLimiter struct {
	mu     sync.Mutex
	bucket map[string]*bucket
}

type bucket struct {
	count  int
	reset  time.Time
}

func NewRateLimiter() *RateLimiter {
	return &RateLimiter{bucket: make(map[string]*bucket)}
}

// Allow returns true if the key is under the limit for the window.
func (r *RateLimiter) Allow(key string, limit int, window time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	b, ok := r.bucket[key]
	if !ok || now.After(b.reset) {
		r.bucket[key] = &bucket{count: 1, reset: now.Add(window)}
		return true
	}
	if b.count >= limit {
		return false
	}
	b.count++
	return true
}
