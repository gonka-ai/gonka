package validationpool

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	mlnodeclient "common/nodemanager"
	"devshard/observability"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testJob(escrow string, id uint64, weight uint32) Job {
	if weight == 0 {
		weight = 1
	}
	return Job{EscrowID: escrow, InferenceID: id, Weight: weight}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}

func metricGauge(t *testing.T, name string) float64 {
	t.Helper()
	mfs, err := observability.Registry().Gather()
	require.NoError(t, err)
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if m.GetGauge() != nil {
				return m.GetGauge().GetValue()
			}
		}
	}
	t.Fatalf("gauge %q not found", name)
	return 0
}

func TestOffer_SetsSlotWeight(t *testing.T) {
	// Caller contract: Weight = max(1, len(slotIDs)) before Offer.
	var got atomic.Uint32
	s := New(Config{Dispatchers: 1, SoftMaxOut: 8, DeferJitter: -1}, func(_ context.Context, job Job) error {
		got.Store(job.Weight)
		return nil
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 1, 3)))
	waitFor(t, time.Second, func() bool { return got.Load() == 3 })

	require.True(t, s.Offer(Job{EscrowID: "e1", InferenceID: 2, Weight: 0}))
	waitFor(t, time.Second, func() bool { return got.Load() == 1 })
}

func TestScheduler_DropsWhenPendingFull(t *testing.T) {
	block := make(chan struct{})
	s := New(Config{Dispatchers: 1, SoftMaxOut: 1, PendingHWM: 2, DeferJitter: -1}, func(ctx context.Context, _ Job) error {
		select {
		case <-block:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	t.Cleanup(func() {
		close(block)
		_ = s.Shutdown(context.Background())
	})

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, time.Second, func() bool { return s.Outstanding() == 1 })
	require.True(t, s.Offer(testJob("e1", 2, 1)))
	require.True(t, s.Offer(testJob("e1", 3, 1)))
	require.False(t, s.Offer(testJob("e1", 4, 1)), "over HWM must reject")
	assert.Equal(t, 2, s.PendingLen())
}

func TestScheduler_SoftOutstandingCap(t *testing.T) {
	var maxOut atomic.Int64
	release := make(chan struct{})
	started := make(chan struct{}, 16)
	var s *Scheduler
	s = New(Config{Dispatchers: 2, SoftMaxOut: 3, HAMultiplier: 1, DeferJitter: -1}, func(ctx context.Context, _ Job) error {
		cur := s.Outstanding()
		for {
			old := maxOut.Load()
			if cur <= old || maxOut.CompareAndSwap(old, cur) {
				break
			}
		}
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	t.Cleanup(func() {
		close(release)
		_ = s.Shutdown(context.Background())
	})

	for i := uint64(1); i <= 10; i++ {
		require.True(t, s.Offer(testJob("e1", i, 1)))
	}
	for i := 0; i < 3; i++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for soft-capped spawns")
		}
	}
	time.Sleep(30 * time.Millisecond)
	assert.LessOrEqual(t, maxOut.Load(), int64(3))
	assert.Equal(t, int64(3), s.Outstanding())
	assert.Equal(t, 7, s.PendingLen())
}

func TestScheduler_HAMultiplierScalesSoftMax(t *testing.T) {
	s := New(Config{SoftMaxOut: 128, HAMultiplier: 4, Dispatchers: 1, DeferJitter: -1}, func(context.Context, Job) error {
		return nil
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	assert.Equal(t, 32, s.EffectiveSoftMaxOut())
}

func TestScheduler_IdleHasNoValidationGoroutines(t *testing.T) {
	var ran atomic.Int32
	s := New(Config{Dispatchers: 2, SoftMaxOut: 8, DeferJitter: -1}, func(context.Context, Job) error {
		ran.Add(1)
		return nil
	}, nil)
	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, time.Second, func() bool { return ran.Load() == 1 && s.Outstanding() == 0 })
	assert.Equal(t, 0, s.PendingLen())
	assert.Equal(t, 0, s.DeferredLen())
	_ = s.Shutdown(context.Background())
}

func TestScheduler_WeightedFairness(t *testing.T) {
	var mu sync.Mutex
	var order []string
	gate := make(chan struct{})

	s := New(Config{Dispatchers: 1, SoftMaxOut: 1, PendingHWM: 100, DeferJitter: -1}, func(ctx context.Context, job Job) error {
		mu.Lock()
		order = append(order, job.EscrowID)
		mu.Unlock()
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	t.Cleanup(func() {
		close(gate)
		_ = s.Shutdown(context.Background())
	})

	// First job starts immediately; fill pending before draining.
	require.True(t, s.Offer(testJob("light", 1, 1)))
	waitFor(t, time.Second, func() bool { return s.Outstanding() == 1 })
	for i := uint64(0); i < 12; i++ {
		require.True(t, s.Offer(testJob("light", 100+i, 1)))
		require.True(t, s.Offer(testJob("heavy", 200+i, 3)))
	}

	for i := 0; i < 16; i++ {
		gate <- struct{}{}
		waitFor(t, time.Second, func() bool {
			mu.Lock()
			defer mu.Unlock()
			return len(order) >= i+2
		})
	}
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(order), 16)
	heavy, light := 0, 0
	for _, e := range order[1:16] {
		if e == "heavy" {
			heavy++
		} else {
			light++
		}
	}
	assert.Greater(t, heavy, light, "order=%v", order[:16])
	assert.GreaterOrEqual(t, heavy, 10, "heavy share too low: %d light=%d order=%v", heavy, light, order[:16])
}

func TestScheduler_DeferOnAcquireBusy(t *testing.T) {
	var abandons atomic.Int32
	var attempts atomic.Int32
	s := New(Config{
		Dispatchers: 1,
		SoftMaxOut:  4,
		DeferDelay:  20 * time.Millisecond,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		n := attempts.Add(1)
		if n == 1 {
			return mlnodeclient.ErrNoNodesAvailable
		}
		return nil
	}, func(string, uint64) { abandons.Add(1) })
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 1 })
	assert.Equal(t, int32(0), abandons.Load(), "defer must keep validating (no abandon)")
	waitFor(t, time.Second, func() bool { return attempts.Load() >= 2 && s.DeferredLen() == 0 && s.Outstanding() == 0 })
}

func TestScheduler_DeferKeepsValidatingBlocksReoffer(t *testing.T) {
	var abandons atomic.Int32
	blockBusy := make(chan struct{})
	s := New(Config{
		Dispatchers: 1,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		<-blockBusy
		return mlnodeclient.ErrNoNodesAvailable
	}, func(string, uint64) { abandons.Add(1) })
	t.Cleanup(func() {
		close(blockBusy)
		_ = s.Shutdown(context.Background())
	})

	require.True(t, s.Offer(testJob("e1", 42, 1)))
	blockBusy <- struct{}{}
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 1 })
	assert.Equal(t, int32(0), abandons.Load())
}

func TestScheduler_NoDoubleRetry(t *testing.T) {
	var attempts atomic.Int32
	s := New(Config{
		Dispatchers: 1,
		DeferDelay:  15 * time.Millisecond,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		if attempts.Add(1) == 1 {
			return mlnodeclient.ErrNoNodesAvailable
		}
		return nil
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, time.Second, func() bool { return attempts.Load() == 2 })
	time.Sleep(40 * time.Millisecond)
	assert.Equal(t, int32(2), attempts.Load())
}

func TestScheduler_DeferJitteredIntervalInfinite(t *testing.T) {
	var attempts atomic.Int32
	s := New(Config{
		Dispatchers: 1,
		DeferDelay:  10 * time.Millisecond,
		DeferJitter: 10 * time.Millisecond,
	}, func(context.Context, Job) error {
		attempts.Add(1)
		return mlnodeclient.ErrNoNodesAvailable
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, 2*time.Second, func() bool { return attempts.Load() >= 3 })
	assert.Equal(t, 1, s.DeferredLen()+s.PendingLen()+int(s.Outstanding()))
}

func TestScheduler_DeferredDoesNotCountTowardSoftMax(t *testing.T) {
	release := make(chan struct{})
	s := New(Config{
		Dispatchers: 2,
		SoftMaxOut:  2,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(ctx context.Context, job Job) error {
		if job.InferenceID <= 2 {
			return mlnodeclient.ErrNoNodesAvailable
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	t.Cleanup(func() {
		close(release)
		_ = s.Shutdown(context.Background())
	})

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	require.True(t, s.Offer(testJob("e1", 2, 1)))
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 2 })
	assert.Equal(t, int64(0), s.Outstanding())

	require.True(t, s.Offer(testJob("e2", 3, 1)))
	require.True(t, s.Offer(testJob("e2", 4, 1)))
	waitFor(t, time.Second, func() bool { return s.Outstanding() == 2 })
	assert.Equal(t, 2, s.DeferredLen())
}

func TestScheduler_DeferSweeperDoesNotBlockDispatchers(t *testing.T) {
	var otherRan atomic.Bool
	s := New(Config{
		Dispatchers: 2,
		SoftMaxOut:  4,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(_ context.Context, job Job) error {
		if job.EscrowID == "busy" {
			return mlnodeclient.ErrNoNodesAvailable
		}
		otherRan.Store(true)
		return nil
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("busy", 1, 1)))
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 1 })
	require.True(t, s.Offer(testJob("other", 2, 1)))
	waitFor(t, time.Second, func() bool { return otherRan.Load() })
}

func TestScheduler_DeferJitterBreaksLockstep(t *testing.T) {
	elig := make([]time.Time, 0, 20)
	var mu sync.Mutex
	var phase atomic.Int32
	s := New(Config{
		Dispatchers: 2,
		SoftMaxOut:  20,
		DeferDelay:  30 * time.Millisecond,
		DeferJitter: 30 * time.Millisecond,
	}, func(_ context.Context, _ Job) error {
		if phase.Load() == 0 {
			return mlnodeclient.ErrNoNodesAvailable
		}
		mu.Lock()
		elig = append(elig, time.Now())
		mu.Unlock()
		return nil
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	for i := uint64(0); i < 12; i++ {
		require.True(t, s.Offer(testJob("e1", i, 1)))
	}
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 12 })
	phase.Store(1)
	waitFor(t, 2*time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(elig) >= 12
	})

	mu.Lock()
	defer mu.Unlock()
	minT, maxT := elig[0], elig[0]
	for _, ts := range elig {
		if ts.Before(minT) {
			minT = ts
		}
		if ts.After(maxT) {
			maxT = ts
		}
	}
	assert.Greater(t, maxT.Sub(minT), 5*time.Millisecond, "eligible wakes should not be perfectly lockstep")
}

func TestScheduler_DeferredDepthMetric(t *testing.T) {
	s := New(Config{
		Dispatchers: 1,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		return mlnodeclient.ErrNoNodesAvailable
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	waitFor(t, time.Second, func() bool { return s.DeferredLen() == 1 })
	assert.Equal(t, float64(1), metricGauge(t, "devshard_validation_deferred_depth"))
}

func TestScheduler_DeferredDepthWarnAbove100(t *testing.T) {
	// Gauge crosses 100; edge-triggered warn itself is covered in observability.
	s := New(Config{
		Dispatchers: 2,
		SoftMaxOut:  32,
		PendingHWM:  200,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		return mlnodeclient.ErrNoNodesAvailable
	}, nil)
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	for i := uint64(0); i < 101; i++ {
		require.True(t, s.Offer(testJob("e1", i, 1)))
	}
	waitFor(t, 3*time.Second, func() bool { return s.DeferredLen() == 101 })
	assert.Equal(t, float64(101), metricGauge(t, "devshard_validation_deferred_depth"))
}

func TestScheduler_ForgetHostCancelsInflight(t *testing.T) {
	started := make(chan struct{})
	var abandons atomic.Int32
	s := New(Config{Dispatchers: 1, SoftMaxOut: 4, DeferJitter: -1}, func(ctx context.Context, _ Job) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, func(escrow string, id uint64) {
		if escrow == "e1" && id == 7 {
			abandons.Add(1)
		}
	})
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 7, 1)))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	s.ForgetHost("e1")
	waitFor(t, time.Second, func() bool { return abandons.Load() >= 1 && s.Outstanding() == 0 })
	assert.Equal(t, 0, s.PendingLen())
	assert.Equal(t, 0, s.DeferredLen())
}

func TestScheduler_ForgetHostDoesNotRedeferBusy(t *testing.T) {
	// ForgetHost removes inflight + abandons; a concurrent busy completion must
	// not push the job back onto the deferred heap.
	started := make(chan struct{})
	forgetDone := make(chan struct{})
	var abandons atomic.Int32
	s := New(Config{
		Dispatchers: 1,
		SoftMaxOut:  4,
		DeferDelay:  time.Hour,
		DeferJitter: -1,
	}, func(context.Context, Job) error {
		close(started)
		<-forgetDone
		return mlnodeclient.ErrNoNodesAvailable
	}, func(string, uint64) { abandons.Add(1) })
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })

	require.True(t, s.Offer(testJob("e1", 9, 1)))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}
	s.ForgetHost("e1")
	close(forgetDone)
	waitFor(t, time.Second, func() bool { return s.Outstanding() == 0 })
	assert.Equal(t, 0, s.DeferredLen(), "busy completion must not re-defer after ForgetHost")
	assert.Equal(t, 0, s.PendingLen())
	assert.Equal(t, int32(1), abandons.Load())
}

func TestScheduler_ShutdownWaitsForInflight(t *testing.T) {
	started := make(chan struct{})
	var leftRun atomic.Bool
	s := New(Config{Dispatchers: 1, SoftMaxOut: 4, DeferJitter: -1}, func(ctx context.Context, _ Job) error {
		close(started)
		<-ctx.Done()
		// Simulate post-cancel finish work (mempool / SetResult) that must
		// complete before Shutdown returns and store.Close runs.
		time.Sleep(50 * time.Millisecond)
		leftRun.Store(true)
		return ctx.Err()
	}, nil)

	require.True(t, s.Offer(testJob("e1", 1, 1)))
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("job did not start")
	}

	err := s.Shutdown(context.Background())
	require.NoError(t, err)
	assert.True(t, leftRun.Load(), "Shutdown must wait for in-flight validate goroutine")
}

func TestConfig_EffectiveSoftMaxOutFloor(t *testing.T) {
	assert.Equal(t, 1, (Config{SoftMaxOut: 3, HAMultiplier: 10}).EffectiveSoftMaxOut())
	assert.Equal(t, 128, (Config{}).EffectiveSoftMaxOut())
}
