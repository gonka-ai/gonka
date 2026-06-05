package main

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCapacityStateEscrowWeightSplitsSharedHosts(t *testing.T) {
	m := NewCapacityState()
	// Host A has 2 slots in escrow X and 1 slot in escrow Y.
	// Host B has 1 slot in escrow X only.
	m.SetEscrowMembership("X", map[string]int{"A": 2, "B": 1})
	m.SetEscrowMembership("Y", map[string]int{"A": 1})
	// Outside PoC -> updates both fullWeights and currentWeights.
	m.SetHostWeights(map[string]float64{"A": 3, "B": 1}, false)

	// W(X) = 3*(2/3) + 1*(1/1) = 2 + 1 = 3
	require.InDelta(t, 3.0, m.EscrowWeight("X"), 1e-9)
	// W(Y) = 3*(1/3) = 1
	require.InDelta(t, 1.0, m.EscrowWeight("Y"), 1e-9)
	// W_tot = 3 (A counted once) + 1 (B) = 4
	require.InDelta(t, 4.0, m.TotalWeight(), 1e-9)
}

func TestCapacityStatePoCPreservationDropsUnpreservedHosts(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1, "B": 1})
	m.SetHostWeights(map[string]float64{"A": 2, "B": 2}, false)
	// PoC active, only A preserved.
	m.SetPoCPreserved([]string{"A"})

	require.InDelta(t, 2.0, m.EscrowWeight("X"), 1e-9)
	require.InDelta(t, 2.0, m.TotalWeight(), 1e-9)
}

func TestCapacityStateLiveAvailabilityScalesWeights(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1, "B": 1})
	m.SetHostWeights(map[string]float64{"A": 1, "B": 1}, false)

	// Binary availability: A is unavailable so it drops out, B stays.
	m.SetLiveAvailable(func(host string) bool {
		return host != "A"
	})

	// W(X) = 1 (B only, A throttled to zero).
	require.InDelta(t, 1.0, m.EscrowWeight("X"), 1e-9)
}

func TestCapacityStateMissingHostFallsBackToWeightOne(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1})
	// No SetHostWeights call - never observed -> fallback to 1.
	require.InDelta(t, 1.0, m.EscrowWeight("X"), 1e-9)
}

func TestCapacityStateBaselineFromFullWeightsNotFrozen(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1, "B": 1})

	// Steady state: both hosts at weight 1, baseline is 2.
	m.SetHostWeights(map[string]float64{"A": 1, "B": 1}, false)
	require.InDelta(t, 2.0, m.BaselineWeight(), 1e-9)
	require.InDelta(t, 1.0, m.ScaleFactor(), 1e-9)

	// PoC enters: chain reports B at zero weight, but the observation
	// happens during PoC so fullWeights stays at the steady-state
	// values. currentWeights drops -> W_tot drops -> scale 0.5.
	m.SetHostWeights(map[string]float64{"A": 1, "B": 0}, true)
	m.SetPoCPreserved([]string{"A"})
	require.InDelta(t, 2.0, m.BaselineWeight(), 1e-9, "baseline must stay at full weights during PoC")
	require.InDelta(t, 0.5, m.ScaleFactor(), 1e-9)

	// PoC exits: a fresh poll outside PoC restores the baseline view.
	m.SetPoCPreserved(nil)
	m.SetHostWeights(map[string]float64{"A": 1, "B": 1}, false)
	require.InDelta(t, 2.0, m.BaselineWeight(), 1e-9)
	require.InDelta(t, 1.0, m.ScaleFactor(), 1e-9)
}

func TestCapacityStateTotalWeightUsesAdjustedPoCWeights(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1, "B": 1, "C": 1})
	m.SetHostWeights(map[string]float64{"A": 100, "B": 50, "C": 25}, false)

	m.SetHostWeights(map[string]float64{"A": 40, "B": 0, "C": 10}, true)
	m.SetPoCPreserved([]string{"A", "C"})

	require.InDelta(t, 50.0, m.TotalWeight(), 1e-9)
	require.InDelta(t, 175.0, m.BaselineWeight(), 1e-9, "baseline must keep full weights during PoC")
}

func TestCapacityStateUsesModelSpecificPoCWeights(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1, "B": 1})
	m.SetHostWeights(map[string]float64{"A": 100, "B": 50}, false)
	m.SetHostWeightsByModel(map[string]map[string]float64{
		"Model/A": {"A": 100, "B": 50},
		"Model/B": {"A": 100, "B": 50},
	}, false)

	m.SetHostWeights(map[string]float64{"A": 40, "B": 50}, true)
	m.SetHostWeightsByModel(map[string]map[string]float64{
		"Model/A": {"A": 40, "B": 0},
		"Model/B": {"A": 0, "B": 50},
	}, true)
	m.SetPoCPreserved([]string{"A", "B"})

	require.InDelta(t, 40.0, m.TotalWeightForModel("Model/A"), 1e-9)
	require.InDelta(t, 50.0, m.TotalWeightForModel("Model/B"), 1e-9)
	require.InDelta(t, 150.0, m.BaselineWeightForModel("Model/A"), 1e-9)
	require.InDelta(t, 150.0, m.BaselineWeightForModel("Model/B"), 1e-9)
	require.InDelta(t, 40.0/150.0, m.ScaleFactorForModel("Model/A"), 1e-9)
	require.InDelta(t, 40.0/300.0, m.LimitShareForModel("Model/A"), 1e-9)
	require.InDelta(t, 50.0/300.0, m.LimitShareForModel("Model/B"), 1e-9)
	require.InDelta(t, 90.0/300.0, m.ScaleFactorAcrossModels(), 1e-9)
}

func TestGatewayLimiterModelScalesUseIndependentModelCapacity(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(4, 100), "Model/A")
	g.limiter.UpdateLimits(4, 100, []GatewayModelLimitSettings{
		{ModelID: "Model/A", MaxConcurrentRequests: 8, MaxInputTokensInFlight: 200},
		{ModelID: "Model/B", MaxConcurrentRequests: 2, MaxInputTokensInFlight: 50},
	})
	g.capacity.SetEscrowMembership("A", map[string]int{"host-a": 1})
	g.capacity.SetEscrowMembership("B", map[string]int{"host-b": 1})
	g.capacity.SetHostWeights(map[string]float64{"host-a": 100, "host-b": 100}, false)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Model/A": {"host-a": 100},
		"Model/B": {"host-b": 100},
	}, false)

	scales := g.limiterModelScales([]string{"Model/A", "Model/B"}, map[string]gatewayModelRuntimeStatus{
		"Model/A": {active: 1, routable: 1},
		"Model/B": {active: 1, routable: 1},
	})
	require.InDelta(t, 1.0, scales["Model/A"], 1e-9)
	require.InDelta(t, 1.0, scales["Model/B"], 1e-9)

	snap := g.limiter.SnapshotWithModelScales(scales)
	require.EqualValues(t, 8, snap.Models["Model/A"].EffectiveMaxConcurrent)
	require.EqualValues(t, 2, snap.Models["Model/B"].EffectiveMaxConcurrent)
	require.EqualValues(t, 200, snap.Models["Model/A"].EffectiveMaxInputTokens)
	require.EqualValues(t, 50, snap.Models["Model/B"].EffectiveMaxInputTokens)
	require.EqualValues(t, 8, snap.Models["Model/A"].CapacityCapRequests)
	require.EqualValues(t, 8, snap.Models["Model/A"].CurrentCapacityCapRequests)
	require.EqualValues(t, 200, snap.Models["Model/A"].CapacityCapInputTokens)
	require.EqualValues(t, 200, snap.Models["Model/A"].CurrentCapacityCapInputTokens)
}

func TestCapacityStateScaleFactorClampedTo01(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1})
	m.SetHostWeights(map[string]float64{"A": 1}, false)

	// Boost the current-poll weight 10x while keeping fullWeights at 1
	// (simulating a transient bump during PoC); scale must clamp to 1.
	m.SetHostWeights(map[string]float64{"A": 10}, true)
	require.InDelta(t, 1.0, m.ScaleFactor(), 1e-9, "scale must clamp to 1.0")
}

func TestCapacityStateRemoveEscrowDropsTotals(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1})
	m.SetEscrowMembership("Y", map[string]int{"A": 1})
	m.SetHostWeights(map[string]float64{"A": 4}, false)

	// W(X) = 4 * (1/2) = 2; W(Y) = 4 * (1/2) = 2; W_tot = 4 (A counted once).
	require.InDelta(t, 2.0, m.EscrowWeight("X"), 1e-9)
	require.InDelta(t, 4.0, m.TotalWeight(), 1e-9)

	m.RemoveEscrow("Y")

	// After Y is gone A's only home is X -> share=1.
	require.InDelta(t, 4.0, m.EscrowWeight("X"), 1e-9)
	require.InDelta(t, 0.0, m.EscrowWeight("Y"), 1e-9)
	require.InDelta(t, 4.0, m.TotalWeight(), 1e-9)
}

func TestCapacityStateSnapshotMatchesAccessors(t *testing.T) {
	m := NewCapacityState()
	m.SetEscrowMembership("X", map[string]int{"A": 1})
	m.SetHostWeights(map[string]float64{"A": 2}, false)

	snap := m.Snapshot()
	require.Equal(t, 1, snap.HostCount)
	require.InDelta(t, m.TotalWeight(), snap.TotalWeight, 1e-9)
	require.InDelta(t, m.ScaleFactor(), snap.ScaleFactor, 1e-9)
	require.InDelta(t, 2.0, snap.EscrowWeights["X"], 1e-9)
}

func TestGatewayLimiterApplyScaleFactorClampsToZeroToOne(t *testing.T) {
	l := NewGatewayLimiter(10, 1000)

	l.ApplyScaleFactor(0.5)
	snap := l.Snapshot()
	require.Equal(t, int64(5), snap.EffectiveMaxConcurrent)
	require.Equal(t, int64(500), snap.EffectiveMaxInputTokens)
	require.InDelta(t, 0.5, snap.ScaleFactor, 1e-9)

	// Scale 0 means "no available hosts" -> block all traffic. The
	// caller is responsible for not landing here unless that's really
	// what they want.
	l.ApplyScaleFactor(0)
	snap = l.Snapshot()
	require.Equal(t, int64(0), snap.EffectiveMaxConcurrent)
	require.Equal(t, int64(0), snap.EffectiveMaxInputTokens)

	// A scale > 1 must not let us exceed the configured baseline.
	l.ApplyScaleFactor(5)
	snap = l.Snapshot()
	require.Equal(t, int64(10), snap.EffectiveMaxConcurrent)
	require.Equal(t, int64(1000), snap.EffectiveMaxInputTokens)
	require.InDelta(t, 1.0, snap.ScaleFactor, 1e-9)
}

func TestGatewayLimiterAcquireRespectsScaledCaps(t *testing.T) {
	l := NewGatewayLimiter(4, 0)
	l.ApplyScaleFactor(0.5) // effective max concurrent = 2

	require.NoError(t, l.Acquire(1))
	require.NoError(t, l.Acquire(1))
	require.Error(t, l.Acquire(1), "third request must hit scaled cap")

	l.Release(1)
	require.NoError(t, l.Acquire(1))
}

func TestGatewayLimiterTracksIndependentModelCounters(t *testing.T) {
	l := NewGatewayLimiter(4, 0)

	require.NoError(t, l.AcquireForModel("Model/A", 1, 0.5))
	require.NoError(t, l.AcquireForModel("Model/A", 1, 0.5))
	require.ErrorContains(t, l.AcquireForModel("Model/A", 1, 0.5), "too many concurrent requests")

	require.NoError(t, l.AcquireForModel("Model/B", 1, 0.5))
	require.NoError(t, l.AcquireForModel("Model/B", 1, 0.5))
	require.ErrorContains(t, l.AcquireForModel("Model/B", 1, 0.5), "too many concurrent requests")

	snap := l.Snapshot()
	require.Equal(t, int64(4), snap.InFlightRequests)

	l.ReleaseForModel("Model/A", 1)
	require.NoError(t, l.AcquireForModel("Model/A", 1, 0.5))
	require.ErrorContains(t, l.AcquireForModel("Model/B", 1, 0.5), "too many concurrent requests")
}

func TestGatewayLimiterUsesPerModelConfiguredCaps(t *testing.T) {
	l := NewGatewayLimiter(4, 100)
	l.UpdateLimits(4, 100, []GatewayModelLimitSettings{
		{ModelID: "Model/A", MaxConcurrentRequests: 2, MaxInputTokensInFlight: 20},
		{ModelID: "Model/B", MaxConcurrentRequests: 6, MaxInputTokensInFlight: 60},
	})

	require.NoError(t, l.AcquireForModel("Model/A", 10, 1))
	require.NoError(t, l.AcquireForModel("Model/A", 10, 1))
	require.ErrorContains(t, l.AcquireForModel("Model/A", 1, 1), "too many concurrent requests")

	require.NoError(t, l.AcquireForModel("Model/B", 10, 1))
	require.NoError(t, l.AcquireForModel("Model/B", 10, 1))
	require.NoError(t, l.AcquireForModel("Model/B", 10, 1))
}

func TestGatewayLimiterDerivesModelConcurrencyFromWeight(t *testing.T) {
	l := NewGatewayLimiter(512, 0)
	capacity := LimiterModelCapacity{
		ScaleFactor:                 0.5,
		BaselineWeight:              10_000,
		CurrentWeight:               6_000,
		MaxConcurrentPer10000Weight: 5,
	}

	require.NoError(t, l.AcquireForModelWithCapacity("Model/A", 1, capacity))
	require.NoError(t, l.AcquireForModelWithCapacity("Model/A", 1, capacity))
	require.NoError(t, l.AcquireForModelWithCapacity("Model/A", 1, capacity))
	require.ErrorContains(t, l.AcquireForModelWithCapacity("Model/A", 1, capacity), "too many concurrent requests")

	snap := l.SnapshotWithModelCapacities(map[string]LimiterModelCapacity{"Model/A": capacity})
	require.EqualValues(t, 5, snap.Models["Model/A"].MaxConcurrent)
	require.EqualValues(t, 3, snap.Models["Model/A"].EffectiveMaxConcurrent)
	require.InDelta(t, 6_000.0, snap.Models["Model/A"].CurrentWeight, 1e-9)
	require.InDelta(t, 10_000.0, snap.Models["Model/A"].BaselineWeight, 1e-9)
	require.InDelta(t, 5.0, snap.Models["Model/A"].MaxConcurrentPer10000Weight, 1e-9)
}

func TestGatewaySelectsPoCWeightConcurrencyRate(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(512, 0), "Model/A")
	g.settings = GatewaySettings{
		DefaultRequestMaxTokens:        1024,
		RequestMaxTokensCap:            4096,
		MaxConcurrentPer10000Weight:    5,
		PoCMaxConcurrentPer10000Weight: 10,
		ParticipantThrottle:            DefaultParticipantThrottleSettings(),
		Redundancy:                     DefaultRedundancySettings(),
		Perf:                           PerfSettings{SampleSize: 1, WindowMS: 1},
	}
	g.capacity.SetEscrowMembership("A", map[string]int{"host-a": 1})
	g.capacity.SetHostWeights(map[string]float64{"host-a": 10_000}, false)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Model/A": {"host-a": 10_000},
	}, false)

	regular := g.limiterCapacityForModel("Model/A")
	require.InDelta(t, 5.0, regular.MaxConcurrentPer10000Weight, 1e-9)

	g.phaseGate = &ChainPhaseGate{}
	g.phaseGate.storeSnapshot(ChainPhaseSnapshot{BlockReason: "confirmation_poc"})
	t.Cleanup(func() { setPoCPhaseState(false, "") })
	poc := g.limiterCapacityForModel("Model/A")
	require.InDelta(t, 10.0, poc.MaxConcurrentPer10000Weight, 1e-9)
}

func TestGatewayLimiterAcquireBlocksWhenScaledToZero(t *testing.T) {
	l := NewGatewayLimiter(4, 100)
	l.ApplyScaleFactor(0)

	require.ErrorContains(t, l.Acquire(1), "too many concurrent requests")
}

func TestGatewayLimiterUpdateLimitsPreservesScale(t *testing.T) {
	l := NewGatewayLimiter(10, 100)
	l.ApplyScaleFactor(0.25)

	// New baseline of 20/200 with scale 0.25 -> effective 5/50.
	l.UpdateLimits(20, 200)
	snap := l.Snapshot()
	require.Equal(t, int64(20), snap.MaxConcurrent)
	require.Equal(t, int64(5), snap.EffectiveMaxConcurrent)
	require.Equal(t, int64(200), snap.MaxInputTokens)
	require.Equal(t, int64(50), snap.EffectiveMaxInputTokens)
	require.InDelta(t, 0.25, snap.ScaleFactor, 1e-9)
}

func TestGatewayLimiterUnlimitedBaselinePreservedUnderScale(t *testing.T) {
	l := NewGatewayLimiter(0, 0)
	l.ApplyScaleFactor(0.1)
	snap := l.Snapshot()
	// 0 means unlimited - scaling must NOT turn it into a positive cap.
	require.Equal(t, int64(0), snap.EffectiveMaxConcurrent)
	require.Equal(t, int64(0), snap.EffectiveMaxInputTokens)
}

func TestDevshardRuntimeLoadIsActivePerWeight(t *testing.T) {
	rt := &devshardRuntime{}
	rt.activeRequests.Store(2)
	rt.reservedTokens.Store(3) // reserved tokens no longer factor in.

	require.InDelta(t, 0.5, rt.load(4.0), 1e-9)
	require.InDelta(t, 2.0, rt.load(1.0), 1e-9)
	require.True(t, math.IsInf(rt.load(0), +1))
	require.True(t, math.IsInf(rt.load(-1), +1))
}

func TestReserveRuntimeForModelPrefersHigherWeightEscrow(t *testing.T) {
	hi := &devshardRuntime{id: "hi", model: "M"}
	hi.active.Store(true)
	lo := &devshardRuntime{id: "lo", model: "M"}
	lo.active.Store(true)

	g := NewGateway([]*devshardRuntime{lo, hi}, NewGatewayLimiter(0, 0), "M")
	g.capacity = NewCapacityState()
	g.capacity.SetEscrowMembership("hi", map[string]int{"A": 1})
	g.capacity.SetEscrowMembership("lo", map[string]int{"B": 1})
	g.capacity.SetHostWeights(map[string]float64{"A": 10, "B": 1}, false)

	// Accumulate picks without releasing so load grows. The higher-
	// weight escrow should soak up roughly 10x as much before its load
	// catches up to the low-weight escrow.
	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		rt, err := g.reserveRuntimeForModel("M", 1)
		require.NoError(t, err)
		counts[rt.id]++
	}
	require.Greater(t, counts["hi"], counts["lo"]*5,
		"high-weight escrow should win majority of picks: %v", counts)
}

func TestReserveRuntimeForModelUsesModelSpecificWeights(t *testing.T) {
	hi := &devshardRuntime{id: "hi", model: "Model/A"}
	hi.active.Store(true)
	lo := &devshardRuntime{id: "lo", model: "Model/A"}
	lo.active.Store(true)

	g := NewGateway([]*devshardRuntime{lo, hi}, NewGatewayLimiter(0, 0), "Model/A")
	g.capacity = NewCapacityState()
	g.capacity.SetEscrowMembership("hi", map[string]int{"A": 1})
	g.capacity.SetEscrowMembership("lo", map[string]int{"B": 1})
	g.capacity.SetHostWeights(map[string]float64{"A": 1, "B": 10}, false)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Model/A": {"A": 10, "B": 1},
	}, false)

	counts := map[string]int{}
	for i := 0; i < 100; i++ {
		rt, err := g.reserveRuntimeForModel("Model/A", 1)
		require.NoError(t, err)
		counts[rt.id]++
	}
	require.Greater(t, counts["hi"], counts["lo"]*5,
		"model-specific weight should override the host-level aggregate: %v", counts)
}

func TestReserveRuntimeForModelTreatsZeroWeightEscrowAsLastResort(t *testing.T) {
	healthy := &devshardRuntime{id: "healthy", model: "M"}
	healthy.active.Store(true)
	dead := &devshardRuntime{id: "dead", model: "M"}
	dead.active.Store(true)

	g := NewGateway([]*devshardRuntime{healthy, dead}, NewGatewayLimiter(0, 0), "M")
	g.capacity = NewCapacityState()
	g.capacity.SetEscrowMembership("healthy", map[string]int{"A": 1})
	g.capacity.SetEscrowMembership("dead", map[string]int{"B": 1})
	g.capacity.SetHostWeights(map[string]float64{"A": 1, "B": 1}, false)
	// PoC excludes B entirely -> W(dead) = 0 -> +Inf load -> never picked
	// while the healthy escrow can take traffic.
	g.capacity.SetPoCPreserved([]string{"A"})

	for i := 0; i < 5; i++ {
		rt, err := g.reserveRuntimeForModel("M", 1)
		require.NoError(t, err)
		require.Equal(t, "healthy", rt.id, "iteration %d", i)
		g.releaseRuntime(rt, 1)
	}
}

func TestParticipantLimiterEnforcedUnderCapacityAware(t *testing.T) {
	// With capacity-aware mode ON the relaxed-PoC bypass must NOT
	// short-circuit the reactive limiter; we want scaled caps + bucket
	// throttling to keep working even when PoC is active.
	setPoCModeForTest(t, pocRequestModeRelaxed)
	setPoCPhaseState(true, "poc")
	setCapacityAwareLimitsForTest(t, true)

	limiter := NewParticipantRequestLimiter(1, 1)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", 503)

	// One token is consumed by ObserveResult's 503 backoff; AllowRequest
	// should reject the next attempt because we are NOT bypassing.
	require.Error(t, limiter.AllowRequest("shared-host", "/sessions/12/chat/completions"))
	require.Error(t, limiter.CanAcceptEscrow([]string{"shared-host"}))
}
