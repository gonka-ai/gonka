package limits

import (
	"context"
	"sync"
	"testing"
	"time"
)

const (
	headCeilingWait     = 10 * time.Second
	headCeilingTokenCap = 100
	heldTokens          = 60
	headTokens          = 50
	smallTokens         = 10
	headCeilingModel    = "model-a"
)

type steppedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *steppedClock) advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(step)
}

// limiterWithAStarvingHead holds most of the model's token budget and queues a request too large for the rest.
func limiterWithAStarvingHead(t *testing.T, clock *steppedClock) (*GatewayLimiter, <-chan error) {
	t.Helper()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: headCeilingTokenCap, MaxInputTokens: headCeilingTokenCap, AcquireWait: headCeilingWait})
	limiter.now = clock.Now
	if err := limiter.AcquireForModel(context.Background(), headCeilingModel, heldTokens, fullScale()); err != nil {
		t.Fatalf("holding acquire: %v", err)
	}
	head := make(chan error, 1)
	go func() {
		head <- limiter.AcquireForModel(context.Background(), headCeilingModel, headTokens, fullScale())
	}()
	waitForQueueLen(t, limiter, 1)
	return limiter, head
}

func assertTheHeadGoesFirst(t *testing.T, limiter *GatewayLimiter, head <-chan error, queuedSmall <-chan error) {
	t.Helper()
	waitForQueueLen(t, limiter, 2)
	limiter.ReleaseForModel(headCeilingModel, heldTokens)
	if err := <-head; err != nil {
		t.Fatalf("the queue head = %v, want admission once capacity is freed", err)
	}
	if err := <-queuedSmall; err != nil {
		t.Fatalf("the small request queued behind the head = %v, want admission after it", err)
	}
}

// Test flow:
//  1. Queue a request whose input tokens do not fit beside the ones in flight, then pass it with small requests of the same model as many times as the ceiling allows.
//  2. Send one more small request.
//  3. Assert it queues behind the head instead of passing it, and the head is admitted first once capacity frees: passing gives throughput, but never starves the head for good.
func TestTheQueueHeadIsPassedOnlyUpToTheCeiling(t *testing.T) {
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0)}
	limiter, head := limiterWithAStarvingHead(t, clock)
	for range maxQueueHeadPasses {
		if err := limiter.AcquireForModel(context.Background(), headCeilingModel, smallTokens, fullScale()); err != nil {
			t.Fatalf("a small request inside the ceiling = %v, want it to pass the head", err)
		}
		limiter.ReleaseForModel(headCeilingModel, smallTokens)
	}

	queuedSmall := make(chan error, 1)
	go func() {
		queuedSmall <- limiter.AcquireForModel(context.Background(), headCeilingModel, smallTokens, fullScale())
	}()

	assertTheHeadGoesFirst(t, limiter, head, queuedSmall)
}

// Test flow:
//  1. Queue a request whose input tokens do not fit beside the ones in flight, and let it wait past the share of its admission wait the ceiling allows.
//  2. Send a small request of the same model that would fit.
//  3. Assert it queues behind the head instead of passing it, and the head is admitted first once capacity frees.
func TestAQueueHeadThatWaitedPastTheCeilingIsNotPassed(t *testing.T) {
	clock := &steppedClock{now: time.Unix(1_700_000_000, 0)}
	limiter, head := limiterWithAStarvingHead(t, clock)
	clock.advance(headCeilingWait/queueHeadPatienceDivisor + time.Millisecond)

	queuedSmall := make(chan error, 1)
	go func() {
		queuedSmall <- limiter.AcquireForModel(context.Background(), headCeilingModel, smallTokens, fullScale())
	}()

	assertTheHeadGoesFirst(t, limiter, head, queuedSmall)
}
