package main

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/stub"
)

// concurrencyEngine answers like the stub after a fixed latency and records
// the highest number of requests it served at once.
type concurrencyEngine struct {
	answer  *stub.InferenceEngine
	latency time.Duration
	current atomic.Int64
	peak    atomic.Int64
}

func (e *concurrencyEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	now := e.current.Add(1)
	defer e.current.Add(-1)
	for {
		peak := e.peak.Load()
		if now <= peak || e.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	select {
	case <-time.After(e.latency):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.answer.Execute(ctx, req)
}

// A 16-slot escrow where only one participant is preserved for PoC: every real
// request lands on that participant, so a burst must not exceed its share of
// the PoC concurrency budget.
func TestPoCHostCap_BurstOnLoneSmallPreservedHostStaysWithinCap(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	t.Cleanup(func() { setPoCPreservedParticipantsByModel(nil) })

	const slots = 16
	engines := make([]devshard.InferenceEngine, slots)
	tracked := make([]*concurrencyEngine, slots)
	for i := range engines {
		tracked[i] = &concurrencyEngine{answer: stub.NewInferenceEngine(), latency: 300 * time.Millisecond}
		engines[i] = tracked[i]
	}
	env := setupTestProxy(t, slots, engines, true)

	rt := &devshardRuntime{id: "escrow-proxy", model: "llama", proxy: env.proxy, session: env.session}
	g := NewGateway([]*devshardRuntime{rt}, nil, "llama")
	snapshot := ChainPhaseSnapshot{EpochPhase: epochPhasePoCGenerate, BlockReason: "poc"}
	g.phaseGate = &ChainPhaseGate{}
	g.phaseGate.storeSnapshot(snapshot)
	g.attachRuntimeSharedState(rt)
	setPoCPhaseStateFromSnapshot(snapshot)

	// Weight 3000 at the default 10 per 10000 weight allows 3 requests at once.
	const wantCap = 3
	preservedKey := env.session.HostParticipantKey(0)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{"llama": {preservedKey: 3000}}, true)
	setPoCPreservedParticipantsByModel(map[string][]string{"llama": {preservedKey}})

	const burst = 12
	var wg sync.WaitGroup
	var served atomic.Int64
	for i := 0; i < burst; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var buf bytes.Buffer
			if err := env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil); err == nil {
				served.Add(1)
			}
		}()
	}
	wg.Wait()

	require.Positive(t, served.Load(), "the preserved host should still serve requests")
	for i := 1; i < slots; i++ {
		require.Zero(t, tracked[i].peak.Load(), "slot %d is not preserved and must not serve real requests", i)
	}
	require.LessOrEqual(t, tracked[0].peak.Load(), int64(wantCap),
		"concurrent real requests on the lone preserved host exceeded its PoC cap")
}
