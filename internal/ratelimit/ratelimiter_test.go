package ratelimit

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func init() {
	log.SetOutput(io.Discard)
}

type fakeClock struct {
	currentTime time.Time
}

func (fc *fakeClock) Now() time.Time {
	return fc.currentTime
}
func (fc *fakeClock) Add(d time.Duration) {
	fc.currentTime = fc.currentTime.Add(d)
}
func TestRateLimiter_AllowReqResult(t *testing.T) {
	ip := "12.345.678.90"
	o := RateLimiterOption{
		Capacity:   10,
		RefillRate: 0,
		UsageRate:  1,
	}
	rl := NewRateLimiter(o)
	for i := 0; i < 10; i++ {
		if ok := rl.AllowReq(ip); !ok {
			t.Fatalf("expected req %d to be allowed", i+1)
		}
	}
	if ok := rl.AllowReq(ip); ok {
		t.Fatalf("expected req 11 to be denied")
	}
}
func TestRateLimiter_Cleanup(t *testing.T) {
	ip := "12.345.678.90"
	fc := &fakeClock{currentTime: time.Now()}
	o := RateLimiterOption{
		Capacity:    10,
		DeleteAfter: 1 * time.Minute,
		Clock:       fc,
	}
	rl := NewRateLimiter(o)
	rl.AllowReq(ip)

	_, exists := rl.entries.Load(ip)
	if !exists {
		t.Fatalf("expected entry for IP to exist before cleanup")
	}

	rl.cleanupTick()
	_, exists = rl.entries.Load(ip)
	if !exists {
		t.Fatalf("expected entry for IP to still exist before time advancement")
	}

	// Advance time beyond DeleteAfter duration
	fc.Add(1 * time.Minute)

	rl.cleanupTick()
	_, exists = rl.entries.Load(ip)
	if exists {
		t.Fatalf("expected entry for IP to be deleted after cleanup")
	}
}

// Helper to build request with RemoteAddr + headers
func newReq(remoteAddr string, headers map[string]string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "http://example.com/", nil)
	r.RemoteAddr = remoteAddr
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	return r
}

func TestResolveIP_NoTrustedProxies_ReturnsRemoteAddrExpanded_IPv4(t *testing.T) {
	rl := &RateLimiter{trustedProxies: nil}

	r := newReq("203.0.113.5:12345", nil)

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Expanded form for IPv4 from netip.Addr.StringExpanded():
	// it's still dotted decimal.
	want := "203.0.113.5"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveIP_NoTrustedProxies_ReturnsRemoteAddrExpanded_IPv6(t *testing.T) {
	rl := &RateLimiter{trustedProxies: nil}

	r := newReq("[2001:db8::1]:443", nil)

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	// Expanded form should be 8 hextets, 4 hex digits each.
	want := "2001:0db8:0000:0000:0000:0000:0000:0001"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveIP_TrustedProxy_UsesXRealIP_WhenValid(t *testing.T) {
	rl := &RateLimiter{
		trustedProxies: []string{"203.0.113.0/24"},
	}

	r := newReq("203.0.113.10:5555", map[string]string{
		"X-Real-Ip": "198.51.100.42",
	})

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := "198.51.100.42"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveIP_TrustedProxy_XRealIPInvalid_FallsBackToXForwardedForFirstIP(t *testing.T) {
	rl := &RateLimiter{
		trustedProxies: []string{"203.0.113.0/24"},
	}

	r := newReq("203.0.113.10:5555", map[string]string{
		"X-Real-Ip":       "not-an-ip",
		"X-Forwarded-For": "198.51.100.42, 192.0.2.9",
	})

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := "198.51.100.42"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveIP_TrustedProxy_HeadersInvalid_ReturnsRemoteAddr(t *testing.T) {
	rl := &RateLimiter{
		trustedProxies: []string{"203.0.113.0/24"},
	}

	r := newReq("203.0.113.10:5555", map[string]string{
		"X-Real-Ip":       "still-not-an-ip",
		"X-Forwarded-For": "also-not-an-ip, 123",
	})

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := "203.0.113.10"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveIP_NotTrustedProxy_IgnoresForwardedHeaders_ReturnsRemoteAddr(t *testing.T) {
	rl := &RateLimiter{
		trustedProxies: []string{"203.0.113.0/24"},
	}

	// RemoteAddr outside trusted range:
	r := newReq("198.51.100.10:9999", map[string]string{
		"X-Real-Ip":       "203.0.113.5",
		"X-Forwarded-For": "203.0.113.5, 192.0.2.9",
	})

	got, err := rl.resolveIp(r)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	want := "198.51.100.10"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// populateEntries fills the rate limiter with n token buckets.
func populateEntries(rl *RateLimiter, n int) []string {
	keys := make([]string, n)
	now := rl.clock.Now()
	for i := 0; i < n; i++ {
		key := fmt.Sprintf("10.%d.%d.%d", i/65536, (i/256)%256, i%256)
		keys[i] = key

		tb := newTokenBucket(rl.capacity, rl.refillRate, rl.clock)
		tb.expireAt = now.Add(5 * time.Minute)
		rl.entries.Store(key, tb)
	}
	return keys
}

func BenchmarkAllowReq_Parallel_100K(b *testing.B) {
	rl := NewRateLimiter(RateLimiterOption{
		Capacity:        1000,
		RefillRate:      1000,
		UsageRate:       1,
		DeleteAfter:     5 * time.Minute,
		CleanupInterval: 10 * time.Second,
	})
	keys := populateEntries(rl, 100000)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			rl.AllowReq(keys[i%100000])
			i++
		}
	})
}

func BenchmarkAllowReq_Parallel_1M(b *testing.B) {
	rl := NewRateLimiter(RateLimiterOption{
		Capacity:        1000,
		RefillRate:      1000,
		UsageRate:       1,
		DeleteAfter:     5 * time.Minute,
		CleanupInterval: 10 * time.Second,
	})
	keys := populateEntries(rl, 1_000_000)

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			rl.AllowReq(keys[i%1_000_000])
			i++
		}
	})
}

func TestResolveIP_RemoteAddrWithoutPort_ReturnsError(t *testing.T) {
	rl := &RateLimiter{trustedProxies: nil}

	// This will make SplitHostPort fail, but your code ignores the error
	// so ip becomes "", ParseAddr("") errors.
	r := newReq("203.0.113.5", nil)

	_, err := rl.resolveIp(r)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
