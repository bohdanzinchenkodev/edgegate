package ratelimit

import (
	"context"
	"fmt"
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
	cleanupInterval time.Duration
	deleteAfter     time.Duration
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
	Clock           clock
	TrustedProxies  []string
}

func NewRateLimiter(rlOptions RateLimiterOption) *RateLimiter {
	if rlOptions.Clock == nil {
		rlOptions.Clock = &realClock{}
	}
	log.Printf("RateLimiter created with capacity: %d, refillRate: %.2f, usageRate: %d, deleteAfter: %s, cleanupInterval: %s",
		rlOptions.Capacity,
		rlOptions.RefillRate,
		rlOptions.UsageRate,
		rlOptions.DeleteAfter.String(),
		rlOptions.CleanupInterval.String(),
	)
	return &RateLimiter{
		entries:         sync.Map{},
		capacity:        rlOptions.Capacity,
		refillRate:      rlOptions.RefillRate,
		usageRate:       rlOptions.UsageRate,
		cleanupInterval: rlOptions.CleanupInterval,
		deleteAfter:     rlOptions.DeleteAfter,
		clock:           rlOptions.Clock,
		trustedProxies:  rlOptions.TrustedProxies,
	}
}
func (rl *RateLimiter) String() string {
	return fmt.Sprintf("{capacity:%d refillRate:%.2f usageRate:%d deleteAfter:%s cleanupInterval:%s trustedProxies:%v}",
		rl.capacity, rl.refillRate, rl.usageRate, rl.deleteAfter, rl.cleanupInterval, rl.trustedProxies)
}

func (rl *RateLimiter) Equal(other any) bool {
	o, ok := other.(*RateLimiter)
	if !ok {
		return false
	}
	if rl == nil || o == nil {
		return rl == o
	}
	return rl.capacity == o.capacity &&
		rl.refillRate == o.refillRate &&
		rl.usageRate == o.usageRate &&
		rl.deleteAfter == o.deleteAfter &&
		rl.cleanupInterval == o.cleanupInterval &&
		slicesEqual(rl.trustedProxies, o.trustedProxies)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
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
	// set expiry date for bucket
	v.mux.Lock()
	v.expireAt = rl.clock.Now().Add(rl.deleteAfter)
	v.mux.Unlock()

	return v.take(rl.usageRate)
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
	rl.entries.Range(func(key, value any) bool {
		tb := value.(*tokenBucket)
		tb.mux.Lock()
		diff := now.Sub(tb.expireAt)
		tb.mux.Unlock()
		if diff.Seconds() >= 0 {
			rl.entries.Delete(key)
		}
		return true
	})
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
