package ratelimit

import (
	"context"
	"testing"
	"time"
)

func TestRateLimiter_AllowReqResult(t *testing.T) {
	ip := "12.345.678.90"
	o := RateLimiterOption{
		Capacity:   10,
		RefillRate: 0,
		UsageRate:  1,
		WheelSize:  10,
	}
	rl := NewRateLimiter(o)
	for i := 0; i < 11; i++ {
		res := rl.AllowReq(ip)
		if i < 10 && !res {
			t.Errorf("expected request %d to be allowed", i+1)
		}
		if i >= 10 && res {
			t.Errorf("expected request %d to be denied", i+1)
		}
	}
}
func TestRateLimiter_RecordsDeletedAfterCleanup(t *testing.T) {
	ip := "12.345.678.90"
	o := RateLimiterOption{
		Capacity:        10,
		RefillRate:      0,
		UsageRate:       1,
		CleanupInterval: 0, // immediate cleanup
		DeleteAfter:     0, // immediate deletion
		WheelSize:       1, // single slot for simplicity
	}
	rl := NewRateLimiter(o)
	for i := 0; i < 10; i++ {
		_ = rl.AllowReq(ip)
	}

	_, exists := rl.entries.Load(ip)
	if !exists {
		t.Errorf("expected entry for IP to exist before cleanup")
	}

	ctx := context.Background()
	rl.Cleanup(ctx, true)
	slot := rl.wheel.slots[0]
	if len(slot.keys) != 0 {
		t.Errorf("expected slot to be empty after cleanup")
	}
	_, exists = rl.entries.Load(ip)
	if exists {
		t.Errorf("expected entry for IP to be deleted after cleanup")
	}
}
func TestRateLimiter_RecordsStayAfterCleanup(t *testing.T) {
	ip := "12.345.678.90"
	o := RateLimiterOption{
		Capacity:        10,
		RefillRate:      0,
		UsageRate:       1,
		CleanupInterval: 0,                 // immediate cleanup
		DeleteAfter:     100 * time.Minute, // far future deletion
		WheelSize:       1,                 // single slot for simplicity
	}
	rl := NewRateLimiter(o)
	for i := 0; i < 10; i++ {
		_ = rl.AllowReq(ip)
	}
	_, exists := rl.entries.Load(ip)
	if !exists {
		t.Errorf("expected entry for IP to exist before cleanup")
	}

	ctx := context.Background()
	rl.Cleanup(ctx, true)
	slot := rl.wheel.slots[0]
	if len(slot.keys) != 0 {
		t.Errorf("expected slot to be empty after cleanup")
	}
	_, exists = rl.entries.Load(ip)
	if !exists {
		t.Errorf("expected entry for IP to still exist after cleanup")
	}
}
