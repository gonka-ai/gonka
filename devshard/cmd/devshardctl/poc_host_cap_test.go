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

func testPoCHostCap(active *atomic.Bool, weights map[string]float64, per10000 float64) *pocHostCap {
	c := newPoCHostCap(active.Load, func(_, key string) float64 { return weights[key] })
	c.setPer10000Weight(per10000)
	return c
}

func TestPoCHostCap_Limit(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	c := testPoCHostCap(&active, map[string]float64{"small": 1809, "mid": 3000, "zero": 0}, 10)

	require.EqualValues(t, 1, c.limit("m", "small"), "a share below one request still allows one")
	require.EqualValues(t, 3, c.limit("m", "mid"))
	require.EqualValues(t, 0, c.limit("m", "zero"), "unknown weight is not capped")
	require.EqualValues(t, 0, c.limit("m", "absent"), "unknown host is not capped")
	require.EqualValues(t, 0, c.limit("m", ""))

	c.setPer10000Weight(0)
	require.EqualValues(t, 0, c.limit("m", "mid"), "no concurrency budget configured means no cap")
	c.setPer10000Weight(10)

	active.Store(false)
	require.EqualValues(t, 0, c.limit("m", "mid"), "outside PoC generation nothing is capped")
	require.False(t, c.blocked("m", "mid"))
}

func TestPoCHostCap_ReserveStopsAtLimitAndReleaseReopens(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	c := testPoCHostCap(&active, map[string]float64{"h": 2000}, 10)

	require.True(t, c.reserve("m", "h"))
	require.False(t, c.blocked("m", "h"))
	require.True(t, c.reserve("m", "h"))
	require.True(t, c.blocked("m", "h"))
	require.False(t, c.reserve("m", "h"))
	require.EqualValues(t, 2, c.inFlightFor("h"))

	c.release("h")
	require.False(t, c.blocked("m", "h"))
	require.True(t, c.reserve("m", "h"))

	c.release("h")
	c.release("h")
	c.release("h")
	c.release("never-reserved")
	require.EqualValues(t, 0, c.inFlightFor("h"), "extra releases never drive the count negative")
	require.EqualValues(t, 0, c.inFlightFor("never-reserved"))
}

func TestPoCHostCap_ConcurrentReserveNeverExceedsLimit(t *testing.T) {
	var active atomic.Bool
	active.Store(true)
	c := testPoCHostCap(&active, map[string]float64{"h": 5000}, 10)

	var granted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if c.reserve("m", "h") {
				granted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.EqualValues(t, 5, granted.Load())
	require.EqualValues(t, 5, c.inFlightFor("h"))
}

type blockedGate struct{ released atomic.Int64 }

func (*blockedGate) blocked(string, string) bool { return true }
func (*blockedGate) reserve(string, string) bool { return false }
func (g *blockedGate) release(string)            { g.released.Add(1) }

// A request whose only viable participant is at its cap is dropped with
// ErrNoAvailableHost instead of burning nonces until the host frees up.
func TestPoCHostCap_PickerDropsRequestWhenOnlyParticipantIsCapped(t *testing.T) {
	p, session, ghost := pickerEnv(t)
	p.setHostCap(&blockedGate{})

	req := defaultPickerRequest()
	req.excludeParticipants = map[string]bool{
		session.HostParticipantKey(0): true,
		session.HostParticipantKey(1): true,
	}
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.ErrorIs(t, res.err, ErrNoAvailableHost)
	require.Zero(t, ghost.total(), "the queue empties without burning nonces")
}

func TestPoCHostCap_PickerReservesSlotForRealDispatch(t *testing.T) {
	p, session, _ := pickerEnv(t)
	var active atomic.Bool // PoC generation off: counted but never blocked
	c := testPoCHostCap(&active, nil, 10)
	p.setHostCap(c)

	req := defaultPickerRequest()
	p.submit(req)
	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.Equal(t, session.HostParticipantKey(res.prepared.HostIdx()), res.hostCapKey)
	require.EqualValues(t, 1, c.inFlightFor(res.hostCapKey))

	p.releaseHostCap(res.hostCapKey)
	require.EqualValues(t, 0, c.inFlightFor(res.hostCapKey))
}

func gatewayWithHostCap(t *testing.T, env *testProxyEnv) *Gateway {
	t.Helper()
	rt := &devshardRuntime{id: "escrow-proxy", model: "llama", proxy: env.proxy, session: env.session}
	g := NewGateway([]*devshardRuntime{rt}, nil, "llama")
	g.attachRuntimeSharedState(rt)
	return g
}

func totalHostCapInFlight(c *pocHostCap) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var sum int64
	for _, n := range c.inFlight {
		sum += n
	}
	return sum
}

func requireHostCapDrains(t *testing.T, c *pocHostCap) {
	t.Helper()
	require.Eventually(t, func() bool { return totalHostCapInFlight(c) == 0 }, 5*time.Second, 10*time.Millisecond,
		"every reserved slot must be released once the attempts end")
}

func TestPoCHostCap_CounterIsExactOnEveryTerminalPath(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		env := setupTestProxy(t, 3, nil, true)
		g := gatewayWithHostCap(t, env)
		var buf bytes.Buffer
		require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil))
		requireHostCapDrains(t, g.pocCap)
	})

	t.Run("error", func(t *testing.T) {
		env := setupTestProxy(t, 3, nil, true)
		g := gatewayWithHostCap(t, env)
		for _, kc := range env.killables {
			kc.killed.Store(true)
		}
		var buf bytes.Buffer
		require.Error(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil))
		requireHostCapDrains(t, g.pocCap)
	})

	t.Run("cancel", func(t *testing.T) {
		engines := make([]devshard.InferenceEngine, 3)
		for i := range engines {
			engines[i] = &slowFirstTokenEngine{inner: stub.NewInferenceEngine(), delay: time.Second}
		}
		env := setupTestProxy(t, 3, engines, true)
		g := gatewayWithHostCap(t, env)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			var buf bytes.Buffer
			done <- env.proxy.redundancy.RunInference(ctx, defaultParams(), &buf, nil)
		}()
		require.Eventually(t, func() bool { return totalHostCapInFlight(g.pocCap) > 0 }, 5*time.Second, 5*time.Millisecond)
		cancel()
		require.Error(t, <-done)
		requireHostCapDrains(t, g.pocCap)
	})

	t.Run("race loser", func(t *testing.T) {
		withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
		setSpeculativeTiming(t, 50*time.Millisecond, 2*time.Second, 0, 50*time.Millisecond)
		engines := make([]devshard.InferenceEngine, 3)
		for i := range engines {
			engines[i] = &slowFirstTokenEngine{inner: stub.NewInferenceEngine(), delay: 300 * time.Millisecond}
		}
		env := setupTestProxy(t, 3, engines, true)
		g := gatewayWithHostCap(t, env)
		var payloads atomic.Int32
		for _, kc := range env.killables {
			kc.inner = &lateReceiptThenHangClient{inner: kc.inner, delay: 80 * time.Millisecond, payloads: &payloads}
		}
		params := defaultParams()
		seedFirstTokenFallbackDelay(t, env.proxy.redundancy.perf, params.Model, params.InputLength, time.Second)

		var buf bytes.Buffer
		require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil))
		require.GreaterOrEqual(t, payloads.Load(), int32(2), "secondaries were started and lost the race")
		requireHostCapDrains(t, g.pocCap)
	})
}

// With the cap off (not PoC generation) routing is unchanged: a request is
// served and the slot is still counted so a later PoC window starts exact.
func TestPoCHostCap_NoCapOutsidePoCGeneration(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	g := gatewayWithHostCap(t, env)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{"llama": {env.session.HostParticipantKey(0): 1}}, false)

	for i := 0; i < 4; i++ {
		var buf bytes.Buffer
		err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)
		require.NoError(t, err)
	}
	requireHostCapDrains(t, g.pocCap)
}
