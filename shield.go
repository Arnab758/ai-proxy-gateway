package main

import (
	"net/http"
	"sync"
	"time"
)

type TokenBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
	mu         sync.Mutex
}

var tenantLimits = make(map[string]*TokenBucket)
var shieldMu sync.Mutex

func AllowRequest(startupID string, maxRequests float64, refillWindow time.Duration) bool {
	shieldMu.Lock()
	bucket, exists := tenantLimits[startupID]
	if !exists {
		bucket = &TokenBucket{
			tokens:     maxRequests,
			maxTokens:  maxRequests,
			refillRate: maxRequests / refillWindow.Seconds(),
			lastRefill: time.Now(),
		}
		tenantLimits[startupID] = bucket
	}
	shieldMu.Unlock()

	bucket.mu.Lock()
	defer bucket.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill).Seconds()
	bucket.lastRefill = now

	bucket.tokens += elapsed * bucket.refillRate
	if bucket.tokens > bucket.maxTokens {
		bucket.tokens = bucket.maxTokens
	}

	if bucket.tokens >= 1.0 {
		bucket.tokens -= 1.0
		return true
	}

	return false
}

func ShieldMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		startupID := r.Header.Get("X-Gateway-Token")
		if startupID == "" {
			startupID = "default"
		}

		if !AllowRequest(startupID, 60, time.Minute) {
			http.Error(w, `{"error": "Rate limit exceeded"}`, http.StatusTooManyRequests)
			return
		}

		next(w, r)
	}
}
