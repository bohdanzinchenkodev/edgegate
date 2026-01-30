package ratelimit

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

type RateLimiter struct {
	entries         sync.Map
	capacity        int
	refillRate      int
	usageRate       int
	mux             sync.Mutex
	wheel           wheel
	cleanupInterval time.Duration
	deleteAfter     time.Duration
	wheelSize       int
}
type RateLimiterOption struct {
	Capacity        int
	RefillRate      int
	UsageRate       int
	CleanupInterval time.Duration
	DeleteAfter     time.Duration
	WheelSize       int
}

type wheel struct {
	slots []*slot
	cur   int
}
type slot struct {
	keys []string
	mux  sync.Mutex
}

func NewRateLimiter(rlOptions RateLimiterOption) *RateLimiter {
	slots := make([]*slot, rlOptions.WheelSize)
	for i := range slots {
		slots[i] = &slot{}
	}
	return &RateLimiter{
		entries:         sync.Map{},
		capacity:        rlOptions.Capacity,
		refillRate:      rlOptions.RefillRate,
		usageRate:       rlOptions.UsageRate,
		wheel:           wheel{slots: slots},
		cleanupInterval: rlOptions.CleanupInterval,
		wheelSize:       rlOptions.WheelSize,
		deleteAfter:     rlOptions.DeleteAfter,
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
	/*log.Printf("Bucket info - capacity: %v, tokens: %v", v.capacity, v.tokens)*/

	//set expiry date for bucket
	v.mux.Lock()
	v.expireAt = time.Now().Add(rl.deleteAfter)
	v.mux.Unlock()

	// Add to wheel slot
	rl.addToWheel(key)
	res := v.take(rl.usageRate)
	return res
}

func (rl *RateLimiter) addToWheel(key string) {
	currentTick := time.Now().Unix()
	da := int64(rl.deleteAfter.Seconds())
	//ex: (531 + 300) % 300 = 231
	slotIndex := (currentTick + da) % int64(rl.wheelSize)
	s := rl.wheel.slots[slotIndex]

	s.mux.Lock()
	s.keys = append(s.keys, key)
	s.mux.Unlock()

	log.Printf("%s was added to slot: %d", key, slotIndex)
}

func (rl *RateLimiter) Cleanup(ctx context.Context, cleanupOnStart bool) {
	if cleanupOnStart {
		log.Println("Performing initial cleanup")
		rl.cleanupTick()
	}
	if rl.cleanupInterval == 0 {
		log.Println("Cleanup interval is 0, skipping cleanup routine")
		return
	}
	ticker := time.NewTicker(rl.cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("Exit Cleanup")
			return
		case <-ticker.C:
			rl.cleanupTick()
		}
	}
}
func (rl *RateLimiter) cleanupTick() {
	now := time.Now()
	slotIndex := (time.Now().Unix() + 1) % int64(rl.wheelSize)
	log.Printf("Cleanup tick: %d", slotIndex)

	s := rl.wheel.slots[slotIndex]
	s.mux.Lock()
	for _, key := range s.keys {
		tb, ok := rl.entries.Load(key)
		v, ok := tb.(*tokenBucket)
		if !ok {
			rl.entries.Delete(key)
			continue
		}
		v.mux.Lock()
		diff := now.Sub(v.expireAt)
		v.mux.Unlock()
		if diff.Seconds() > 0 {
			rl.entries.Delete(key)
		}
	}
	s.keys = make([]string, 0)
	s.mux.Unlock()
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
	tb.lastRefill = now
	if tokensToAdd == 0 {
		return
	}
	tb.tokens = min(tokensToAdd+tb.tokens, tb.capacity)
}
