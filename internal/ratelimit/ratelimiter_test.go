package ratelimit

import (
	"testing"
	"time"
)

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
		WheelSize:  10,
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
		WheelSize:   1, // single slot for simplicity
		Clock:       fc,
	}
	rl := NewRateLimiter(o)
	rl.AllowReq(ip)

	_, exists := rl.entries.Load(ip)
	if !exists {
		t.Fatalf("expected entry for IP to exist before cleanup")
	}

	rl.cleanupTick()
	slot := rl.wheel.slots[0]
	if len(slot.keys) == 0 {
		t.Fatalf("expected slot to have keys before time advancement")
	}
	_, exists = rl.entries.Load(ip)
	if !exists {
		t.Fatalf("expected entry for IP to still exist before time advancement")
	}

	// Advance time beyond DeleteAfter duration
	fc.Add(1 * time.Minute)

	rl.cleanupTick()
	slot = rl.wheel.slots[0]
	if len(slot.keys) != 0 {
		t.Fatalf("expected slot to be empty after cleanup")
	}
	_, exists = rl.entries.Load(ip)
	if exists {
		t.Fatalf("expected entry for IP to be deleted after cleanup")
	}
}
