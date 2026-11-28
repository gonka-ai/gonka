package limits

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"
)

const (
	queuedBytesModel  = "model-q"
	queuedBodyBytes   = 1_600_000
	queuedBodyLimit   = 3
	queuedBytesWait   = 10 * time.Second
	queuedBytesShort  = 2 * time.Second
	queuedBytesBound  = 5 * time.Second
	queuedLargestBody = 10 << 20
)

func limiterHoldingItsOnlySlot(t *testing.T, cfg GatewayConfig) *GatewayLimiter {
	t.Helper()
	cfg.MaxConcurrent = 1
	if cfg.AcquireWait == 0 {
		cfg.AcquireWait = queuedBytesWait
	}
	limiter := NewGatewayLimiter(cfg)
	if err := limiter.AcquireForModel(context.Background(), queuedBytesModel, 1, 0, fullScale()); err != nil {
		t.Fatalf("the acquire that holds the only slot = %v, want admitted", err)
	}
	return limiter
}

func queueBody(ctx context.Context, limiter *GatewayLimiter, bytes int64) <-chan error {
	verdict := make(chan error, 1)
	go func() { verdict <- limiter.AcquireForModel(ctx, queuedBytesModel, 1, bytes, fullScale()) }()
	return verdict
}

func heldQueuedBytes(limiter *GatewayLimiter) int64 {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	return limiter.queuedBytes
}

func waitForQueuedBytes(t *testing.T, limiter *GatewayLimiter, want int64) {
	t.Helper()
	deadline := time.Now().Add(queuedBytesBound)
	for {
		got := heldQueuedBytes(limiter)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued bytes = %d, want %d within %s", got, want, queuedBytesBound)
		}
		runtime.Gosched()
	}
}

func verdictOf(t *testing.T, verdict <-chan error, what string) error {
	t.Helper()
	select {
	case err := <-verdict:
		return err
	case <-time.After(queuedBytesBound):
		t.Fatalf("%s never ended within %s", what, queuedBytesBound)
		return nil
	}
}

func fillQueueToItsByteBudget(t *testing.T, limiter *GatewayLimiter, ctx context.Context) []<-chan error {
	t.Helper()
	waiting := make([]<-chan error, 0, queuedBodyLimit)
	for i := range queuedBodyLimit {
		waiting = append(waiting, queueBody(ctx, limiter, queuedBodyBytes))
		waitForQueueLen(t, limiter, i+1)
	}
	waitForQueuedBytes(t, limiter, queuedBodyLimit*queuedBodyBytes)
	return waiting
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a byte budget of three bodies, and hold its only slot.
//  2. Queue three bodies of 1.6 MB and wait for all three to be waiting with their bytes held.
//  3. Acquire a fourth body of the same size; assert it returns `ErrQueuedBytesFull` at once, with its own text, and is not queued.
//  4. Assert the queue still holds three waiters and exactly three bodies of bytes.
//  5. Cancel the waiters and assert each ends with the context error and the held bytes return to zero.
func TestQueuedBodiesUpToTheByteBudgetAllWaitAndOneMoreIsRefused(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := fillQueueToItsByteBudget(t, limiter, ctx)

	err := verdictOf(t, queueBody(context.Background(), limiter, queuedBodyBytes), "the body past the byte budget")
	if !errors.Is(err, ErrQueuedBytesFull) {
		t.Fatalf("the body past the byte budget = %v, want ErrQueuedBytesFull", err)
	}
	var rateLimited *RateLimitError
	if errors.As(err, &rateLimited) {
		t.Fatalf("the byte budget refusal is a *RateLimitError (%v), want its own error", err)
	}
	if want := "the gateway is holding as many queued request bodies as it can"; err.Error() != want {
		t.Fatalf("refusal text = %q, want %q", err.Error(), want)
	}
	if depth := limiter.Snapshot().Total.QueueDepth; depth != queuedBodyLimit {
		t.Fatalf("queue depth after the refusal = %d, want %d", depth, queuedBodyLimit)
	}
	if held := heldQueuedBytes(limiter); held != queuedBodyLimit*queuedBodyBytes {
		t.Fatalf("queued bytes after the refusal = %d, want %d", held, queuedBodyLimit*queuedBodyBytes)
	}

	cancel()
	for i, verdict := range waiting {
		if err := verdictOf(t, verdict, "a cancelled waiter"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter %d = %v, want context.Canceled", i, err)
		}
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a byte budget of three bodies, hold its only slot and fill the queue with three bodies.
//  2. Free the slot; assert the earliest waiter is admitted and its bytes are given back while the other two stay held.
//  3. Release the admitted slots twice more so the remaining waiters are admitted; assert the held bytes are zero.
func TestQueuedBytesAreGivenBackWhenAWaiterIsAdmitted(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes})
	waiting := fillQueueToItsByteBudget(t, limiter, context.Background())

	limiter.ReleaseForModel(queuedBytesModel, 1)
	if err := verdictOf(t, waiting[0], "the first waiter"); err != nil {
		t.Fatalf("the first waiter = %v, want admitted", err)
	}
	waitForQueuedBytes(t, limiter, (queuedBodyLimit-1)*queuedBodyBytes)

	for i := 1; i < queuedBodyLimit; i++ {
		limiter.ReleaseForModel(queuedBytesModel, 1)
		if err := verdictOf(t, waiting[i], "a later waiter"); err != nil {
			t.Fatalf("waiter %d = %v, want admitted", i, err)
		}
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1, a 2 s AcquireWait and a byte budget of three bodies, and hold its only slot.
//  2. Queue three bodies and let their wait run out.
//  3. Assert each ends with a `*RateLimitError` and the held bytes return to zero.
func TestQueuedBytesAreGivenBackWhenAWaitTimesOut(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes, AcquireWait: queuedBytesShort})
	waiting := fillQueueToItsByteBudget(t, limiter, context.Background())

	for i, verdict := range waiting {
		var rateLimited *RateLimitError
		if err := verdictOf(t, verdict, "a waiter whose wait runs out"); !errors.As(err, &rateLimited) {
			t.Fatalf("timed out waiter %d = %v, want a rate-limit error", i, err)
		}
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1, one queue place per slot and a byte budget of one body, and hold its only slot.
//  2. Queue one body; then acquire a second, which both the queue depth and the byte budget would refuse, and assert the queue-depth refusal comes first and charges nothing.
//  3. Reconfigure the queue to allow more places, leaving the byte budget at one body; acquire another body and assert `ErrQueuedBytesFull` charges nothing either.
//  4. Assert the held bytes are those of the one waiting body, then cancel it and assert they are zero.
func TestARefusedBodyChargesNothingToTheQueue(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyBytes, AdmissionQueuePerSlot: 1})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := queueBody(ctx, limiter, queuedBodyBytes)
	waitForQueuedBytes(t, limiter, queuedBodyBytes)

	var rateLimited *RateLimitError
	if err := verdictOf(t, queueBody(context.Background(), limiter, queuedBodyBytes), "the body refused by queue depth"); !errors.As(err, &rateLimited) || rateLimited.Label() != RejectionQueueDepth {
		t.Fatalf("the body past the queue depth = %v, want the queue_depth rate-limit error", err)
	}
	if held := heldQueuedBytes(limiter); held != queuedBodyBytes {
		t.Fatalf("queued bytes after the queue-depth refusal = %d, want %d", held, queuedBodyBytes)
	}

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 1, AcquireWait: queuedBytesWait, AdmissionQueuePerSlot: 4, MaxQueuedBytes: queuedBodyBytes})
	if err := verdictOf(t, queueBody(context.Background(), limiter, queuedBodyBytes), "the body refused by the byte budget"); !errors.Is(err, ErrQueuedBytesFull) {
		t.Fatalf("the body past the byte budget = %v, want ErrQueuedBytesFull", err)
	}
	if held := heldQueuedBytes(limiter); held != queuedBodyBytes {
		t.Fatalf("queued bytes after the byte-budget refusal = %d, want %d", held, queuedBodyBytes)
	}

	cancel()
	if err := verdictOf(t, waiting, "the cancelled waiter"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter = %v, want context.Canceled", err)
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a byte budget of three bodies, hold its only slot and fill the queue.
//  2. Reconfigure with MaxConcurrent 4 so the queue is swept; assert all three waiters are admitted and the held bytes are zero.
func TestQueuedBytesAreGivenBackWhenAReconfigureAdmitsTheQueue(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes})
	waiting := fillQueueToItsByteBudget(t, limiter, context.Background())

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 4, AcquireWait: queuedBytesWait, MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes})
	for i, verdict := range waiting {
		if err := verdictOf(t, verdict, "a waiter after a raised cap"); err != nil {
			t.Fatalf("waiter %d = %v, want admitted", i, err)
		}
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a byte budget of three bodies, hold its only slot and fill the queue.
//  2. Assert a fourth body is refused; reconfigure the byte budget to four bodies and assert the fourth body now waits.
//  3. Reconfigure the byte budget to one body; assert the four waiters stay queued and a fifth body is refused.
//  4. Cancel everything and assert the held bytes are zero.
func TestARetunedByteBudgetTakesEffectOnTheNextBody(t *testing.T) {
	t.Parallel()
	limiter := limiterHoldingItsOnlySlot(t, GatewayConfig{MaxQueuedBytes: queuedBodyLimit * queuedBodyBytes})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiting := fillQueueToItsByteBudget(t, limiter, ctx)

	if err := verdictOf(t, queueBody(context.Background(), limiter, queuedBodyBytes), "the fourth body"); !errors.Is(err, ErrQueuedBytesFull) {
		t.Fatalf("the fourth body under a budget of three = %v, want ErrQueuedBytesFull", err)
	}

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 1, AcquireWait: queuedBytesWait, MaxQueuedBytes: (queuedBodyLimit + 1) * queuedBodyBytes})
	waiting = append(waiting, queueBody(ctx, limiter, queuedBodyBytes))
	waitForQueuedBytes(t, limiter, (queuedBodyLimit+1)*queuedBodyBytes)

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 1, AcquireWait: queuedBytesWait, MaxQueuedBytes: queuedBodyBytes})
	if err := verdictOf(t, queueBody(context.Background(), limiter, queuedBodyBytes), "the fifth body"); !errors.Is(err, ErrQueuedBytesFull) {
		t.Fatalf("the fifth body under a lowered budget = %v, want ErrQueuedBytesFull", err)
	}
	if depth := limiter.Snapshot().Total.QueueDepth; depth != queuedBodyLimit+1 {
		t.Fatalf("queue depth after lowering the budget = %d, want the %d waiters to stay", depth, queuedBodyLimit+1)
	}

	cancel()
	for i, verdict := range waiting {
		if err := verdictOf(t, verdict, "a cancelled waiter"); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled waiter %d = %v, want context.Canceled", i, err)
		}
	}
	waitForQueuedBytes(t, limiter, 0)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 2 and a byte budget of one body.
//  2. Acquire twice with bodies of the largest size a request may carry; assert both are admitted without waiting.
//  3. Assert the held bytes are zero: a request that does not wait is not charged.
func TestARequestThatDoesNotWaitIsNotCharged(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 2, AcquireWait: queuedBytesWait, MaxQueuedBytes: queuedLargestBody})
	for i := range 2 {
		if err := limiter.AcquireForModel(context.Background(), queuedBytesModel, 1, queuedLargestBody, fullScale()); err != nil {
			t.Fatalf("acquire %d under a free cap = %v, want admitted", i, err)
		}
	}
	if held := heldQueuedBytes(limiter); held != 0 {
		t.Fatalf("queued bytes after two admitted requests = %d, want 0", held)
	}
}
