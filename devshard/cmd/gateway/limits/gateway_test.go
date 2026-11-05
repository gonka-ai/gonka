package limits

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/internal/leakcheck"
)

func fullScale() ModelCapacity { return ModelCapacity{ScaleFactor: 1} }

func int64Ptr(v int64) *int64 { return &v }

// Test flow:
//  1. Create a limiter with MaxConcurrent 2.
//  2. Acquire for modelA twice in a row.
//  3. Assert both acquires succeed.
func TestAcquireForModel_AdmitsUnderCap(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 2, AcquireWait: time.Second})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 10, fullScale()); err != nil {
		t.Fatalf("first AcquireForModel: %v", err)
	}
	if err := limiter.AcquireForModel(context.Background(), "modelA", 10, fullScale()); err != nil {
		t.Fatalf("second AcquireForModel: %v", err)
	}
}

// Test flow:
//  1. Create a limiter with MaxInputTokens 100 and a short AcquireWait.
//  2. Acquire 80 input tokens for modelA; assert it succeeds.
//  3. Acquire 50 more input tokens for modelA, exceeding the budget.
//  4. Assert the second acquire returns a `*RateLimitError` with reasonTooManyInputTokens and a RetryAfter equal to the configured wait, after actually blocking for that full wait.
func TestAcquireForModel_InputTokenBudgetEnforced(t *testing.T) {
	t.Parallel()
	wait := 60 * time.Millisecond
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 100, MaxInputTokens: 100, AcquireWait: wait})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 80, fullScale()); err != nil {
		t.Fatalf("first AcquireForModel: %v", err)
	}

	start := time.Now()
	err := limiter.AcquireForModel(context.Background(), "modelA", 50, fullScale())
	elapsed := time.Since(start)

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("second AcquireForModel error = %v, want *RateLimitError", err)
	}
	if rateLimitErr.Reason != reasonTooManyInputTokens {
		t.Errorf("Reason = %q, want %q", rateLimitErr.Reason, reasonTooManyInputTokens)
	}
	if rateLimitErr.RetryAfter != wait {
		t.Errorf("RetryAfter = %v, want %v", rateLimitErr.RetryAfter, wait)
	}
	if elapsed < wait {
		t.Errorf("elapsed = %v, want >= AcquireWait %v (should wait the full budget before rejecting)", elapsed, wait)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and acquire its only slot for modelA.
//  2. Start a goroutine that releases that slot after 80ms.
//  3. Acquire modelA again, which blocks until the release.
//  4. Assert the second acquire succeeds after blocking at least ~30ms but well under the 1s AcquireWait.
func TestAcquireForModel_BlocksThenAdmitsWhenSlotFreesWithinWait(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, AcquireWait: time.Second})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 1, fullScale()); err != nil {
		t.Fatalf("initial AcquireForModel: %v", err)
	}

	go func() {
		time.Sleep(80 * time.Millisecond)
		limiter.ReleaseForModel("modelA", 1)
	}()

	start := time.Now()
	err := limiter.AcquireForModel(context.Background(), "modelA", 1, fullScale())
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AcquireForModel after release = %v, want nil", err)
	}
	if elapsed < 30*time.Millisecond {
		t.Errorf("elapsed = %v, want at least ~30ms of blocking before the release freed a slot", elapsed)
	}
	if elapsed >= time.Second {
		t.Errorf("elapsed = %v, want well under the 1s AcquireWait", elapsed)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and MaxInputTokens 100, and acquire modelA's only slot.
//  2. Queue a second modelA acquire in a goroutine and wait for the queue depth to reach 1.
//  3. Assert `Snapshot`'s ByModel and EnforcedByModel entries for modelA report the queued waiter and the configured limits.
//  4. Release the slot, assert the queued acquire completes, and assert the queue depth returns to 0.
func TestSnapshot_CountsAWaiterAgainstItsModelsQueue(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, MaxInputTokens: 100, AcquireWait: time.Second})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 10, fullScale()); err != nil {
		t.Fatalf("initial AcquireForModel: %v", err)
	}
	queued := make(chan error, 1)
	go func() { queued <- limiter.AcquireForModel(context.Background(), "modelA", 10, fullScale()) }()

	waitFor(t, func() bool { return limiter.Snapshot().Total.QueueDepth == 1 })
	snapshot := limiter.Snapshot()
	if got := snapshot.ByModel["modelA"]; got.Requests != 1 || got.InputTokens != 10 || got.QueueDepth != 1 {
		t.Fatalf("ByModel[modelA] = %+v, want 1 request, 10 tokens, 1 queued", got)
	}
	if enforced := snapshot.EnforcedByModel["modelA"]; enforced.MaxConcurrentRequests != 1 || enforced.MaxInputTokensInFlight != 100 {
		t.Fatalf("EnforcedByModel[modelA] = %+v, want 1/100", enforced)
	}

	limiter.ReleaseForModel("modelA", 10)
	if err := <-queued; err != nil {
		t.Fatalf("queued AcquireForModel = %v, want nil", err)
	}
	if got := limiter.Snapshot().Total.QueueDepth; got != 0 {
		t.Fatalf("queue depth after promotion = %d, want 0", got)
	}
	limiter.ReleaseForModel("modelA", 10)
}

func waitFor(t *testing.T, holds func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !holds() {
		if time.Now().After(deadline) {
			t.Fatal("condition never held")
		}
		runtime.Gosched()
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a short AcquireWait, and acquire its only slot.
//  2. Acquire modelA again without releasing the first slot.
//  3. Assert the second acquire returns a `*RateLimitError` with reasonTooManyConcurrentRequests and a RetryAfter equal to the configured wait, after blocking for roughly that long.
func TestAcquireForModel_TimesOutWhenNoSlotFrees(t *testing.T) {
	t.Parallel()
	wait := 60 * time.Millisecond
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, AcquireWait: wait})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 1, fullScale()); err != nil {
		t.Fatalf("initial AcquireForModel: %v", err)
	}

	start := time.Now()
	err := limiter.AcquireForModel(context.Background(), "modelA", 1, fullScale())
	elapsed := time.Since(start)

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("AcquireForModel error = %v, want *RateLimitError", err)
	}
	if rateLimitErr.Reason != reasonTooManyConcurrentRequests {
		t.Errorf("Reason = %q, want %q", rateLimitErr.Reason, reasonTooManyConcurrentRequests)
	}
	if rateLimitErr.RetryAfter != wait {
		t.Errorf("RetryAfter = %v, want %v", rateLimitErr.RetryAfter, wait)
	}
	if elapsed < wait {
		t.Errorf("elapsed = %v, want >= AcquireWait %v", elapsed, wait)
	}
	if elapsed > wait+2*time.Second {
		t.Errorf("elapsed = %v, want reasonably close to AcquireWait %v", elapsed, wait)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a 1s AcquireWait, and acquire its only slot.
//  2. Start a goroutine that cancels a fresh context after 25ms.
//  3. Acquire modelA on that context in a goroutine.
//  4. Assert it returns `context.Canceled` well before the 1s AcquireWait, within a 400ms bound.
func TestAcquireForModel_CtxCancelWakesPromptly(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, AcquireWait: time.Second})

	if err := limiter.AcquireForModel(context.Background(), "modelA", 1, fullScale()); err != nil {
		t.Fatalf("initial AcquireForModel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(25 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- limiter.AcquireForModel(ctx, "modelA", 1, fullScale()) }()

	select {
	case err := <-done:
		elapsed := time.Since(start)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("AcquireForModel error = %v, want context.Canceled", err)
		}
		if elapsed >= time.Second {
			t.Errorf("elapsed = %v, want well under the 1s AcquireWait (ctx-cancel should wake it promptly)", elapsed)
		}
	case <-time.After(400 * time.Millisecond):
		t.Fatal("AcquireForModel did not return within 400ms of ctx cancellation")
	}
}

// Test flow:
//  1. Create a limiter and a context that is already cancelled.
//  2. Acquire modelA on that context.
//  3. Assert it returns `context.Canceled` immediately.
func TestAcquireForModel_ContextAlreadyCancelled(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 5, AcquireWait: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := limiter.AcquireForModel(ctx, "modelA", 1, fullScale()); !errors.Is(err, context.Canceled) {
		t.Fatalf("AcquireForModel with pre-cancelled ctx = %v, want context.Canceled", err)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 5 and a 1s AcquireWait.
//  2. Acquire modelA with a `ModelCapacity{ScaleFactor: 0}`.
//  3. Assert it returns a `*RateLimitError` with reasonTooManyConcurrentRequests and a RetryAfter equal to the configured AcquireWait, but returns near-instantly rather than waiting the full wait, since a zero scale cannot change mid-wait.
func TestAcquireForModel_ScaleFactorZeroBlocksImmediately(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 5, AcquireWait: time.Second})

	start := time.Now()
	err := limiter.AcquireForModel(context.Background(), "modelA", 1, ModelCapacity{ScaleFactor: 0})
	elapsed := time.Since(start)

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("AcquireForModel error = %v, want *RateLimitError", err)
	}
	if rateLimitErr.Reason != reasonTooManyConcurrentRequests {
		t.Errorf("Reason = %q, want %q", rateLimitErr.Reason, reasonTooManyConcurrentRequests)
	}
	if rateLimitErr.RetryAfter != time.Second {
		t.Errorf("RetryAfter = %v, want 1s (the configured AcquireWait)", rateLimitErr.RetryAfter)
	}
	if elapsed >= 200*time.Millisecond {
		t.Errorf("elapsed = %v, want near-instant rejection (scale can't change mid-wait, so waiting is pointless)", elapsed)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1000 and a short AcquireWait.
//  2. Build a `ModelCapacity` with CurrentWeight and BaselineWeight both 2000 and MaxConcurrentPer10000Weight 5, giving a dynamic cap of floor(2000*5/10000) = 1.
//  3. Acquire modelA once; assert it succeeds.
//  4. Acquire modelA again; assert it is rejected as a rate-limit error after waiting the full AcquireWait, since the dynamic cap of 1 is already used.
func TestAcquireForModel_PerTenThousandWeightDynamicCap(t *testing.T) {
	t.Parallel()
	wait := 40 * time.Millisecond
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1000, AcquireWait: wait})
	capacity := ModelCapacity{
		CurrentWeight:               2000,
		BaselineWeight:              2000,
		MaxConcurrentPer10000Weight: 5,
	}

	if err := limiter.AcquireForModel(context.Background(), "modelA", 1, capacity); err != nil {
		t.Fatalf("first AcquireForModel: %v", err)
	}

	start := time.Now()
	err := limiter.AcquireForModel(context.Background(), "modelA", 1, capacity)
	elapsed := time.Since(start)

	var rateLimitErr *RateLimitError
	if !errors.As(err, &rateLimitErr) {
		t.Fatalf("second AcquireForModel error = %v, want *RateLimitError (dynamic cap of 1 already used)", err)
	}
	if elapsed < wait {
		t.Errorf("elapsed = %v, want >= AcquireWait %v", elapsed, wait)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1000 and a 1ms AcquireWait.
//  2. Build a `ModelCapacity` with CurrentWeight 5000 above BaselineWeight 1000 and MaxConcurrentPer10000Weight 100, giving a current-derived cap of 50 but a baseline-derived cap of 10.
//  3. Acquire modelA 10 times; assert every one succeeds.
//  4. Acquire an 11th time; assert it is rejected, since the baseline-derived cap of 10 bounds it regardless of the higher current weight.
func TestAcquireForModel_DynamicCapClampsToBaselineWeight(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1000, AcquireWait: time.Millisecond})
	capacity := ModelCapacity{
		CurrentWeight:               5000,
		BaselineWeight:              1000,
		MaxConcurrentPer10000Weight: 100,
	}

	for i := range 10 {
		if err := limiter.AcquireForModel(context.Background(), "modelA", 1, capacity); err != nil {
			t.Fatalf("acquire %d = %v, want admitted under the baseline-derived cap of 10", i, err)
		}
	}
	if err := limiter.AcquireForModel(context.Background(), "modelA", 1, capacity); err == nil {
		t.Fatal("11th AcquireForModel = nil, want rejection: current weight must not lift the cap above the baseline-derived limit")
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 0 (unlimited baseline) and a 1ms AcquireWait.
//  2. Acquire modelA 50 times with `ScaleFactor: 0`.
//  3. Assert every acquire succeeds, since a zero baseline is unlimited even at zero scale.
func TestAcquireForModel_BaselineZeroIsUnlimited(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 0, AcquireWait: time.Millisecond})

	for i := range 50 {
		if err := limiter.AcquireForModel(context.Background(), "modelA", 1, ModelCapacity{ScaleFactor: 0}); err != nil {
			t.Fatalf("acquire %d = %v, want nil (baseline 0 means unlimited even at scale 0)", i, err)
		}
	}
}

// Test flow:
//  1. Create a limiter with a default MaxConcurrent of 10 and a per-model override capping "tight-model" at MaxConcurrent 1.
//  2. Acquire "tight-model" once; assert it succeeds.
//  3. Acquire "tight-model" again; assert it is rejected after waiting the AcquireWait, since the override's cap of 1 is spent.
//  4. Acquire "other-model" 5 times; assert every one succeeds under its own default budget of 10, unaffected by the override.
func TestAcquireForModel_PerModelOverride(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{
		MaxConcurrent: 10,
		AcquireWait:   30 * time.Millisecond,
		ModelLimits: map[string]ModelOverride{
			"tight-model": {MaxConcurrent: int64Ptr(1)},
		},
	})

	if err := limiter.AcquireForModel(context.Background(), "tight-model", 1, fullScale()); err != nil {
		t.Fatalf("first AcquireForModel(tight-model): %v", err)
	}

	start := time.Now()
	if err := limiter.AcquireForModel(context.Background(), "tight-model", 1, fullScale()); err == nil {
		t.Fatal("second AcquireForModel(tight-model) = nil, want rejection under the per-model override cap of 1")
	}
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Errorf("elapsed = %v, want >= AcquireWait 30ms", elapsed)
	}

	for i := range 5 {
		if err := limiter.AcquireForModel(context.Background(), "other-model", 1, fullScale()); err != nil {
			t.Fatalf("AcquireForModel(other-model) %d = %v, want nil (its own budget of 10, not the override)", i, err)
		}
	}
}

// Test flow:
//  1. Create a limiter.
//  2. Release a model that was never acquired.
//  3. Assert the call does not panic.
func TestReleaseForModel_UnknownModelIsNoop(t *testing.T) {
	t.Parallel()
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1})
	limiter.ReleaseForModel("never-acquired", 5)
}

// Test flow:
//  1. Build a `RateLimitError` with a reason and a RetryAfter.
//  2. Call `Error()`.
//  3. Assert the message matches the expected "rate limit exceeded: ..." text.
func TestRateLimitError_Error(t *testing.T) {
	t.Parallel()
	err := &RateLimitError{Reason: reasonTooManyConcurrentRequests, RetryAfter: time.Second}
	want := "rate limit exceeded: too many concurrent requests"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// Test flow:
//  1. Wrap the test in `leakcheck.VerifyNone` and create a limiter with MaxConcurrent 3, MaxInputTokens 30, and a 20ms AcquireWait.
//  2. Start many goroutines that repeatedly acquire modelA under a short-lived context, releasing on success and ignoring failures.
//  3. Wait for every goroutine to finish.
//  4. Assert modelA's counter and the limiter's total counter both settle back to zero in-flight requests and zero input tokens, even though the counter entry itself may still exist rather than being cleaned up.
func TestGatewayLimiter_ConcurrencyRace(t *testing.T) {
	defer leakcheck.VerifyNone(t)

	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 3, MaxInputTokens: 30, AcquireWait: 20 * time.Millisecond})

	const goroutines = 20
	const iterations = 25
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			for range iterations {
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				err := limiter.AcquireForModel(ctx, "modelA", 3, fullScale())
				cancel()
				if err != nil {
					continue
				}
				limiter.ReleaseForModel("modelA", 3)
			}
		}()
	}
	wg.Wait()

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if counter, held := limiter.models["modelA"]; held && (counter.inFlight != 0 || counter.inputTokens != 0) {
		t.Errorf("residual counter after race = %+v, want zero on both", counter)
	}
	if limiter.total.inFlight != 0 || limiter.total.inputTokens != 0 {
		t.Errorf("residual total after race = %+v, want zero on both", limiter.total)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a shared full-scale `ModelCapacity`.
//  2. Acquire "model-a" once; assert it succeeds.
//  3. Acquire "model-b" once; assert it also succeeds, since it has its own separate budget.
//  4. Acquire "model-a" again; assert it is rejected as a rate-limit error because model-a's single-slot budget is already spent.
func TestAcquireForModelGivesEachModelItsOwnBudget(t *testing.T) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1})
	capacity := ModelCapacity{ScaleFactor: 1}
	ctx := context.Background()

	if err := limiter.AcquireForModel(ctx, "model-a", 1, capacity); err != nil {
		t.Fatalf("acquire on model-a: %v", err)
	}
	if err := limiter.AcquireForModel(ctx, "model-b", 1, capacity); err != nil {
		t.Fatalf("acquire on model-b = %v, want nil: model-a's traffic must not spend model-b's budget", err)
	}

	var rateLimit *RateLimitError
	if err := limiter.AcquireForModel(ctx, "model-a", 1, capacity); !errors.As(err, &rateLimit) {
		t.Fatalf("second acquire on model-a = %v, want a rate-limit error: its own budget of 1 is spent", err)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1000 and a shared `ModelCapacity` with CurrentWeight and BaselineWeight both 4000 and MaxConcurrentPer10000Weight 5, giving each model its own weight-derived cap of floor(4000*5/10000) = 2.
//  2. Acquire "model-a" twice with that capacity; assert both succeed.
//  3. Acquire "model-b" twice with the same capacity value; assert both also succeed, since the cap is tracked per model rather than shared.
//  4. Acquire "model-b" a third time; assert it is rejected as a rate-limit error at its own cap of 2.
func TestAcquireForModelWeightDerivedCapIsNotSpentByAnotherModel(t *testing.T) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1000})
	ctx := context.Background()
	perModelCapacity := ModelCapacity{CurrentWeight: 4000, BaselineWeight: 4000, MaxConcurrentPer10000Weight: 5}

	for i := range 2 {
		if err := limiter.AcquireForModel(ctx, "model-a", 1, perModelCapacity); err != nil {
			t.Fatalf("acquire %d on model-a = %v, want nil under its weight-derived cap of 2", i, err)
		}
	}

	for i := range 2 {
		if err := limiter.AcquireForModel(ctx, "model-b", 1, perModelCapacity); err != nil {
			t.Fatalf("acquire %d on model-b = %v, want nil: model-b's weight buys model-b two slots of its own", i, err)
		}
	}
	var rateLimit *RateLimitError
	if err := limiter.AcquireForModel(ctx, "model-b", 1, perModelCapacity); !errors.As(err, &rateLimit) {
		t.Fatalf("third acquire on model-b = %v, want a rate-limit error at its weight-derived cap of 2", err)
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1.
//  2. Acquire "model-a" with `ScaleFactor: 0.5`.
//  3. Assert it succeeds, since the cap of 1 rounds to 1 rather than flooring to 0 at half capacity.
func TestAcquireForModelSmallCapSurvivesPartialCapacity(t *testing.T) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1})

	if err := limiter.AcquireForModel(context.Background(), "model-a", 1, ModelCapacity{ScaleFactor: 0.5}); err != nil {
		t.Fatalf("acquire at half capacity under a cap of 1 = %v, want nil (rounded to 1, not floored to 0)", err)
	}
}

// waitForQueueLen polls the limiter's queue rather than sleeping, so the test is deterministic.
func waitForQueueLen(t *testing.T, limiter *GatewayLimiter, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		limiter.mu.Lock()
		got := len(limiter.queue)
		limiter.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue length = %d, want %d within 2s", got, want)
		}
		runtime.Gosched()
	}
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a 2s AcquireWait, and acquire model-a's only slot.
//  2. Queue an earlier waiter in a goroutine and wait for the queue length to reach 1.
//  3. Queue a later waiter in a second goroutine and wait for the queue length to reach 2.
//  4. Release the slot once; assert the earlier waiter is admitted, not the later one.
//  5. Release again; assert the later waiter is then admitted too.
func TestAcquireForModelHandsFreedSlotToTheEarlierWaiter(t *testing.T) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, AcquireWait: 2 * time.Second})
	capacity := ModelCapacity{ScaleFactor: 1}
	ctx := context.Background()

	if err := limiter.AcquireForModel(ctx, "model-a", 1, capacity); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}

	earlier := make(chan error, 1)
	go func() { earlier <- limiter.AcquireForModel(ctx, "model-a", 1, capacity) }()
	waitForQueueLen(t, limiter, 1)

	later := make(chan error, 1)
	go func() { later <- limiter.AcquireForModel(ctx, "model-a", 1, capacity) }()
	waitForQueueLen(t, limiter, 2)

	limiter.ReleaseForModel("model-a", 1)

	select {
	case err := <-earlier:
		if err != nil {
			t.Fatalf("earlier waiter = %v, want admission", err)
		}
	case <-later:
		t.Fatal("the later arrival was admitted first; the earlier waiter can be starved past its deadline")
	case <-time.After(2 * time.Second):
		t.Fatal("no waiter was admitted after a slot was freed")
	}

	limiter.ReleaseForModel("model-a", 1)
	if err := <-later; err != nil {
		t.Fatalf("later waiter after a second release = %v, want admission", err)
	}
	limiter.ReleaseForModel("model-a", 1)
}

// Test flow:
//  1. Create a limiter with MaxConcurrent 1 and a 10s AcquireWait, and acquire modelX's only slot.
//  2. Queue a second acquire in a goroutine and wait for the queue depth to reach 1.
//  3. Reconfigure the limiter to raise MaxConcurrent to 2.
//  4. Assert the queued waiter is admitted and the snapshot reports the new enforced concurrency of 2.
//  5. Reconfigure again, lowering MaxConcurrent to 1 with AcquireWait 0.
//  6. Assert a further acquire is rejected immediately as a rate-limit error under the lowered cap.
func TestReconfigureAppliesToTheNextAcquireAndReleasesTheQueue(t *testing.T) {
	limiter := NewGatewayLimiter(GatewayConfig{MaxConcurrent: 1, AcquireWait: 10 * time.Second})
	if err := limiter.AcquireForModel(context.Background(), "modelX", 1, fullScale()); err != nil {
		t.Fatalf("first AcquireForModel(): %v", err)
	}

	queued := make(chan error, 1)
	go func() { queued <- limiter.AcquireForModel(context.Background(), "modelX", 1, fullScale()) }()
	waitFor(t, func() bool { return limiter.Snapshot().Total.QueueDepth == 1 })

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 2, AcquireWait: 10 * time.Second})

	select {
	case err := <-queued:
		if err != nil {
			t.Fatalf("the queued request after a raised cap = %v, want admitted", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a raised cap never reached the queue: the waiter is still blocked")
	}
	if got := limiter.Snapshot().EnforcedByModel["modelX"].MaxConcurrentRequests; got != 2 {
		t.Fatalf("EnforcedByModel[modelX] concurrency = %d, want 2", got)
	}

	limiter.Reconfigure(GatewayConfig{MaxConcurrent: 1, AcquireWait: 0})
	var rateLimited *RateLimitError
	if err := limiter.AcquireForModel(context.Background(), "modelX", 1, fullScale()); !errors.As(err, &rateLimited) {
		t.Fatalf("AcquireForModel() after a lowered cap = %v, want a rate-limit error", err)
	}
}

// Test flow:
//  1. Iterate over every known rejection reason constant.
//  2. Build a `RateLimitError` for each reason and call `Label()`.
//  3. Assert no reason returns "unnamed" or an empty label.
func TestEveryRejectionReasonHasALabel(t *testing.T) {
	for _, reason := range []string{
		reasonTooManyConcurrentRequests, reasonTooManyInputTokens, reasonQueueTooDeep,
	} {
		if label := (&RateLimitError{Reason: reason}).Label(); label == "unnamed" || label == "" {
			t.Errorf("reason %q has no label", reason)
		}
	}
}
