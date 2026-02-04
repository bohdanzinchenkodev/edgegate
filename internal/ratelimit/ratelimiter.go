package ratelimit

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

type clock interface {
	Now() time.Time
}
type realClock struct {
	now time.Time
}

func (rc *realClock) Now() time.Time {
	return time.Now()
}

type RateLimiter struct {
	entries         sync.Map
	capacity        int
	refillRate      float64
	usageRate       int
	mux             sync.Mutex
	wheel           wheel
	cleanupInterval time.Duration
	deleteAfter     time.Duration
	wheelSize       int
	clock           clock
	trustedProxies  []string
	cancelCleanup   context.CancelFunc
}
type RateLimiterOption struct {
	Capacity        int
	RefillRate      float64
	UsageRate       int
	CleanupInterval time.Duration
	DeleteAfter     time.Duration
	WheelSize       int
	Clock           clock
	TrustedProxies  []string
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
	if rlOptions.Clock == nil {
		rlOptions.Clock = &realClock{}
	}
	log.Printf("RateLimiter created with capacity: %d, refillRate: %.2f, usageRate: %d, wheelSize: %d, deleteAfter: %s, cleanupInterval: %s\n",
		rlOptions.Capacity,
		rlOptions.RefillRate,
		rlOptions.UsageRate,
		rlOptions.WheelSize,
		rlOptions.DeleteAfter.String(),
		rlOptions.CleanupInterval.String(),
	)
	return &RateLimiter{
		entries:         sync.Map{},
		capacity:        rlOptions.Capacity,
		refillRate:      rlOptions.RefillRate,
		usageRate:       rlOptions.UsageRate,
		wheel:           wheel{slots: slots},
		cleanupInterval: rlOptions.CleanupInterval,
		wheelSize:       rlOptions.WheelSize,
		deleteAfter:     rlOptions.DeleteAfter,
		clock:           rlOptions.Clock,
		trustedProxies:  rlOptions.TrustedProxies,
	}
}
func (rl *RateLimiter) AllowReq(key string) bool {
	tb, ok := rl.entries.Load(key)
	if !ok {
		tb = newTokenBucket(rl.capacity, rl.refillRate, rl.clock)
		tb, _ = rl.entries.LoadOrStore(key, tb)
	}
	v, ok := tb.(*tokenBucket)
	if !ok {
		return false
	}
	/*log.Printf("Bucket info - capacity: %v, tokens: %v", v.capacity, v.tokens)*/

	//set expiry date for bucket
	v.mux.Lock()
	v.expireAt = rl.clock.Now().Add(rl.deleteAfter)
	v.mux.Unlock()

	// Add to wheel slot
	rl.addToWheel(key)
	res := v.take(rl.usageRate)
	return res
}

func (rl *RateLimiter) addToWheel(key string) {
	currentTick := rl.clock.Now().Unix()
	da := int64(rl.deleteAfter.Seconds())
	//ex: (531 + 300) % 300 = 231
	slotIndex := (currentTick + da) % int64(rl.wheelSize)
	s := rl.wheel.slots[slotIndex]

	s.mux.Lock()
	s.keys = append(s.keys, key)
	s.mux.Unlock()

	log.Printf("%s was added to slot: %d", key, slotIndex)
}
func (rl *RateLimiter) ServerStart(ctx context.Context) {
	cleanupCtx, cancel := context.WithCancel(ctx)
	rl.cancelCleanup = cancel

	go rl.cleanup(cleanupCtx)
}

func (rl *RateLimiter) ServerShutdown() {
	if rl.cancelCleanup != nil {
		rl.cancelCleanup()
	}
}
func (rl *RateLimiter) WrapHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, err := rl.resolveIp(r)
		if err != nil {
			http.Error(w, "cannot resolve IP", http.StatusInternalServerError)
			return
		}
		log.Printf("Resolved key: %s", key)
		if !rl.AllowReq(key) {
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (rl *RateLimiter) cleanup(ctx context.Context) {
	log.Println("Start Cleanup")
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
	now := rl.clock.Now()
	slotIndex := (now.Unix() + 1) % int64(rl.wheelSize)

	s := rl.wheel.slots[slotIndex]
	s.mux.Lock()
	survivedKeys := make([]string, 0)
	for _, key := range s.keys {
		tb, ok := rl.entries.Load(key)
		if !ok {
			continue
		}
		v, ok := tb.(*tokenBucket)
		if !ok {
			rl.entries.Delete(key)
			continue
		}
		v.mux.Lock()
		diff := now.Sub(v.expireAt)
		v.mux.Unlock()
		if diff.Seconds() >= 0 {
			rl.entries.Delete(key)
			continue
		}
		survivedKeys = append(survivedKeys, key)
	}
	s.keys = survivedKeys
	s.mux.Unlock()
}
func (rl *RateLimiter) resolveIp(r *http.Request) (string, error) {

	//get ip without port
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	//validate ip and parse to netip.Addr
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		log.Printf("Invalid IP address %s: %v", ip, err)
		return "", err
	}

	if len(rl.trustedProxies) == 0 {
		return addr.StringExpanded(), nil
	}

	if isIPInTrustedProxyRanges(addr, rl.trustedProxies) {
		// Check X-Real-Ip and X-Forwarded-For headers
		xRealIp := r.Header.Get("X-Real-Ip")
		//validate xRealIp
		xRealAddr, err := netip.ParseAddr(xRealIp)
		if err == nil {
			return xRealAddr.StringExpanded(), nil
		}

		xForwardedFor := r.Header.Get("X-Forwarded-For")
		ips := strings.Split(xForwardedFor, ",")
		if len(ips) > 0 {
			//get first ip
			xForwardedIp := strings.TrimSpace(ips[0])
			//validate xForwardedIp
			xForwardedAddr, err := netip.ParseAddr(xForwardedIp)
			if err == nil {
				return xForwardedAddr.StringExpanded(), nil
			}
		}
	}
	//return remote addr if no valid headers found
	return addr.StringExpanded(), nil
}

func isIPInTrustedProxyRanges(addr netip.Addr, trustedProxies []string) bool {
	for _, proxyRange := range trustedProxies {
		prefix, err := netip.ParsePrefix(proxyRange)
		if err != nil {
			log.Printf("Invalid proxy range %s: %v", proxyRange, err)
			continue
		}
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

type tokenBucket struct {
	capacity   int
	tokens     float64 // current tokens (float to accumulate fractions)
	refillRate float64 // tokens per second
	lastRefill time.Time
	mux        sync.Mutex
	expireAt   time.Time
	clock      clock
}

func newTokenBucket(capacity int, refillRate float64, c clock) *tokenBucket {
	tb := &tokenBucket{
		capacity:   capacity,
		tokens:     float64(capacity),
		refillRate: refillRate,
		lastRefill: time.Now(),
		clock:      c,
	}
	return tb
}
func (tb *tokenBucket) take(tokens int) bool {
	tb.mux.Lock()
	defer tb.mux.Unlock()
	tb.refill()

	if tb.tokens < float64(tokens) {
		return false
	}

	tb.tokens -= float64(tokens)
	return true
}
func (tb *tokenBucket) refill() {
	now := tb.clock.Now()
	elapsed := now.Sub(tb.lastRefill)
	tb.lastRefill = now

	tb.tokens = min(tb.tokens+elapsed.Seconds()*tb.refillRate, float64(tb.capacity))
}
