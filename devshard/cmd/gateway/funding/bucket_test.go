package funding

import (
	"testing"
	"time"
)

const refillInterval = 15 * time.Second

// Test flow:
//  1. Start a bucket, take both tokens, and refill it at fifteen, thirty and six hundred seconds.
//  2. Assert it holds one, then two, then still two tokens: two at once, one per interval, never more than two.
func TestABucketHoldsTwoAndRefillsOnePerInterval(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	bucket := NewBucket(start)
	bucket.Take(2)

	for _, step := range []struct {
		at   time.Duration
		want int
	}{{at: 15 * time.Second, want: 1}, {at: 30 * time.Second, want: 2}, {at: 600 * time.Second, want: 2}} {
		bucket.Refill(start.Add(step.at), refillInterval)
		if got := bucket.Available(); got != step.want {
			t.Fatalf("Available() at %s = %d, want %d", step.at, got, step.want)
		}
	}
}

// Test flow:
//  1. Empty a bucket and refill it at five and fourteen seconds, as back-to-back wakeup ticks would, then at fifteen.
//  2. Assert it earns nothing before a whole interval and one token at fifteen seconds.
func TestBackToBackWakeupsEarnNoToken(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	bucket := NewBucket(start)
	bucket.Take(2)

	for _, step := range []struct {
		at   time.Duration
		want int
	}{{at: 5 * time.Second, want: 0}, {at: 14 * time.Second, want: 0}, {at: 15 * time.Second, want: 1}} {
		bucket.Refill(start.Add(step.at), refillInterval)
		if got := bucket.Available(); got != step.want {
			t.Fatalf("Available() at %s = %d, want %d", step.at, got, step.want)
		}
	}
}

// Test flow:
//  1. Leave a full bucket untouched for a minute, take both tokens, and refill at sixty-one and seventy-five seconds.
//  2. Assert the idle minute banked nothing: zero tokens at sixty-one seconds, one at seventy-five.
func TestAFullBucketBanksNoTime(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	bucket := NewBucket(start)
	bucket.Refill(start.Add(time.Minute), refillInterval)
	bucket.Take(2)

	bucket.Refill(start.Add(61*time.Second), refillInterval)
	if got := bucket.Available(); got != 0 {
		t.Fatalf("Available() at 61s = %d, want 0", got)
	}
	bucket.Refill(start.Add(75*time.Second), refillInterval)
	if got := bucket.Available(); got != 1 {
		t.Fatalf("Available() at 75s = %d, want 1", got)
	}
}

// Test flow:
//  1. Start a full bucket, refill it one second later while it is still full, and take both tokens.
//  2. Refill at fifteen seconds and at sixteen seconds.
//  3. Assert no token at fifteen seconds and one at sixteen: the full bucket re-anchored at its one-second refill, so the time it sat full earned nothing.
func TestAFullBucketReanchorsAtEveryRefill(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	bucket := NewBucket(start)
	bucket.Refill(start.Add(time.Second), refillInterval)
	bucket.Take(2)

	bucket.Refill(start.Add(15*time.Second), refillInterval)
	if got := bucket.Available(); got != 0 {
		t.Fatalf("Available() at 15s = %d, want 0", got)
	}
	bucket.Refill(start.Add(16*time.Second), refillInterval)
	if got := bucket.Available(); got != 1 {
		t.Fatalf("Available() at 16s = %d, want 1", got)
	}
}
