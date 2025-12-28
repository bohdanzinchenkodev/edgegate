package ratelimit

import (
	"sync"
	"time"
)

const (
	cleanupRate = 60  //each 60 seconds
	deleteAfter = 300 //delete entries unused for 300 seconds

)

type RateLimiter struct {
	entries     map[string]*tokenBucket
	capacity    int
	refillRate  int
	usageRate   int
	mux         sync.Mutex
	lastCleanup time.Time
}

func NewRateLimiter(capacity, refillRate, usageRate int) *RateLimiter {
	return &RateLimiter{
		entries:    make(map[string]*tokenBucket),
		capacity:   capacity,
		refillRate: refillRate,
		usageRate:  usageRate,
	}
}
func (rl *RateLimiter) AllowReq(key string) bool {
	rl.mux.Lock()
	defer rl.mux.Unlock()

	tb, ok := rl.entries[key]
	if !ok {
		tb = newTokenBucket(rl.capacity, rl.refillRate)
		rl.entries[key] = tb
	}

	res := tb.take(rl.usageRate)
	rl.cleanup()
	return res
}

func (rl *RateLimiter) cleanup() {
	now := time.Now()
	elapsed := now.Sub(rl.lastCleanup)
	if elapsed.Seconds() < cleanupRate {
		return
	}
	toDelete := make([]string, 0)
	for key, tb := range rl.entries {
		refillElapsed := now.Sub(tb.lastRefill)
		if refillElapsed.Seconds() < deleteAfter {
			continue
		}
		toDelete = append(toDelete, key)
	}
	for _, key := range toDelete {
		delete(rl.entries, key)
	}
	rl.lastCleanup = now
}

type tokenBucket struct {
	capacity   int
	tokens     int
	refillRate int // tokens per second
	lastRefill time.Time
	mux        sync.Mutex
}

func newTokenBucket(capacity, refillRate int) *tokenBucket {
	tb := &tokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
	return tb
}
func (tb *tokenBucket) take(tokens int) bool {
	tb.mux.Lock()
	defer tb.mux.Unlock()

	tb.refill()

	if tb.tokens < tokens {
		return false
	}

	tb.tokens -= tokens
	return true
}
func (tb *tokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill)

	tokensToAdd := int(elapsed.Seconds()) * tb.refillRate
	if tokensToAdd == 0 {
		return
	}

	tb.tokens = min(tokensToAdd+tb.tokens, tb.capacity)
	tb.lastRefill = now

}
