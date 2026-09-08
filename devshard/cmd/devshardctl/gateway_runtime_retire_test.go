package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
)

// newRetireTestGateway builds a minimal Gateway holding a single active runtime
// registered in both the lookup map and the ordered slice, mirroring how the
// real registry is populated.
func newRetireTestGateway(id string) (*Gateway, *devshardRuntime) {
	rt := &devshardRuntime{id: id}
	rt.active.Store(true)
	g := &Gateway{
		runtimes:         map[string]*devshardRuntime{id: rt},
		runtimeOrder:     []*devshardRuntime{rt},
		rotationBreakers: make(map[string]*rotationBreaker),
	}
	return g, rt
}

// TestRetireRuntimeRemovesRuntimeFromRegistry pins the core leak fix: retiring a
// runtime must drop it from both g.runtimes and g.runtimeOrder so its
// user.Session (and the per-runtime SQLite handles it owns) can be released.
func TestRetireRuntimeRemovesRuntimeFromRegistry(t *testing.T) {
	g, _ := newRetireTestGateway("12")

	require.True(t, g.retireRuntime("12", "test"))

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "runtime must be removed from g.runtimes")
	require.Empty(t, g.runtimeOrder, "runtime must be removed from g.runtimeOrder")

	// Idempotent: retiring an already-gone runtime is a no-op, not a panic.
	require.False(t, g.retireRuntime("12", "test"))
}

func TestPooledStatusAfterLastRuntimeRetiredIsNotFound(t *testing.T) {
	rt := &devshardRuntime{id: "12"}
	rt.active.Store(true)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")
	require.True(t, g.retireRuntime("12", "escrow confirmed settled on chain"))

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handlePooledStatus(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "gateway", body["mode"])
	require.Equal(t, float64(0), body["runtimes"])
	require.Equal(t, "not_found", body["phase"])
	require.NotContains(t, body, "escrow_id")
	errObj, ok := body["error"].(map[string]any)
	require.True(t, ok, "retired status must include error: %v", body)
	require.Equal(t, "not_found", errObj["type"])
	require.Equal(t, "no active escrow", errObj["message"])
}

// TestRetireRuntimeDefersWhileRequestsInFlight guards against closing a SQLite
// store out from under an in-flight request: retirement must defer (and leave
// the runtime registered) until the request count drains to zero.
func TestRetireRuntimeDefersWhileRequestsInFlight(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	rt.activeUserRequests.Store(1)

	require.False(t, g.retireRuntime("12", "busy"))
	_, stillRegistered := g.runtimes["12"]
	require.True(t, stillRegistered, "busy runtime must stay registered")
	require.True(t, rt.retirePending.Load(), "deferred retire must record its intent")
	require.Equal(t, "busy", rt.retireReason)

	rt.activeUserRequests.Store(0)
	require.True(t, g.retireRuntime("12", "drained"))
	_, stillRegistered = g.runtimes["12"]
	require.False(t, stillRegistered)
}

// TestReleaseRuntimeRetiresAfterDrain: a retire deferred while busy fires once
// the last request drains through releaseRuntime.
func TestReleaseRuntimeRetiresAfterDrain(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	rt.activeUserRequests.Store(1)

	require.False(t, g.retireRuntime("12", "balance exhausted"))
	_, stillRegistered := g.runtimes["12"]
	require.True(t, stillRegistered, "busy runtime must stay registered")

	g.releaseRuntime(rt, 0)

	_, stillRegistered = g.runtimes["12"]
	require.False(t, stillRegistered, "drained runtime must be retired by releaseRuntime")
	require.Empty(t, g.runtimeOrder)
}

// TestReleaseRuntimeRetiresWithOnlyRetirePending exercises the retire branch in
// isolation: only retirePending set (no settlement), drain → retire.
func TestReleaseRuntimeRetiresWithOnlyRetirePending(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	rt.activeUserRequests.Store(1)
	rt.retireReason = "balance exhausted"
	rt.retirePending.Store(true)

	g.releaseRuntime(rt, 0)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "retire branch must fire on drain")
	require.Empty(t, g.runtimeOrder)
}

// TestReleaseRuntimeDefersWhileRequestsRemain: while remaining != 0 nothing
// fires; settled stays 0 until the last request drains. Uses settlementPending
// because scheduleAutoSettlement fires regardless of the live count, so a
// broken guard leaks as settled>0 (retire would self-defer and hide it).
func TestReleaseRuntimeDefersWhileRequestsRemain(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	g.reserveRuntime(rt, 1)
	g.reserveRuntime(rt, 1)
	rt.settlementReason = "low_balance"
	rt.settlementPending.Store(true)

	g.releaseRuntime(rt, 1) // remaining == 1 → quiet
	require.Never(t, func() bool { return settled.Load() > 0 }, 200*time.Millisecond, 20*time.Millisecond)

	g.releaseRuntime(rt, 1) // remaining == 0 → settles once
	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond)
}

// TestRetireRotatedDevshardRetiresWithoutSettlement covers the no-settle
// terminal path: when settlement is disabled, the rotated-out runtime is
// deactivated AND retired in the same step.
func TestRetireRotatedDevshardRetiresWithoutSettlement(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	settings := GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: false}}

	settled, err := g.retireRotatedDevshard(context.Background(), "12", "rotated", settings)
	require.NoError(t, err)
	require.False(t, settled)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "no-settle rotation must retire the runtime")
}

func TestRetireRotatedDevshardRetiresWhenAlreadySettled(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	settings := GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: true}}

	oldSettle := gatewaySettleDevshardOnChain
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		return nil, fmt.Errorf("rehydrate devshard %s for settlement: runtime %s: %w", id, id, bridge.ErrEscrowSettled)
	}
	t.Cleanup(func() { gatewaySettleDevshardOnChain = oldSettle })

	settled, err := g.retireRotatedDevshard(context.Background(), "12", "rotated", settings)
	require.NoError(t, err)
	require.True(t, settled)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "already-settled rotation must retire the runtime")
}

// TestRetireRotatedDevshardRetiresAfterSettlement covers the settle terminal
// path: the runtime stays alive through settlement (which reads its session)
// and is retired only once settlement succeeds.
func TestRetireRotatedDevshardRetiresAfterSettlement(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	settings := GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: true}}

	oldSettle := gatewaySettleDevshardOnChain
	gatewaySettleDevshardOnChain = func(g *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		// The session must still be reachable at settlement time.
		_, ok := g.runtimes[id]
		require.True(t, ok, "runtime must still be registered during settlement")
		return &SettleDevshardEscrowResult{TxHash: "OK"}, nil
	}
	t.Cleanup(func() { gatewaySettleDevshardOnChain = oldSettle })

	settled, err := g.retireRotatedDevshard(context.Background(), "12", "rotated", settings)
	require.NoError(t, err)
	require.True(t, settled)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "settled rotation must retire the runtime")
}

type settleCheckBridge struct {
	bridge.MainnetBridge
	info *bridge.EscrowInfo
	err  error
}

func (b *settleCheckBridge) GetEscrow(string) (*bridge.EscrowInfo, error) {
	if b.err != nil {
		return nil, b.err
	}
	return b.info, nil
}

func stubChainBridge(t *testing.T, br bridge.MainnetBridge) {
	t.Helper()
	old := gatewayChainBridge
	gatewayChainBridge = func(*Gateway) bridge.MainnetBridge { return br }
	t.Cleanup(func() { gatewayChainBridge = old })
}

// A resident runtime never goes through the rehydrate path, so an escrow the
// chain already settled surfaces only as the chain's untyped "already settled"
// rejection. Without reclassifying it the auto-settle loop burns all 30
// attempts against a tx that can never succeed.
func TestSettleTerminalErrClassifiesAlreadySettled(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	stubChainBridge(t, &settleCheckBridge{info: &bridge.EscrowInfo{EscrowID: "12", Settled: true}})

	cause := fmt.Errorf("tx failed: escrow 12 already settled")
	err := g.settleTerminalErr("12", cause)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)
	require.Contains(t, err.Error(), "already settled")
}

func TestSettleTerminalErrKeepsRetryableCause(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	stubChainBridge(t, &settleCheckBridge{info: &bridge.EscrowInfo{EscrowID: "12"}})

	cause := errors.New("broadcast timeout")
	err := g.settleTerminalErr("12", cause)
	require.ErrorIs(t, err, cause)
	require.NotErrorIs(t, err, bridge.ErrEscrowSettled, "an open escrow must stay retryable")
}

// When the chain cannot answer we must not invent a terminal state: the
// original error stays, so the caller keeps retrying.
func TestSettleTerminalErrKeepsCauseWhenChainUnreachable(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	stubChainBridge(t, &settleCheckBridge{err: errors.New("chain down")})

	cause := errors.New("broadcast timeout")
	require.ErrorIs(t, g.settleTerminalErr("12", cause), cause)
}

// The auto-settle terminal branch must persist the deactivation, otherwise the
// stored row stays Active for an escrow the chain considers finished.
func TestScheduleAutoSettlementPersistsDeactivationWhenAlreadySettled(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:    "http://node:1317",
		PublicAPI:    "http://api:9000",
		DefaultModel: "Qwen/Test",
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret"}, Active: true},
	}))

	g, _ := newRetireTestGateway("12")
	g.store = store

	oldSettle := gatewaySettleDevshardOnChain
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		return nil, fmt.Errorf("settle %s: %w", id, bridge.ErrEscrowSettled)
	}
	t.Cleanup(func() { gatewaySettleDevshardOnChain = oldSettle })

	g.scheduleAutoSettlement("12", "test")

	require.Eventually(t, func() bool {
		state, ok, err := store.LoadState()
		if err != nil || !ok || len(state.Devshards) == 0 {
			return false
		}
		return !state.Devshards[0].Active
	}, 5*time.Second, 20*time.Millisecond, "already-settled escrow must be persisted inactive")
}
