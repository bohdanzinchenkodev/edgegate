package ratelimit

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	deleteAfter = 300 * time.Second //delete entries unused for 300 seconds
	wheelSize   = 300               //number of slots in the wheel
)
const cleanupInterval = 1 * time.Second

type RateLimiter struct {
	entries    sync.Map
	capacity   int
	refillRate int
	usageRate  int
	mux        sync.Mutex
	/*wheel      wheel*/
}

/*type wheel struct {
	slots []*slot
	cur   int
}
type slot struct {
	keys []string
	mux  sync.Mutex
}*/

func NewRateLimiter(capacity, refillRate, usageRate int) *RateLimiter {
	/*slots := make([]*slot, wheelSize)
	for i := range slots {
		slots[i] = &slot{}
	}*/
	return &RateLimiter{
		entries:    sync.Map{},
		capacity:   capacity,
		refillRate: refillRate,
		usageRate:  usageRate,
		/*wheel:      wheel{slots: slots},*/
	}
}
func (rl *RateLimiter) AllowReqMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.RemoteAddr //keep it simple for now
		if !rl.AllowReq(key) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (rl *RateLimiter) AllowReq(key string) bool {
	tb, ok := rl.entries.Load(key)
	if !ok {
		tb = newTokenBucket(rl.capacity, rl.refillRate)
		tb, _ = rl.entries.LoadOrStore(key, tb)
	}
	v, ok := tb.(*tokenBucket)
	if !ok {
		return false
	}
	log.Printf("Bucket info - capacity: %v, tokens: %v", v.capacity, v.tokens)
	v.expireAt = time.Now().Add(deleteAfter)
	// Add to wheel slot
	/*rl.addToWheel(key)*/
	res := v.take(rl.usageRate)
	return res
}

/*
	func (rl *RateLimiter) addToWheel(key string) {
		currentTick := rl.wheel.cur
		delaySlots := int(deleteAfter / cleanupInterval)
		slotIndex := (cur + delaySlots) % wheelSize
		s := rl.wheel.slots[slotIndex]
		s.mux.Lock()
		s.keys = append(s.keys, key)
		s.mux.Unlock()
	}
*/
func (rl *RateLimiter) Cleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("Exit Cleanup")
			return
		case <-ticker.C:
			/*now := time.Now()
			currentTick := rl.wheel.cur
			slotIndex := currentTick % wheelSize
			s := rl.wheel.slots[slotIndex]
			s.mux.Lock()

			for _, key := range s.keys {
				tb, ok := rl.entries.Load(key)
				v, ok := tb.(*tokenBucket)
				if !ok {
					rl.entries.Delete(key)
					continue
				}

				diff := now.Sub(v.expireAt)
				if diff.Seconds() > 0 {
					rl.entries.Delete(key)
				}
			}
			rl.wheel.slots = make([]*slot, 0)
			rl.wheel.cur++
			s.mux.Unlock()*/
		}
	}
}

type tokenBucket struct {
	capacity   int
	tokens     int
	refillRate int // tokens per second
	lastRefill time.Time
	mux        sync.Mutex
	expireAt   time.Time
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
	/*tb.refill()*/

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
	tb.lastRefill = now
	if tokensToAdd == 0 {
		return
	}
	tb.tokens = min(tokensToAdd+tb.tokens, tb.capacity)
}
