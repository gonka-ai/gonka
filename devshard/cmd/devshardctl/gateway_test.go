package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"

	_ "modernc.org/sqlite"
)

func gatewayTestStateMachineInPhase(t *testing.T, phase types.SessionPhase) *state.StateMachine {
	t.Helper()

	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	userKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-gateway-test", testutil.DefaultConfig(len(hosts)), group, 1_000_000, userKey.Address(), verifier)
	require.NoError(t, err)

	if phase == types.PhaseActive {
		return sm
	}

	diff := testutil.SignDiff(t, userKey, "escrow-gateway-test", 1, []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}},
	})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	if phase == types.PhaseSettlement {
		for nonce := uint64(2); nonce <= uint64(len(hosts)+1); nonce++ {
			diff = testutil.SignDiff(t, userKey, "escrow-gateway-test", nonce, []*types.DevshardTx{})
			_, err = sm.ApplyDiff(diff)
			require.NoError(t, err)
		}
	}
	require.Equal(t, phase, sm.Phase())
	return sm
}

func gatewayTestRuntimeForLimits(t *testing.T, id string, balance, nonce uint64) *devshardRuntime {
	t.Helper()

	sm := gatewayTestStateMachineInPhase(t, types.PhaseActive)
	st := sm.ExportState()
	st.Balance = balance
	st.LatestNonce = nonce
	sm.RestoreState(st)

	return &devshardRuntime{
		id:    id,
		model: "m",
		proxy: &Proxy{sm: sm},
	}
}

func gatewayTestDepletionGateway(t *testing.T, rt *devshardRuntime, modifySettings ...func(*GatewaySettings)) (*Gateway, *atomic.Int32, *atomic.Int32) {
	t.Helper()

	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "m",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		EscrowRotation: EscrowRotationSettings{
			Enabled:           true,
			SettlementEnabled: true,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "m",
				TempCount:     1,
				TargetCount:   2,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	for _, modify := range modifySettings {
		modify(&settings)
	}
	require.NoError(t, store.Initialize(settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: rt.id, PrivateKeyHex: "secret", Model: rt.model},
		Active:        true,
		RotationRole:  rotationRoleRegular,
		RotationEpoch: 1,
	}}))

	var created atomic.Int32
	var settled atomic.Int32
	oldCreate := gatewayCreateDepletionEscrow
	oldSettle := gatewaySettleDevshardOnChain
	gatewayCreateDepletionEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, model EscrowRotationModelSettings, role string, _ uint64) (*CreateDevshardEscrowResult, error) {
		require.Equal(t, "m", model.ModelID)
		require.Equal(t, rotationRoleRegular, role)
		created.Add(1)
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	gatewaySettleDevshardOnChain = func(_ *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		require.Equal(t, rt.id, id)
		settled.Add(1)
		return &SettleDevshardEscrowResult{EscrowID: mustParseUintForTest(t, id), TxHash: "SETTLED", Settler: "settler"}, nil
	}
	t.Cleanup(func() {
		gatewayCreateDepletionEscrow = oldCreate
		gatewaySettleDevshardOnChain = oldSettle
	})

	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")
	g.store = store
	g.settings = settings
	return g, &created, &settled
}

func mustParseUintForTest(t *testing.T, value string) uint64 {
	t.Helper()
	parsed, err := strconv.ParseUint(value, 10, 64)
	require.NoError(t, err)
	return parsed
}

func TestGatewayCheckBalancesReplacesAndDeactivatesLowBalance(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, created, settled := gatewayTestDepletionGateway(t, rt)

	g.checkBalances()

	require.Eventually(t, func() bool {
		return created.Load() == 1 && settled.Load() == 1 && !rt.active.Load()
	}, time.Second, 10*time.Millisecond)
}

func TestGatewayCheckBalancesReplacesAndDeactivatesHighNonce(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit)
	g, created, settled := gatewayTestDepletionGateway(t, rt)

	g.checkBalances()

	require.Eventually(t, func() bool {
		return created.Load() == 1 && settled.Load() == 1 && !rt.active.Load()
	}, time.Second, 10*time.Millisecond)
}

func TestGatewayCheckBalancesNoOpWhenRotationDisabled(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit)
	g, created, settled := gatewayTestDepletionGateway(t, rt, func(settings *GatewaySettings) {
		settings.EscrowRotation.Enabled = false
	})

	g.checkBalances()

	require.EqualValues(t, 0, created.Load())
	require.EqualValues(t, 0, settled.Load())
	require.True(t, rt.active.Load())
}

func TestGatewayCheckBalancesReplacesAndDeactivatesWithoutSettlement(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, created, settled := gatewayTestDepletionGateway(t, rt, func(settings *GatewaySettings) {
		settings.EscrowRotation.SettlementEnabled = false
	})

	g.checkBalances()

	require.Eventually(t, func() bool {
		return created.Load() == 1 && !rt.active.Load()
	}, time.Second, 10*time.Millisecond)
	require.EqualValues(t, 0, settled.Load())
}

func TestGatewayBalanceExhaustedDeactivatesWhenRotationDisabled(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit-1)
	rt.proxy.redundancy = &Redundancy{}
	g, created, settled := gatewayTestDepletionGateway(t, rt, func(settings *GatewaySettings) {
		settings.EscrowRotation.Enabled = false
	})
	g.attachEscrowChecker(rt)

	rt.proxy.redundancy.onBalanceExhausted()

	require.EqualValues(t, 0, created.Load())
	require.EqualValues(t, 0, settled.Load())
	require.False(t, rt.active.Load())
	state, ok, err := g.store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, gatewayDevshardsByID(state.Devshards)["12"].Active)
}

func TestGatewayCheckBalancesKeepsRuntimeBelowLimits(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit-1)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")

	g.checkBalances()

	require.True(t, rt.active.Load())
}

func TestParseDevshardPath(t *testing.T) {
	id, inner, ok := parseDevshardPath("/devshard/12/v1/debug/perf")
	require.True(t, ok)
	require.Equal(t, "12", id)
	require.Equal(t, "/v1/debug/perf", inner)

	_, _, ok = parseDevshardPath("/v1/status")
	require.False(t, ok)
}

func TestGatewayChooseRuntimeUsesLowestLoad(t *testing.T) {
	// Load score is activeRequests / W(e). Both runtimes share W(e)=1
	// (no capacity model wired). The picker should prefer the runtime
	// with fewer in-flight requests.
	a := &devshardRuntime{id: "6", model: "m"}
	b := &devshardRuntime{id: "12", model: "m"}
	a.activeRequests.Store(5)
	b.activeRequests.Store(1)

	g := NewGateway([]*devshardRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
	require.EqualValues(t, 2, chosen.activeRequests.Load())
	require.EqualValues(t, 5, chosen.reservedTokens.Load())
}

func TestGatewayHandleDevshardRewritesInnerPath(t *testing.T) {
	var seenPath string
	rt := &devshardRuntime{
		id:    "12",
		model: "m",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")

	req := httptest.NewRequest(http.MethodGet, "/devshard/12/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "/v1/status", seenPath)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
}

func TestGatewayDisabledStateReturnsRedirectPayload(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1000), "Qwen/Test")
	g.settings = GatewaySettings{
		DefaultModel: "Qwen/Test",
		Disabled: GatewayDisabledSettings{
			Enabled: true,
			Message: "please use https://.../v1/ base url",
			NewURL:  "https://.../v1/chat/completions",
		},
	}.WithTuningDefaults()
	handler := buildGatewayHandler(g, runtimeOptions{apiKeys: map[string]struct{}{"secret": {}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusPermanentRedirect, rec.Code)
	require.JSONEq(t, `{"status":308,"message":"please use https://.../v1/ base url","new_url":"https://.../v1/chat/completions"}`, rec.Body.String())
	require.False(t, forwarded)
}

func TestGatewayModelAccessAdminOnlyDeniesPooledAndDirectChat(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	other := &devshardRuntime{
		id:                    "13",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-q": 1},
	}
	g := NewGateway([]*devshardRuntime{rt, other}, NewGatewayLimiter(10, 1000), "Kimi/Test")
	g.settings = GatewaySettings{
		DefaultModel: "Kimi/Test",
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:       "Kimi/Test",
			AccessMode:    "admin_only",
			AccessMessage: "Kimi is temporarily unavailable",
		}},
	}.WithTuningDefaults()
	g.capacity.SetEscrowMembership("12", map[string]int{"host-k": 1})
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Kimi/Test": {"host-k": 50},
	}, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "Kimi is temporarily unavailable")
	require.False(t, forwarded)

	req = httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec = httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "Kimi is temporarily unavailable")
	require.False(t, forwarded)

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusRec := httptest.NewRecorder()
	g.handlePooledStatus(statusRec, statusReq)

	require.Equal(t, http.StatusOK, statusRec.Code)
	var body struct {
		Limiter  LimiterSnapshot       `json:"limiter"`
		Capacity gatewayCapacityStatus `json:"capacity"`
	}
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &body))
	require.InDelta(t, 50.0, body.Capacity.Models["Kimi/Test"].CurrentWeight, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.Models["Kimi/Test"].FullWeight, 1e-9)
	require.InDelta(t, 1.0, body.Capacity.Models["Kimi/Test"].ScaleFactor, 1e-9)
	require.True(t, body.Capacity.Models["Kimi/Test"].AccessEnabled)
	require.Equal(t, "admin_only", body.Capacity.Models["Kimi/Test"].AccessMode)
	require.Equal(t, "Kimi is temporarily unavailable", body.Capacity.Models["Kimi/Test"].AccessMessage)
	require.True(t, body.Capacity.Models["Kimi/Test"].Routable)
	require.Equal(t, 1, body.Capacity.Models["Kimi/Test"].ActiveDevshards)
	require.Equal(t, 1, body.Capacity.Models["Kimi/Test"].RoutableDevshards)
}

func TestGatewayModelAccessAdminOnlyAllowsAdminAuthenticatedInference(t *testing.T) {
	var forwarded int
	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1000), "Kimi/Test")
	g.settings = GatewaySettings{
		DefaultModel: "Kimi/Test",
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:       "Kimi/Test",
			AccessMode:    "admin_only",
			AccessMessage: "Kimi is temporarily unavailable",
		}},
	}.WithTuningDefaults()
	g.capacity.SetEscrowMembership("12", map[string]int{"host-k": 1})
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Kimi/Test": {"host-k": 50},
	}, false)

	handler := buildGatewayHandler(g, runtimeOptions{
		apiKeys:     map[string]struct{}{"user-key": {}},
		adminAPIKey: "admin-key",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer user-key")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "Kimi is temporarily unavailable")
	require.Equal(t, 0, forwarded)

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer admin-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, forwarded)

	req = httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer admin-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 2, forwarded)
}

func TestGatewayModelAccessAPIKeyAllowsClientAuthenticatedInference(t *testing.T) {
	var forwarded int
	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1000), "Kimi/Test")
	g.settings = GatewaySettings{
		DefaultModel: "Kimi/Test",
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:    "Kimi/Test",
			AccessMode: "api_key",
		}},
	}.WithTuningDefaults()

	handler := buildGatewayHandler(g, runtimeOptions{
		apiKeys:     map[string]struct{}{"user-key": {}},
		adminAPIKey: "admin-key",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "requires an API key")
	require.Equal(t, 0, forwarded)

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer user-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, forwarded)
}

func TestGatewayAPIKeyLogFieldsUsesLastEightCharacters(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(0, 0), "Kimi/Test")
	g.apiKeys = map[string]struct{}{"client-key-12345678": {}}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer client-key-12345678")
	require.Equal(t, []any{"api_key_suffix", "12345678", "api_key_kind", "api"}, g.apiKeyLogFields(req))

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer unknown-key-87654321")
	require.Equal(t, []any{"api_key_suffix", "87654321", "api_key_kind", "unknown"}, g.apiKeyLogFields(req))

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	handler := adminAuthMiddleware("admin-key-abcdefgh", http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		require.Equal(t, []any{"api_key_suffix", "abcdefgh", "api_key_kind", "admin"}, g.apiKeyLogFields(r))
	}))
	req.Header.Set("Authorization", "Bearer admin-key-abcdefgh")
	handler.ServeHTTP(httptest.NewRecorder(), req)
}

func TestGatewayModelAccessDefaultsToAdminOnly(t *testing.T) {
	var forwarded int
	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	other := &devshardRuntime{
		id:                    "13",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-q": 1},
	}
	g := NewGateway([]*devshardRuntime{rt, other}, NewGatewayLimiter(10, 1000), "Kimi/Test")
	handler := buildGatewayHandler(g, runtimeOptions{adminAPIKey: "admin-key"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.Contains(t, rec.Body.String(), "requires an admin API key")
	require.Equal(t, 0, forwarded)

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer admin-key")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, forwarded)

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusRec := httptest.NewRecorder()
	g.handlePooledStatus(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code)
	var body struct {
		Capacity gatewayCapacityStatus `json:"capacity"`
	}
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &body))
	require.Equal(t, "admin_only", body.Capacity.Models["Kimi/Test"].AccessMode)
}

func TestGatewayModelAccessOpenAllowsUnauthenticatedInference(t *testing.T) {
	var forwarded int
	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded++
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1000), "Kimi/Test")
	g.settings = GatewaySettings{
		DefaultModel: "Kimi/Test",
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:    "Kimi/Test",
			AccessMode: "open",
		}},
	}.WithTuningDefaults()
	handler := buildGatewayHandler(g, runtimeOptions{adminAPIKey: "admin-key"})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, 1, forwarded)
}

func TestGatewayModelsEndpointListsActiveModels(t *testing.T) {
	qwenA := &devshardRuntime{id: "12", model: "Qwen/Test"}
	qwenB := &devshardRuntime{id: "13", model: "Qwen/Test"}
	kimi := &devshardRuntime{id: "14", model: "Kimi/Test"}
	inactive := &devshardRuntime{id: "15", model: "Inactive/Test"}
	g := NewGateway([]*devshardRuntime{qwenA, qwenB, kimi, inactive}, NewGatewayLimiter(0, 0), "Qwen/Test")
	inactive.active.Store(false)
	g.settings.RequestMaxTokensCap = 8192

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Object string `json:"object"`
		Data   []struct {
			ID                  string `json:"id"`
			Object              string `json:"object"`
			OwnedBy             string `json:"owned_by"`
			Name                string `json:"name"`
			ContextLength       uint64 `json:"context_length"`
			MaxCompletionTokens uint64 `json:"max_completion_tokens"`
			Architecture        struct {
				Modality         string   `json:"modality"`
				InputModalities  []string `json:"input_modalities"`
				OutputModalities []string `json:"output_modalities"`
			} `json:"architecture"`
			Pricing struct {
				Prompt     string `json:"prompt"`
				Completion string `json:"completion"`
				Request    string `json:"request"`
			} `json:"pricing"`
			TopProvider struct {
				ContextLength       uint64 `json:"context_length"`
				MaxCompletionTokens uint64 `json:"max_completion_tokens"`
				IsModerated         bool   `json:"is_moderated"`
			} `json:"top_provider"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, 2)
	require.Equal(t, "Qwen/Test", resp.Data[0].ID)
	require.Equal(t, "Kimi/Test", resp.Data[1].ID)
	require.Equal(t, "model", resp.Data[0].Object)
	require.Equal(t, "gonka", resp.Data[0].OwnedBy)
	require.Equal(t, resp.Data[0].ID, resp.Data[0].Name)
	require.EqualValues(t, 8192, resp.Data[0].ContextLength)
	require.EqualValues(t, 8192, resp.Data[0].MaxCompletionTokens)
	require.Equal(t, "text->text", resp.Data[0].Architecture.Modality)
	require.Equal(t, []string{"text"}, resp.Data[0].Architecture.InputModalities)
	require.Equal(t, []string{"text"}, resp.Data[0].Architecture.OutputModalities)
	require.Equal(t, "0", resp.Data[0].Pricing.Prompt)
	require.Equal(t, "0", resp.Data[0].Pricing.Completion)
	require.Equal(t, "0", resp.Data[0].Pricing.Request)
	require.EqualValues(t, 8192, resp.Data[0].TopProvider.ContextLength)
	require.EqualValues(t, 8192, resp.Data[0].TopProvider.MaxCompletionTokens)
	require.False(t, resp.Data[0].TopProvider.IsModerated)
	require.Contains(t, resp.Data[0].SupportedParameters, "stream")
}

func TestRuntimeModelsEndpointListsSingleModel(t *testing.T) {
	handler := newRuntimeMux(&Proxy{model: "Qwen/Test"})

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"object": "list"`)
	require.Contains(t, rec.Body.String(), `"id": "Qwen/Test"`)
}

func TestGatewayModelsEndpointRejectsUnsupportedMethod(t *testing.T) {
	rt := &devshardRuntime{id: "12", model: "Qwen/Test"}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	req := httptest.NewRequest(http.MethodPost, "/v1/models", nil)
	rec := httptest.NewRecorder()
	g.Handler().ServeHTTP(rec, req)

	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
	require.Equal(t, "GET, HEAD", rec.Header().Get("Allow"))
}

func TestNewRESTBridgeForProtocolUsesDevshardEscrowEndpointByDefault(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{
				"id":          "83",
				"creator":     "gonka1creator",
				"amount":      "5000000000",
				"slots":       []string{"gonka1host"},
				"epoch_index": "1",
				"app_hash":    "deadbeef",
				"settled":     false,
			},
			"found": true,
		})
	}))
	t.Cleanup(srv.Close)

	_, err := newRESTBridgeForProtocol(srv.URL, types.ProtocolV1).GetEscrow("83")
	require.NoError(t, err)
	require.Equal(t, "/productscience/inference/inference/devshard_escrow/83", gotPath)
}

func TestAdminDeactivateDevshardAllowsActiveRequestsAndStopsNewChat(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	rt.activeRequests.Store(1)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.store = store

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/12/deactivate", nil)
	rec := httptest.NewRecorder()
	g.handleAdminDeactivateDevshard(rec, req, "12")

	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, rt.active.Load())
	require.EqualValues(t, 1, rt.activeRequests.Load())

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, state.Devshards[0].Active)

	chatReq := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	chatRec := httptest.NewRecorder()
	g.handleDevshard(chatRec, chatReq)

	require.Equal(t, http.StatusConflict, chatRec.Code)
	require.False(t, forwarded)
}

func TestAdminDevshardParticipantsShowsQuarantineState(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResult("dead-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rt := &devshardRuntime{
		id:                    "12",
		model:                 "Qwen/Test",
		participantKeys:       []string{"healthy-host", "dead-host"},
		participantSlotCounts: map[string]int{"healthy-host": 1, "dead-host": 2},
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.participantLimiter = limiter

	req := httptest.NewRequest(http.MethodGet, "/v1/admin/devshards/12/participants", nil)
	rec := httptest.NewRecorder()
	g.handleAdminDevshardAction(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		ID               string `json:"id"`
		Model            string `json:"model"`
		ParticipantCount int    `json:"participant_count"`
		AvailableCount   int    `json:"available_count"`
		BlockedCount     int    `json:"blocked_count"`
		QuarantinedCount int    `json:"quarantined_count"`
		Participants     []struct {
			ParticipantKey      string  `json:"participant_key"`
			SlotCount           int     `json:"slot_count"`
			Tracked             bool    `json:"tracked"`
			Quarantined         bool    `json:"quarantined"`
			Blocked             bool    `json:"blocked"`
			RequestAllowed      bool    `json:"request_allowed"`
			AvailableForCap     bool    `json:"available_for_capacity"`
			Tokens              float64 `json:"tokens"`
			Burst               float64 `json:"burst"`
			QuarantineUntil     string  `json:"quarantine_until"`
			QuarantineRemaining int64   `json:"quarantine_remaining_ms"`
		} `json:"participants"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "12", body.ID)
	require.Equal(t, "Qwen/Test", body.Model)
	require.Equal(t, 2, body.ParticipantCount)
	require.Equal(t, 1, body.AvailableCount)
	require.Equal(t, 1, body.BlockedCount)
	require.Equal(t, 1, body.QuarantinedCount)
	require.Len(t, body.Participants, 2)

	require.Equal(t, "dead-host", body.Participants[0].ParticipantKey)
	require.Equal(t, 2, body.Participants[0].SlotCount)
	require.True(t, body.Participants[0].Tracked)
	require.True(t, body.Participants[0].Quarantined)
	require.True(t, body.Participants[0].Blocked)
	require.False(t, body.Participants[0].RequestAllowed)
	require.False(t, body.Participants[0].AvailableForCap)
	require.Equal(t, 0.0, body.Participants[0].Tokens)
	require.Equal(t, 10.0, body.Participants[0].Burst)
	require.NotEmpty(t, body.Participants[0].QuarantineUntil)
	require.Greater(t, body.Participants[0].QuarantineRemaining, int64(0))

	require.Equal(t, "healthy-host", body.Participants[1].ParticipantKey)
	require.Equal(t, 1, body.Participants[1].SlotCount)
	require.False(t, body.Participants[1].Tracked)
	require.False(t, body.Participants[1].Blocked)
	require.True(t, body.Participants[1].RequestAllowed)
	require.True(t, body.Participants[1].AvailableForCap)
}

func TestAdminAddDevshardWiresSharedPhaseGate(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	previousBuilder := gatewayRuntimeBuilder
	t.Cleanup(func() {
		gatewayRuntimeBuilder = previousBuilder
	})
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
		require.Equal(t, "12", cfg.ID)
		require.Equal(t, "http://node:1317", chainREST)
		require.Equal(t, "Qwen/Test", defaultModel)
		rt := &devshardRuntime{
			id:                    cfg.ID,
			model:                 cfg.Model,
			proxy:                 &Proxy{},
			participantSlotCounts: map[string]int{"host-a": 1},
		}
		rt.active.Store(true)
		return rt, nil
	}

	existing := &devshardRuntime{id: "6", model: "Qwen/Test"}
	existing.active.Store(true)
	g := NewGateway([]*devshardRuntime{existing}, NewGatewayLimiter(2, 200), "Qwen/Test")
	g.store = store
	g.baseStorageDir = t.TempDir()
	g.phaseGate = &ChainPhaseGate{}
	g.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		EpochPhase:           epochPhasePoCValidate,
		ConfirmationPoCPhase: confirmationPoCValidation,
		RequestsBlocked:      true,
		BlockReason:          "confirmation_poc",
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/devshards",
		strings.NewReader(`{"id":"12","private_key_env":"DEVSHARD_12_PRIVATE_KEY","model":"Qwen/Test"}`))
	rec := httptest.NewRecorder()
	g.handleAdminAddDevshard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	added := g.runtimes["12"]
	require.NotNil(t, added)
	require.Same(t, g.phaseGate, added.proxy.phaseGate)

	statusReq := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	statusRec := httptest.NewRecorder()
	g.handlePooledStatus(statusRec, statusReq)
	require.Equal(t, http.StatusOK, statusRec.Code)

	var body struct {
		Devshards []runtimeStatus `json:"devshards"`
	}
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &body))
	var addedStatus *runtimeStatus
	for i := range body.Devshards {
		if body.Devshards[i].ID == "12" {
			addedStatus = &body.Devshards[i]
			break
		}
	}
	require.NotNil(t, addedStatus)
	require.Equal(t, epochPhasePoCValidate, addedStatus.ChainPhase)
	require.Equal(t, confirmationPoCValidation, addedStatus.ConfirmationPoCPhase)
	require.True(t, addedStatus.RequestsBlocked)
	require.Equal(t, "confirmation_poc", addedStatus.BlockReason)
}

func TestAdminImportDevshardLoadsInactiveRuntimeAndAccounting(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	sourcePerfPath := filepath.Join(t.TempDir(), "perf.db")
	sourcePerf, err := NewPerfStore(sourcePerfPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, sourcePerf.Close())
	})
	startedAt := time.Now().Add(-time.Minute)
	completedAt := time.Now()
	require.NoError(t, sourcePerf.UpsertAccountingRequest("req-1", "44", "Kimi/Test", startedAt))
	require.NoError(t, sourcePerf.UpsertAccountingAttempt(RequestAccountingAttempt{
		RequestID:      "req-1",
		EscrowID:       "44",
		Nonce:          7,
		HostIdx:        1,
		ParticipantKey: "host-b",
		Probe:          true,
		CreatedAt:      startedAt,
	}))
	require.NoError(t, sourcePerf.CompleteAccountingRequest("req-1", "44", 7, "winner", "settled", completedAt))

	destPerf, err := NewPerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, destPerf.Close())
	})

	storagePath := filepath.Join(t.TempDir(), "escrow-44")
	previousBuilder := gatewayRuntimeBuilder
	t.Cleanup(func() {
		gatewayRuntimeBuilder = previousBuilder
	})
	var forwarded bool
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
		require.Equal(t, "44", cfg.ID)
		require.Equal(t, "Kimi/Test", cfg.Model)
		require.Equal(t, storagePath, cfg.StoragePath)
		require.Equal(t, "http://node:1317", chainREST)
		require.Equal(t, "Qwen/Test", defaultModel)
		rt := &devshardRuntime{
			id:    cfg.ID,
			model: cfg.Model,
			handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				forwarded = true
				w.WriteHeader(http.StatusNoContent)
			}),
		}
		rt.active.Store(true)
		return rt, nil
	}

	g := NewGateway(nil, NewGatewayLimiter(2, 200), "Qwen/Test")
	g.store = store
	g.perfStore = destPerf
	g.perf = NewPerfTracker(destPerf)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/devshards/import",
		strings.NewReader(fmt.Sprintf(`{"id":"44","private_key":"secret","model":"Kimi/Test","storage_path":%q,"perf_path":%q}`, storagePath, sourcePerfPath)))
	rec := httptest.NewRecorder()
	g.handleAdminDevshardAction(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var body struct {
		ID                         string `json:"id"`
		Active                     bool   `json:"active"`
		AccountingRecordsImported  int64  `json:"accounting_records_imported"`
		AccountingAttemptsImported int64  `json:"accounting_attempts_imported"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, "44", body.ID)
	require.False(t, body.Active)
	require.EqualValues(t, 1, body.AccountingRecordsImported)
	require.EqualValues(t, 1, body.AccountingAttemptsImported)
	require.False(t, g.runtimes["44"].active.Load())

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, state.Devshards, 1)
	require.False(t, state.Devshards[0].Active)
	require.Equal(t, storagePath, state.Devshards[0].StoragePath)

	imported, found, err := destPerf.FindAccountingRequest("req-1", "44")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "Kimi/Test", imported.Model)
	require.Equal(t, "settled", imported.Outcome)
	require.Len(t, imported.Attempts, 1)
	require.True(t, imported.Attempts[0].Winner)

	chatReq := httptest.NewRequest(http.MethodPost, "/devshard/44/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	chatRec := httptest.NewRecorder()
	g.handleDevshard(chatRec, chatReq)
	require.Equal(t, http.StatusConflict, chatRec.Code)
	require.False(t, forwarded)
}

func TestGatewayHandleDevshardFinalizeRequiresNoActiveRequests(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))

	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	rt.activeRequests.Store(1)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.store = store

	req := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.False(t, forwarded)
	require.True(t, rt.active.Load())

	rt.activeRequests.Store(0)
	rec = httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, forwarded)
	require.False(t, rt.active.Load())

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, state.Devshards, 1)
	require.False(t, state.Devshards[0].Active)
}

func TestGatewayHandlePooledChatSetsChosenDevshardHeader(t *testing.T) {
	slow := &devshardRuntime{
		id:    "6",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusAccepted)
		}),
	}
	fast := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			require.Contains(t, string(body), `"model":"Qwen/Test"`)
			w.WriteHeader(http.StatusCreated)
		}),
	}
	slow.activeRequests.Store(10)
	fast.activeRequests.Store(0)

	g := NewGateway([]*devshardRuntime{slow, fast}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
}

func TestGatewayPooledChatCachesNonStreamingResponseWithFreshRequestID(t *testing.T) {
	var calls atomic.Int32
	store, err := NewPerfStore(filepath.Join(t.TempDir(), "perf.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	perf := NewPerfTracker(store)

	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if rid, ok := requestLogFromContext(r.Context()); ok {
				perf.RecordAccountingRequestStart(rid, "12", "Qwen/Test", time.Unix(100, 0))
				perf.RecordAccountingAttempt(RequestAccountingAttempt{
					RequestID:      rid,
					EscrowID:       "12",
					Nonce:          7,
					HostIdx:        1,
					ParticipantKey: "host-a",
					CreatedAt:      time.Unix(101, 0),
				})
				perf.CompleteAccountingRequest(rid, "12", 7, "primary_only", "success", time.Unix(102, 0))
				w.Header().Set("X-Request-Id", rid)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-original","choices":[{"message":{"role":"assistant","content":"hello"}}]}`))
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.perf = perf
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	body := `{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	firstBody := rec.Body.String()
	firstRequestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, firstRequestID)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, firstBody, rec.Body.String())
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	cachedRequestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, cachedRequestID)
	require.NotEqual(t, firstRequestID, cachedRequestID)
	require.EqualValues(t, 1, calls.Load())

	accounting, ok, err := perf.FindAccountingRequest(cachedRequestID, "12")
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, cachedRequestID, accounting.RequestID)
	require.Equal(t, firstRequestID, accounting.CachedFromRequestID)
	require.Equal(t, "cached", accounting.Outcome)
	require.Equal(t, "cache_hit", accounting.Decision)
	require.Len(t, accounting.Attempts, 1)
}

func TestGatewayPooledChatCachesStreamingResponseWithFreshRequestID(t *testing.T) {
	var calls atomic.Int32
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if rid, ok := requestLogFromContext(r.Context()); ok {
				w.Header().Set("X-Request-Id", rid)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `data: {"id":"chatcmpl-original","object":"chat.completion.chunk","choices":[{"delta":{"content":"hello"},"finish_reason":null}]}`+"\n\n")
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	body := `{"model":"Qwen/Test","stream":true,"messages":[{"role":"user","content":"hello"}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	firstBody := rec.Body.String()
	firstRequestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, firstRequestID)
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, firstBody, rec.Body.String())
	require.Contains(t, rec.Header().Get("Content-Type"), "text/event-stream")
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	require.NotEqual(t, firstRequestID, rec.Header().Get("X-Request-Id"))
	require.EqualValues(t, 1, calls.Load())
}

func TestGatewayPooledChatCachesErrorResponseWithFreshRequestID(t *testing.T) {
	var calls atomic.Int32
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if rid, ok := requestLogFromContext(r.Context()); ok {
				w.Header().Set("X-Request-Id", rid)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte(`{"error":{"message":"upstream failed"}}`))
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	body := `{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	firstBody := rec.Body.String()
	firstRequestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, firstRequestID)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, firstBody, rec.Body.String())
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	require.NotEqual(t, firstRequestID, rec.Header().Get("X-Request-Id"))
	require.EqualValues(t, 1, calls.Load())
}

func TestGatewayChatCacheSharedAcrossDifferentEscrowRoutes(t *testing.T) {
	var calls12 atomic.Int32
	var calls44 atomic.Int32
	rt12 := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls12.Add(1)
			if rid, ok := requestLogFromContext(r.Context()); ok {
				w.Header().Set("X-Request-Id", rid)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-12","choices":[{"message":{"role":"assistant","content":"from escrow 12"}}]}`))
		}),
	}
	rt44 := &devshardRuntime{
		id:    "44",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls44.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"id":"chatcmpl-44","choices":[{"message":{"role":"assistant","content":"from escrow 44"}}]}`))
		}),
	}
	g := NewGateway([]*devshardRuntime{rt12, rt44}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	body := `{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`

	req := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions", strings.NewReader(body))
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	firstBody := rec.Body.String()
	firstRequestID := rec.Header().Get("X-Request-Id")
	require.NotEmpty(t, firstRequestID)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))

	req = httptest.NewRequest(http.MethodPost, "/devshard/44/v1/chat/completions", strings.NewReader(body))
	rec = httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, firstBody, rec.Body.String())
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
	require.NotEmpty(t, rec.Header().Get("X-Request-Id"))
	require.NotEqual(t, firstRequestID, rec.Header().Get("X-Request-Id"))
	require.EqualValues(t, 1, calls12.Load())
	require.EqualValues(t, 0, calls44.Load())
}

func TestGatewayHandlePooledChatRejectsUnsupportedModel(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{
		{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)},
		{ModelID: "Nope/Unsupported", AccessMode: string(gatewayAccessModeOpen)},
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Nope/Unsupported","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `unsupported model \"Nope/Unsupported\"`)
	require.Contains(t, rec.Body.String(), "Qwen/Test")
	require.False(t, forwarded)
	require.EqualValues(t, 0, rt.activeRequests.Load())
}

func TestGatewayHandlePooledChatRejectsUnsupportedModelBeforeDefaultAdminOnly(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Nope/Unsupported","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `unsupported model \"Nope/Unsupported\"`)
	require.NotContains(t, rec.Body.String(), "admin API key")
	require.False(t, forwarded)
	require.EqualValues(t, 0, rt.activeRequests.Load())
}

func TestGatewayHandleDevshardChatRejectsUnsupportedModel(t *testing.T) {
	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

	req := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions",
		strings.NewReader(`{"model":"Nope/Unsupported","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), `unsupported model \"Nope/Unsupported\"`)
	require.Contains(t, rec.Body.String(), "Qwen/Test")
	require.False(t, forwarded)
	require.EqualValues(t, 0, rt.activeRequests.Load())
}

func TestGatewayPooledChatRefreshesCapacityScaleBeforeAcquire(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	rt := &devshardRuntime{
		id:    "6",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusCreated)
		}),
		participantKeys:       []string{"host-a", "host-b"},
		participantSlotCounts: map[string]int{"host-a": 1, "host-b": 1},
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(4, 0), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	g.participantLimiter = limiter
	g.capacity.SetLiveAvailable(limiter.IsAvailable)
	g.capacity.SetHostWeights(map[string]float64{"host-a": 1, "host-b": 1}, false)

	require.NoError(t, g.limiter.AcquireForModel("Qwen/Test", 1, 1))
	require.NoError(t, g.limiter.AcquireForModel("Qwen/Test", 1, 1))
	require.EqualValues(t, 4, g.limiter.Snapshot().EffectiveMaxConcurrent)

	limiter.ObserveResult("host-a", "/sessions/6/chat/completions", http.StatusServiceUnavailable)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "too many concurrent requests")
	snap := g.limiter.Snapshot()
	require.EqualValues(t, 2, snap.EffectiveMaxConcurrent)
	require.EqualValues(t, 2, snap.InFlightRequests)
}

func TestGatewayStatusExposesCapacityLossAndEffectiveLimits(t *testing.T) {
	a := &devshardRuntime{
		id:                    "6",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-a": 1},
	}
	b := &devshardRuntime{
		id:                    "12",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-b": 1},
	}
	g := NewGateway([]*devshardRuntime{a, b}, NewGatewayLimiter(4, 100), "Qwen/Test")
	g.settings.MaxConcurrentPer10000Weight = 2
	g.settings.PoCMaxConcurrentPer10000Weight = 2
	g.capacity.SetHostWeights(map[string]float64{"host-a": 10_000, "host-b": 10_000}, false)
	g.capacity.SetLiveAvailable(func(host string) bool {
		return host != "host-a"
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handlePooledStatus(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Limiter  LimiterSnapshot       `json:"limiter"`
		Capacity gatewayCapacityStatus `json:"capacity"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.EqualValues(t, 2, body.Limiter.EffectiveMaxConcurrent)
	require.EqualValues(t, 50, body.Limiter.EffectiveMaxInputTokens)
	require.InDelta(t, 20_000.0, body.Capacity.BaselineWeight, 1e-9)
	require.InDelta(t, 10_000.0, body.Capacity.TotalWeight, 1e-9)
	require.InDelta(t, 10_000.0, body.Capacity.LostWeight, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.AvailablePercent, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.LostPercent, 1e-9)
	require.Equal(t, 1, body.Capacity.UnavailableHostCount)
	require.Equal(t, 2, body.Capacity.CurrentWeightMatched)
	require.Equal(t, 0, body.Capacity.CurrentWeightFallback)
	require.InDelta(t, 10_000.0, body.Capacity.Models["Qwen/Test"].TotalWeight, 1e-9)
	require.InDelta(t, 10_000.0, body.Capacity.Models["Qwen/Test"].CurrentWeight, 1e-9)
	require.InDelta(t, 20_000.0, body.Capacity.Models["Qwen/Test"].FullWeight, 1e-9)
	require.InDelta(t, 20_000.0, body.Capacity.Models["Qwen/Test"].BaselineWeight, 1e-9)
	require.InDelta(t, 0.5, body.Capacity.Models["Qwen/Test"].LimitShare, 1e-9)
	require.EqualValues(t, 2, body.Limiter.Models["Qwen/Test"].EffectiveMaxConcurrent)
	require.EqualValues(t, 50, body.Limiter.Models["Qwen/Test"].EffectiveMaxInputTokens)
	require.EqualValues(t, 4, body.Limiter.Models["Qwen/Test"].CapacityCapRequests)
	require.EqualValues(t, 2, body.Limiter.Models["Qwen/Test"].CurrentCapacityCapRequests)
	require.EqualValues(t, 100, body.Limiter.Models["Qwen/Test"].CapacityCapInputTokens)
	require.EqualValues(t, 50, body.Limiter.Models["Qwen/Test"].CurrentCapacityCapInputTokens)
}

func TestGatewayStatusZerosModelCapacityWithoutRoutableDevshards(t *testing.T) {
	qwen := &devshardRuntime{
		id:                    "qwen",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-q": 1},
	}
	kimi := &devshardRuntime{
		id:                    "kimi",
		model:                 "Kimi/Test",
		participantSlotCounts: map[string]int{"host-k": 1},
	}
	limiter := NewGatewayLimiter(0, 0)
	limiter.UpdateLimits(0, 0, []GatewayModelLimitSettings{
		{ModelID: "Qwen/Test", MaxConcurrentRequests: 1024},
		{ModelID: "Kimi/Test", MaxConcurrentRequests: 64},
	})
	g := NewGateway([]*devshardRuntime{qwen, kimi}, limiter, "Qwen/Test")
	kimi.active.Store(false)
	g.capacity.SetHostWeightsByModel(map[string]map[string]float64{
		"Qwen/Test": {"host-q": 100},
		"Kimi/Test": {"host-k": 50},
	}, false)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handlePooledStatus(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Limiter  LimiterSnapshot       `json:"limiter"`
		Capacity gatewayCapacityStatus `json:"capacity"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

	require.InDelta(t, 100.0, body.Capacity.Models["Qwen/Test"].CurrentWeight, 1e-9)
	require.InDelta(t, 100.0, body.Capacity.Models["Qwen/Test"].FullWeight, 1e-9)
	require.EqualValues(t, 1024, body.Limiter.Models["Qwen/Test"].CurrentCapacityCapRequests)
	require.True(t, body.Capacity.Models["Qwen/Test"].Routable)
	require.Equal(t, 1, body.Capacity.Models["Qwen/Test"].ActiveDevshards)
	require.Equal(t, 1, body.Capacity.Models["Qwen/Test"].RoutableDevshards)

	require.InDelta(t, 0.0, body.Capacity.Models["Kimi/Test"].CurrentWeight, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.Models["Kimi/Test"].FullWeight, 1e-9)
	require.InDelta(t, 0.0, body.Capacity.Models["Kimi/Test"].ScaleFactor, 1e-9)
	require.InDelta(t, 100.0, body.Capacity.Models["Kimi/Test"].LostPercent, 1e-9)
	require.EqualValues(t, 0, body.Limiter.Models["Kimi/Test"].CurrentCapacityCapRequests)
	require.False(t, body.Capacity.Models["Kimi/Test"].Routable)
	require.Equal(t, 0, body.Capacity.Models["Kimi/Test"].ActiveDevshards)
	require.Equal(t, 0, body.Capacity.Models["Kimi/Test"].RoutableDevshards)
}

func TestGatewayWiresQuarantineIntoCapacityWithoutPhaseGate(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	rt := &devshardRuntime{
		id:                    "6",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-a": 1, "host-b": 1},
	}
	other := &devshardRuntime{
		id:                    "12",
		model:                 "Qwen/Test",
		participantSlotCounts: map[string]int{"host-c": 1},
	}
	g := NewGateway([]*devshardRuntime{rt, other}, NewGatewayLimiter(4, 0), "Qwen/Test")
	g.participantLimiter = limiter
	g.attachCapacityLiveAvailability()

	limiter.ObserveResult("host-a", "/sessions/6/chat/completions", http.StatusServiceUnavailable)

	req := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handlePooledStatus(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Limiter  LimiterSnapshot       `json:"limiter"`
		Capacity gatewayCapacityStatus `json:"capacity"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Equal(t, 2, body.Capacity.AvailableHostCount)
	require.Equal(t, 1, body.Capacity.UnavailableHostCount)
	require.Equal(t, 0, body.Capacity.CurrentWeightMatched)
	require.Equal(t, 3, body.Capacity.CurrentWeightFallback)
	require.InDelta(t, 2.0, body.Capacity.TotalWeight, 1e-9)
	require.InDelta(t, 3.0, body.Capacity.BaselineWeight, 1e-9)
	require.InDelta(t, 33.333333, body.Capacity.LostPercent, 1e-6)
	require.EqualValues(t, 3, body.Limiter.EffectiveMaxConcurrent)
}

func TestGatewayChooseRuntimeSkipsInactiveDevshard(t *testing.T) {
	a := &devshardRuntime{id: "6", model: "m"}
	b := &devshardRuntime{id: "12", model: "m"}
	g := NewGateway([]*devshardRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	b.active.Store(false)

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "6", chosen.id)
}

func TestGatewayChooseRuntimeSkipsHighNonceBeforeRouting(t *testing.T) {
	highNonce := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, nonceDeactivationLimit)
	available := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit-1)
	g := NewGateway([]*devshardRuntime{highNonce, available}, NewGatewayLimiter(0, 0), "m")

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
	require.True(t, highNonce.active.Load())
}

func TestGatewayChooseRuntimeFailsWhenAllDevshardsHighNonce(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "6", balanceMinimumThreshold, nonceDeactivationLimit)
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")

	_, err := g.reserveRuntimeForModel("m", 5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no devshard runtimes available for new inferences")
	require.Contains(t, err.Error(), "skipped: high_nonce=1")
	require.True(t, rt.active.Load())
}

func TestGatewayChooseRuntimeSkipsNonActivePhaseDevshard(t *testing.T) {
	finalizing := &devshardRuntime{id: "6", model: "m", proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, types.PhaseFinalizing)}}
	settlement := &devshardRuntime{id: "9", model: "m", proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, types.PhaseSettlement)}}
	active := &devshardRuntime{id: "12", model: "m", proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, types.PhaseActive)}}
	g := NewGateway([]*devshardRuntime{finalizing, settlement, active}, NewGatewayLimiter(0, 0), "m")

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
}

func TestGatewayChooseRuntimeFailsWhenOnlyNonActivePhaseDevshardsRemain(t *testing.T) {
	finalizing := &devshardRuntime{id: "6", model: "m", proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, types.PhaseFinalizing)}}
	settlement := &devshardRuntime{id: "12", model: "m", proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, types.PhaseSettlement)}}
	g := NewGateway([]*devshardRuntime{finalizing, settlement}, NewGatewayLimiter(0, 0), "m")

	_, err := g.reserveRuntimeForModel("m", 5)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no devshard runtimes available for new inferences")
	require.Contains(t, err.Error(), "skipped: finalizing=1, settlement=1")
}

func TestGatewayExplicitChatRouteRejectsNonActivePhaseDevshard(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase types.SessionPhase
	}{
		{name: "finalizing", phase: types.PhaseFinalizing},
		{name: "settlement", phase: types.PhaseSettlement},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var forwarded bool
			rt := &devshardRuntime{
				id:    "12",
				model: "Qwen/Test",
				proxy: &Proxy{sm: gatewayTestStateMachineInPhase(t, tc.phase)},
				handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					forwarded = true
					w.WriteHeader(http.StatusNoContent)
				}),
			}
			g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")

			req := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/chat/completions",
				strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
			rec := httptest.NewRecorder()
			g.handleDevshard(rec, req)

			require.Equal(t, http.StatusConflict, rec.Code)
			require.Contains(t, rec.Body.String(), "unavailable for new inferences")
			require.False(t, forwarded)
		})
	}
}

func TestGatewayChooseRuntimeSkipsParticipantLimitedDevshard(t *testing.T) {
	// One escrow has only a throttled host (W=0 -> +Inf load), the
	// other has a healthy host. Picker must route to the healthy one.
	// No phase poll between the 503 and the pick - reactivity comes
	// from the live throttle source.
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	limited := &devshardRuntime{
		id: "6", model: "m",
		participantKeys:       []string{"shared-host"},
		participantSlotCounts: map[string]int{"shared-host": 1},
	}
	available := &devshardRuntime{
		id: "12", model: "m",
		participantKeys:       []string{"fresh-host"},
		participantSlotCounts: map[string]int{"fresh-host": 1},
	}
	g := NewGateway([]*devshardRuntime{limited, available}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter
	g.capacity.SetLiveAvailable(limiter.IsAvailable)

	chosen, err := g.reserveRuntimeForModel("m", 5)
	require.NoError(t, err)
	require.Equal(t, "12", chosen.id)
}

func TestGatewayChooseRuntimePrefersHealthyEscrowWithoutBenchingPartial(t *testing.T) {
	// Mixed escrow with one healthy and one throttled host should not
	// be benched entirely - it should still receive *some* traffic
	// (its W(e) is half), just less than a fully healthy peer.
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("dead-host", "/sessions/6/chat/completions", http.StatusServiceUnavailable)

	mixed := &devshardRuntime{
		id: "6", model: "m",
		participantKeys:       []string{"dead-host", "live-host"},
		participantSlotCounts: map[string]int{"dead-host": 1, "live-host": 1},
	}
	healthy := &devshardRuntime{
		id: "12", model: "m",
		participantKeys:       []string{"fresh-a", "fresh-b"},
		participantSlotCounts: map[string]int{"fresh-a": 1, "fresh-b": 1},
	}
	g := NewGateway([]*devshardRuntime{mixed, healthy}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter
	g.capacity.SetLiveAvailable(limiter.IsAvailable)

	counts := map[string]int{}
	for i := 0; i < 60; i++ {
		rt, err := g.reserveRuntimeForModel("m", 1)
		require.NoError(t, err)
		counts[rt.id]++
	}
	require.Greater(t, counts["12"], counts["6"], "healthy escrow should win majority: %v", counts)
	require.Greater(t, counts["6"], 0, "partially-throttled escrow should still receive traffic: %v", counts)
}

func TestGatewayChooseRuntimeFailsWhenAllDevshardsParticipantLimited(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	a := &devshardRuntime{
		id: "6", model: "m",
		participantKeys:       []string{"shared-host"},
		participantSlotCounts: map[string]int{"shared-host": 1},
	}
	b := &devshardRuntime{
		id: "12", model: "m",
		participantKeys:       []string{"shared-host"},
		participantSlotCounts: map[string]int{"shared-host": 1},
	}
	g := NewGateway([]*devshardRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter
	g.capacity.SetLiveAvailable(limiter.IsAvailable)

	_, err := g.reserveRuntimeForModel("m", 5)
	require.Error(t, err)
	require.True(t, isParticipantRateLimitError(err))
}

func TestGatewayChooseRuntimeReactsToRecoveryWithoutPhasePoll(t *testing.T) {
	// 503 puts host in cooldown -> picker avoids that escrow. After
	// enough simulated time passes for the bucket to refill above 1,
	// the next pick must route there again - no phase-gate poll
	// involved.
	limiter := NewParticipantRequestLimiter(1, 60) // 1 token/sec
	limiter.ObserveResult("a-host", "/x", http.StatusServiceUnavailable)

	a := &devshardRuntime{
		id: "a", model: "m",
		participantKeys:       []string{"a-host"},
		participantSlotCounts: map[string]int{"a-host": 1},
	}
	b := &devshardRuntime{
		id: "b", model: "m",
		participantKeys:       []string{"b-host"},
		participantSlotCounts: map[string]int{"b-host": 1},
	}
	g := NewGateway([]*devshardRuntime{a, b}, NewGatewayLimiter(0, 0), "m")
	g.participantLimiter = limiter
	g.capacity.SetLiveAvailable(limiter.IsAvailable)

	// Immediately after 503: a is dead (W=0), picks must hit b only.
	for i := 0; i < 5; i++ {
		rt, err := g.reserveRuntimeForModel("m", 1)
		require.NoError(t, err)
		require.Equal(t, "b", rt.id, "iteration %d before recovery", i)
		g.releaseRuntime(rt, 1)
	}

	// Simulate full recovery after the 503 quarantine (wall clock would be
	// httpThrottleQuarantine; tests clear tracking directly).
	limiter.mu.Lock()
	delete(limiter.participants, "a-host")
	limiter.mu.Unlock()

	// Now both escrows have non-zero weight; over many picks, both
	// should receive at least one request.
	counts := map[string]int{}
	for i := 0; i < 20; i++ {
		rt, err := g.reserveRuntimeForModel("m", 1)
		require.NoError(t, err)
		counts[rt.id]++
		g.releaseRuntime(rt, 1)
	}
	require.Greater(t, counts["a"], 0, "recovered escrow should receive traffic: %v", counts)
}

func TestGatewayExplicitRouteStillWorksForInactiveDevshard(t *testing.T) {
	var seenPath string
	rt := &devshardRuntime{
		id:    "12",
		model: "m",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			seenPath = r.URL.Path
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "m")
	rt.active.Store(false)

	req := httptest.NewRequest(http.MethodGet, "/devshard/12/v1/status", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, "/v1/status", seenPath)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
}

// TestGatewayExplicitChatRouteRejectsParticipantLimitedDevshard was removed
// with the all-or-nothing participant gate on the per-devshard path. A
// single throttled host no longer shuts the whole escrow down at the
// gateway; the picker / redundancy layer handles partial (or total)
// throttling per-nonce via IsBlocked -> ghostThrottled silent probes,
// and the pooled path's W(e)-based routing covers the "every host gone"
// shed via EscrowParticipantRateLimitError. See gateway.handleDevshard
// for the per-devshard path comment and gateway.reserveRuntimeForModel
// for the pooled path's +Inf-load rejection.

func TestGatewayLimiterEnforcesConcurrentAndTokenLimits(t *testing.T) {
	limiter := NewGatewayLimiter(1, 10)

	require.NoError(t, limiter.Acquire(8))
	require.ErrorContains(t, limiter.Acquire(1), "too many concurrent requests")
	limiter.Release(8)

	tokenLimiter := NewGatewayLimiter(2, 10)
	require.NoError(t, tokenLimiter.Acquire(5))
	require.ErrorContains(t, tokenLimiter.Acquire(6), "too many input tokens in flight")
}

func TestParticipantRequestLimiterUntrackedHostAlwaysAllowed(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	now := time.Now()

	require.True(t, limiter.allow("shared-host", now))
	require.True(t, limiter.allow("shared-host", now))
	require.True(t, limiter.allow("shared-host", now))
}

func TestParticipantRequestLimiterTransportShorterQuarantineThan503(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	t0 := time.Now()
	limiter.ObserveTransportFailure("transport-host", "/sessions/1/chat/completions", fmt.Errorf("dial tcp: connection refused"))
	require.True(t, limiter.IsBlocked("transport-host"))
	require.True(t, limiter.allow("transport-host", t0.Add(transportFailureQuarantine+time.Second)))

	limiter.ObserveResult("http-host", "/sessions/1/chat/completions", http.StatusServiceUnavailable)
	require.True(t, limiter.IsBlocked("http-host"))
	require.False(t, limiter.allow("http-host", t0.Add(transportFailureQuarantine+time.Second)))
	require.True(t, limiter.allow("http-host", t0.Add(httpThrottleQuarantine+time.Second)))
}

func TestParticipantRequestLimiterTransportFailureOnVerifyTimeoutDoesNotQuarantine(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveTransportFailure("vote-host", "/sessions/1/verify-timeout", fmt.Errorf("dial tcp: i/o timeout"))
	require.False(t, limiter.IsBlocked("vote-host"), "verify-timeout transport failure must not quarantine")

	limiter.ObserveTransportFailure("gossip-host", "/sessions/1/gossip/nonce", fmt.Errorf("connection refused"))
	require.False(t, limiter.IsBlocked("gossip-host"), "gossip transport failure must not quarantine")

	limiter.ObserveTransportFailure("infer-host", "/sessions/1/chat/completions", fmt.Errorf("dial tcp: i/o timeout"))
	require.True(t, limiter.IsBlocked("infer-host"), "inference transport failure must quarantine")
}

func TestParticipantRequestLimiterEOFTransportFailureQuarantinesAfterThreeConsecutive(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	now := time.Now()

	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	require.False(t, limiter.IsBlocked("eof-host"))

	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	require.False(t, limiter.IsBlocked("eof-host"))

	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	require.True(t, limiter.IsBlocked("eof-host"))
	require.True(t, limiter.allow("eof-host", now.Add(transportFailureQuarantine+time.Second)))
}

func TestParticipantRequestLimiterSuccessfulInferenceResetsEOFTransportFailureStreak(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)

	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	limiter.ObserveSuccessfulInference("eof-host")

	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	require.False(t, limiter.IsBlocked("eof-host"))
	limiter.ObserveTransportFailure("eof-host", "/sessions/1/chat/completions", fmt.Errorf("read stream: EOF"))
	require.True(t, limiter.IsBlocked("eof-host"))
}

func TestParticipantRequestLimiterInferenceRouteFailureUsesShortQuarantine(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	t0 := time.Now()
	limiter.ObserveResult("broken-host", "/sessions/38/chat/completions", http.StatusNotFound)
	require.True(t, limiter.IsBlocked("broken-host"))
	require.True(t, limiter.allow("broken-host", t0.Add(transportFailureQuarantine+time.Second)))

	limiter.ObserveResult("forbidden-host", "/sessions/38/chat/completions", http.StatusForbidden)
	require.True(t, limiter.IsBlocked("forbidden-host"))
	require.True(t, limiter.allow("forbidden-host", t0.Add(transportFailureQuarantine+time.Second)))
}

func TestParticipantRequestLimiterTimestampDriftUsesShortQuarantine(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	t0 := time.Now()

	limiter.ObserveResultWithBody("drift-host", "/sessions/38/chat/completions", http.StatusUnauthorized, `{"error":"timestamp drift 64s exceeds maximum 30s"}`)

	require.True(t, limiter.IsBlocked("drift-host"))
	require.True(t, limiter.allow("drift-host", t0.Add(transportFailureQuarantine+time.Second)))
}

func TestParticipantRequestLimiterEmptyStreamQuarantineAfterThreeConsecutive(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	now := time.Now()

	limiter.ObserveEmptyStream("empty-host")
	require.False(t, limiter.IsBlocked("empty-host"))
	require.NoError(t, limiter.AllowRequest("empty-host", "/sessions/12/chat/completions"))

	limiter.ObserveEmptyStream("empty-host")
	require.False(t, limiter.IsBlocked("empty-host"))
	require.NoError(t, limiter.AllowRequest("empty-host", "/sessions/12/chat/completions"))

	limiter.ObserveEmptyStream("empty-host")
	require.True(t, limiter.IsBlocked("empty-host"))
	require.True(t, limiter.allow("empty-host", now.Add(emptyStreamQuarantine+time.Second)))
}

func TestParticipantRequestLimiterUsesUpdatedThrottleSettings(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.UpdateSettings(ParticipantThrottleSettings{
		RequestBurst:                   10,
		RecoveryPerMinute:              10,
		HTTPQuarantineMS:               200,
		TransportFailureQuarantineMS:   100,
		EmptyStreamQuarantineMS:        150,
		StalledWinnerQuarantineMS:      175,
		EmptyStreamQuarantineThreshold: 2,
	})
	now := time.Now()

	limiter.ObserveTransportFailure("transport-host", "/sessions/1/chat/completions", fmt.Errorf("dial tcp: connection refused"))
	require.True(t, limiter.IsBlocked("transport-host"))
	require.True(t, limiter.allow("transport-host", now.Add(101*time.Millisecond)))

	limiter.ObserveEmptyStream("empty-host")
	require.False(t, limiter.IsBlocked("empty-host"))
	limiter.ObserveEmptyStream("empty-host")
	require.True(t, limiter.IsBlocked("empty-host"))
	require.True(t, limiter.allow("empty-host", now.Add(151*time.Millisecond)))
}

func TestParticipantRequestLimiterSuccessfulInferenceResetsEmptyStreamStreak(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)

	limiter.ObserveEmptyStream("empty-host")
	limiter.ObserveEmptyStream("empty-host")
	limiter.ObserveSuccessfulInference("empty-host")

	require.False(t, limiter.IsBlocked("empty-host"))

	limiter.ObserveEmptyStream("empty-host")
	require.False(t, limiter.IsBlocked("empty-host"))
	limiter.ObserveEmptyStream("empty-host")
	require.False(t, limiter.IsBlocked("empty-host"))
	limiter.ObserveEmptyStream("empty-host")
	require.True(t, limiter.IsBlocked("empty-host"))
}

func TestParticipantRequestLimiterStalledWinnerQuarantinesImmediately(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	now := time.Now()

	limiter.ObserveEmptyStream("stall-host")
	limiter.ObserveEmptyStream("stall-host")
	limiter.ObserveStalledWinner("stall-host")

	require.True(t, limiter.IsBlocked("stall-host"))
	require.True(t, limiter.allow("stall-host", now.Add(stalledWinnerQuarantine+time.Second)))
}

func TestParticipantRequestLimiterRecoversAfterThrottle(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	now := time.Now()
	require.False(t, limiter.allow("shared-host", now))
	after := now.Add(httpThrottleQuarantine + 2*time.Second)
	require.True(t, limiter.allow("shared-host", after))
}

func TestParticipantRequestLimiterMarksParticipantExhaustedOn503(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.Equal(t, 1, limiter.ExhaustedCount())
	require.Equal(t, 1, limiter.TrackedCount())
	require.Error(t, limiter.CanAcceptEscrow([]string{"shared-host"}))
}

func TestParticipantRequestLimiterExpiresOnFullRecovery(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.Equal(t, 1, limiter.TrackedCount())
	require.Equal(t, 1, limiter.ExhaustedCount())

	now := time.Now().Add(httpThrottleQuarantine + 2*time.Second)
	require.True(t, limiter.allow("shared-host", now))
	require.Equal(t, 1, limiter.TrackedCount())
	require.True(t, limiter.IsRecentlyQuarantined("shared-host"))

	for i := 0; i < participantProbationSuccessesAfterQuarantine-1; i++ {
		limiter.ObserveSuccessfulInference("shared-host")
		require.True(t, limiter.IsRecentlyQuarantined("shared-host"))
	}
	limiter.ObserveSuccessfulInference("shared-host")
	require.False(t, limiter.IsRecentlyQuarantined("shared-host"))
	require.Equal(t, 0, limiter.TrackedCount())
}

func TestParticipantRequestLimiterClearQuarantineStartsProbation(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.True(t, limiter.ClearQuarantine("shared-host"))
	require.False(t, limiter.IsBlocked("shared-host"))
	require.True(t, limiter.IsRecentlyQuarantined("shared-host"))
	require.Equal(t, 1, limiter.TrackedCount())

	snapshot := limiter.Snapshot([]string{"shared-host"})["shared-host"]
	require.True(t, snapshot.Tracked)
	require.False(t, snapshot.Quarantined)
	require.False(t, snapshot.Blocked)
	require.True(t, snapshot.RequestAllowed)
	require.True(t, snapshot.AvailableForCapacity)

	for i := 0; i < participantProbationSuccessesAfterQuarantine-1; i++ {
		limiter.ObserveSuccessfulInference("shared-host")
		require.True(t, limiter.IsRecentlyQuarantined("shared-host"))
	}
	limiter.ObserveSuccessfulInference("shared-host")
	require.False(t, limiter.IsRecentlyQuarantined("shared-host"))
	require.Equal(t, 0, limiter.TrackedCount())
}

func TestParticipantRequestLimiterPersistsThrottleState(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "shared-host", rows[0].Key)
	require.Equal(t, float64(0), rows[0].Tokens)
	require.Equal(t, http.StatusServiceUnavailable, rows[0].Status)
	require.Equal(t, 0, rows[0].EmptyStreamStreak)
}

func TestParticipantRequestLimiterPersistsEmptyStreamStreak(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.ObserveEmptyStream("shared-host")
	limiter.ObserveEmptyStream("shared-host")

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "shared-host", rows[0].Key)
	require.Equal(t, 2, rows[0].EmptyStreamStreak)
}

func TestParticipantRequestLimiterLoadStateRecoversTokens(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 60)
	pastRefill := time.Now().Add(-5 * time.Second)
	limiter.LoadState("shared-host", 0, pastRefill)

	require.Equal(t, 1, limiter.TrackedCount())
	require.NoError(t, limiter.AllowRequest("shared-host", "/sessions/12/chat/completions"))
}

func TestParticipantRequestLimiterLoadStateDeletesFullyRecovered(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	require.NoError(t, store.SaveParticipantThrottle("shared-host", 0, time.Now().Add(-time.Hour), 503, time.Time{}, 0, 0))

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.LoadState("shared-host", 0, time.Now().Add(-time.Hour))

	require.Equal(t, 0, limiter.TrackedCount())

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 0)
}

func TestParticipantRequestLimiterDeletesOnExpiry(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })

	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.SetStore(store)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rows, err := store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 1)

	now := time.Now().Add(httpThrottleQuarantine + 2*time.Second)
	require.True(t, limiter.allow("shared-host", now))

	rows, err = store.LoadParticipantThrottles()
	require.NoError(t, err)
	require.Len(t, rows, 0)
}

func TestNormalizeChatRequestDefaultsAndCapsMaxTokens(t *testing.T) {
	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	DefaultRequestMaxTokens = 3_072
	RequestMaxTokensCap = 4_096
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
		sharedParticipantRequestLimiter.UpdateSettings(DefaultParticipantThrottleSettings())
		ApplyRedundancySettings(DefaultRedundancySettings())
	})

	body, req, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 3_072, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":3072`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":64,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 64, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":64`)
	require.NotContains(t, string(body), `"max_completion_tokens"`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":10001,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 4_096, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":4096`)
}

func TestGatewayParseChatReservationUsesPerModelTokenLimits(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.settings = GatewaySettings{
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 3072,
		RequestMaxTokensCap:     4096,
		ModelLimits: []GatewayModelLimitSettings{{
			ModelID:                 "Kimi/Test",
			DefaultRequestMaxTokens: 2048,
			RequestMaxTokensCap:     3584,
		}},
	}.WithTuningDefaults()

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","max_tokens":4096,"messages":[{"role":"user","content":"hello"}]}`))
	body, model, _, err := g.parseChatReservation(req, g.settings.DefaultModel)

	require.NoError(t, err)
	require.Equal(t, "Kimi/Test", model)
	require.Contains(t, string(body), `"max_tokens":3584`)

	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Kimi/Test","messages":[{"role":"user","content":"hello"}]}`))
	body, model, _, err = g.parseChatReservation(req, g.settings.DefaultModel)

	require.NoError(t, err)
	require.Equal(t, "Kimi/Test", model)
	require.Contains(t, string(body), `"max_tokens":2048`)
}

func TestFinalizeRuntimeConfigsUsesPerEscrowStorageDirectories(t *testing.T) {
	baseDir := "/tmp/devshardctl"
	runtimes, err := finalizeRuntimeConfigs([]RuntimeConfig{{
		ID:            "12",
		PrivateKeyHex: "abc123",
	}}, "Qwen/Test", baseDir)
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, filepath.Join(baseDir, "escrow-12"), runtimes[0].StoragePath)
	require.Equal(t, "Qwen/Test", runtimes[0].Model)
}

func TestFinalizeRuntimeConfigsNormalizesLegacyStateDBPath(t *testing.T) {
	baseDir := "/tmp/devshardctl"
	legacyPath := filepath.Join(baseDir, "escrow-12", "state.db")
	runtimes, err := finalizeRuntimeConfigs([]RuntimeConfig{{
		ID:          "12",
		StoragePath: legacyPath,
	}}, "Qwen/Test", baseDir)
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, filepath.Dir(legacyPath), runtimes[0].StoragePath)
}

func TestResolveBaseStorageDirNormalizesLegacyStateDBPath(t *testing.T) {
	baseDir := "/tmp/devshardctl"
	legacyPath := filepath.Join(baseDir, "escrow-12", "state.db")
	require.Equal(t, baseDir, resolveBaseStorageDir("", legacyPath))
	require.Equal(t, baseDir, resolveBaseStorageDir("", filepath.Dir(legacyPath)))
}

func TestMigrateGatewayLegacyStorageUsesChainEpoch(t *testing.T) {
	storageDir := t.TempDir()
	legacyPath := filepath.Join(storageDir, "state.db")
	writeGatewayLegacyStateDB(t, legacyPath, "12", 3)

	err := migrateGatewayLegacyStorage(storageDir, legacyPath, "12", gatewayMigrationBridge{epochID: 270})
	require.NoError(t, err)

	_, err = os.Stat(legacyPath)
	require.True(t, os.IsNotExist(err), "legacy state.db should be renamed after successful migration")

	sqlStore, err := storage.NewSQLite(storageDir)
	require.NoError(t, err)
	defer sqlStore.Close()
	meta, err := sqlStore.GetSessionMeta("12")
	require.NoError(t, err)
	require.Equal(t, uint64(270), meta.EpochID)
	require.Equal(t, uint64(3), meta.LatestNonce)
}

func TestMigrateGatewayLegacyStorageRejectsConflictingEpochDB(t *testing.T) {
	storageDir := t.TempDir()
	legacyPath := filepath.Join(storageDir, "state.db")
	writeGatewayLegacyStateDB(t, legacyPath, "12", 1)

	sqlStore, err := storage.NewSQLite(storageDir)
	require.NoError(t, err)
	require.NoError(t, sqlStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       "12",
		EpochID:        270,
		Version:        types.LegacySessionVersion,
		CreatorAddr:    "creator",
		Config:         types.SessionConfig{},
		Group:          []types.SlotAssignment{{SlotID: 0, ValidatorAddress: "a"}},
		InitialBalance: 1000,
	}))
	require.NoError(t, sqlStore.AppendDiff("12", types.DiffRecord{
		Diff:      types.Diff{Nonce: 1},
		StateHash: []byte("different"),
	}))
	require.NoError(t, sqlStore.Close())

	err = migrateGatewayLegacyStorage(storageDir, legacyPath, "12", gatewayMigrationBridge{epochID: 270})
	require.ErrorContains(t, err, "migrated diff conflict")

	_, statErr := os.Stat(legacyPath)
	require.NoError(t, statErr, "legacy state.db must remain after a conflicting migration")
}

type gatewayMigrationBridge struct {
	epochID uint64
}

func (b gatewayMigrationBridge) OnEscrowCreated(bridge.EscrowInfo) error { return nil }
func (b gatewayMigrationBridge) OnSettlementProposed(string, []byte, uint64) error {
	return nil
}
func (b gatewayMigrationBridge) OnSettlementFinalized(string) error { return nil }
func (b gatewayMigrationBridge) GetEscrow(escrowID string) (*bridge.EscrowInfo, error) {
	return &bridge.EscrowInfo{EscrowID: escrowID, EpochID: b.epochID}, nil
}
func (b gatewayMigrationBridge) GetHostInfo(string) (*bridge.HostInfo, error) { return nil, nil }
func (b gatewayMigrationBridge) GetValidationThreshold(uint64, string) (*bridge.Decimal, error) {
	return nil, nil
}
func (b gatewayMigrationBridge) VerifyWarmKey(string, string) (bool, error) { return false, nil }
func (b gatewayMigrationBridge) SubmitDisputeState(string, []byte, uint64, map[uint32][]byte) error {
	return nil
}

func writeGatewayLegacyStateDB(t *testing.T, path, escrowID string, latestNonce uint64) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	defer db.Close()

	_, err = db.Exec(`
	CREATE TABLE sessions (
		escrow_id       TEXT PRIMARY KEY,
		version         TEXT,
		creator_addr    TEXT NOT NULL,
		config_json     TEXT NOT NULL,
		group_json      TEXT NOT NULL,
		initial_balance INTEGER NOT NULL,
		latest_nonce    INTEGER NOT NULL DEFAULT 0,
		last_finalized  INTEGER NOT NULL DEFAULT 0,
		status          TEXT NOT NULL DEFAULT 'active',
		settled_at      INTEGER
	);
	CREATE TABLE diffs (
		escrow_id       TEXT NOT NULL,
		nonce           INTEGER NOT NULL,
		txs_proto       BLOB NOT NULL,
		user_sig        BLOB,
		post_state_root BLOB,
		state_hash      BLOB,
		warm_keys_json  TEXT,
		created_at      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (escrow_id, nonce)
	);
	CREATE TABLE signatures (
		escrow_id TEXT NOT NULL,
		nonce     INTEGER NOT NULL,
		slot_id   INTEGER NOT NULL,
		sig       BLOB NOT NULL,
		PRIMARY KEY (escrow_id, nonce, slot_id)
	);`)
	require.NoError(t, err)

	groupJSON, err := json.Marshal([]types.SlotAssignment{{SlotID: 0, ValidatorAddress: "a"}})
	require.NoError(t, err)
	configJSON, err := json.Marshal(types.SessionConfig{})
	require.NoError(t, err)
	_, err = db.Exec(
		`INSERT INTO sessions (escrow_id, version, creator_addr, config_json, group_json, initial_balance, latest_nonce)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		escrowID, types.LegacySessionVersion, "creator", string(configJSON), string(groupJSON), 1000, latestNonce,
	)
	require.NoError(t, err)
	for nonce := uint64(1); nonce <= latestNonce; nonce++ {
		_, err = db.Exec(
			`INSERT INTO diffs (escrow_id, nonce, txs_proto, state_hash, created_at) VALUES (?, ?, ?, ?, ?)`,
			escrowID, nonce, []byte{}, []byte{byte(nonce)}, int64(nonce),
		)
		require.NoError(t, err)
	}
}

func TestAdminSettingsUpdatesLimiterAndDefaultTokens(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		RequestMaxTokensCap:     2000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	oldDefault := DefaultRequestMaxTokens
	oldCap := RequestMaxTokensCap
	oldStreamingHardTimeout := StreamingAttemptHardTimeout
	oldNonStreamNoContentTimeout := nonStreamingNoContentTimeout
	oldNonStreamMaxAttemptWait := nonStreamingMaxAttemptWait
	DefaultRequestMaxTokens = 1000
	RequestMaxTokensCap = 2000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		RequestMaxTokensCap = oldCap
		StreamingAttemptHardTimeout = oldStreamingHardTimeout
		nonStreamingNoContentTimeout = oldNonStreamNoContentTimeout
		nonStreamingMaxAttemptWait = oldNonStreamMaxAttemptWait
	})

	limiter := NewGatewayLimiter(2, 200)
	g := NewManagedGateway(nil, limiter, GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		RequestMaxTokensCap:     2000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
		Disabled: GatewayDisabledSettings{
			Enabled: true,
			Message: "please use http://.../v1/ base url",
			NewURL:  "http://.../v1/chat/completions",
		},
	}, t.TempDir(), store)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/settings",
		strings.NewReader(`{"chain_rest":"http://node:2317","public_api":"http://api:9900","default_model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","max_concurrent_requests":7,"max_input_tokens_in_flight":700,"default_request_max_tokens":3072,"request_max_tokens_cap":4096,"tx_gas_limit":700000,"model_limits":[{"model_id":"moonshotai/Kimi-K2.6","access_mode":"admin_only","access_message":"Kimi temporarily unavailable"}],"disabled":{"enabled":true,"message":"please use ... base url","new_url":"https://.../v1/chat/completions"},"participant_throttle":{"request_burst":42,"recovery_per_minute":7,"http_quarantine_ms":1100,"transport_failure_quarantine_ms":1200,"empty_stream_quarantine_ms":1300,"stalled_winner_quarantine_ms":1400,"empty_stream_threshold":2},"redundancy":{"receipt_timeout_ms":1500,"first_token_timeout_floor_ms":1600,"per_input_token_first_token_lag_ms":17,"inter_chunk_stall_timeout_ms":1800,"streaming_attempt_hard_timeout_ms":1810,"non_stream_response_floor_ms":1900,"non_stream_no_content_timeout_ms":2200,"non_stream_max_attempt_wait_ms":2600,"per_input_token_response_lag_ms":20,"secondary_wait_after_winner_ms":2100,"parallel_advantage_threshold":0.4,"unresponsive_threshold":0.8}}`))
	rec := httptest.NewRecorder()
	g.handleAdminSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 3072, DefaultRequestMaxTokens)
	require.EqualValues(t, 4096, RequestMaxTokensCap)

	snap := limiter.Snapshot()
	require.EqualValues(t, 7, snap.MaxConcurrent)
	require.EqualValues(t, 700, snap.MaxInputTokens)

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "http://node:2317", state.Settings.ChainREST)
	require.Equal(t, "http://api:9900", state.Settings.PublicAPI)
	require.Equal(t, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", state.Settings.DefaultModel)
	require.EqualValues(t, 3072, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 4096, state.Settings.RequestMaxTokensCap)
	require.EqualValues(t, 7, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 700, state.Settings.MaxInputTokensInFlight)
	require.EqualValues(t, 700000, state.Settings.TxGasLimit)
	require.Equal(t, []GatewayModelLimitSettings{{
		ModelID:       "moonshotai/Kimi-K2.6",
		AccessMode:    "admin_only",
		AccessMessage: "Kimi temporarily unavailable",
	}}, state.Settings.ModelLimits)
	require.True(t, state.Settings.Disabled.Enabled)
	require.Equal(t, "please use ... base url", state.Settings.Disabled.Message)
	require.Equal(t, "https://.../v1/chat/completions", state.Settings.Disabled.NewURL)
	require.EqualValues(t, 42, state.Settings.ParticipantThrottle.RequestBurst)
	require.EqualValues(t, 2, state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold)
	require.EqualValues(t, 1500, state.Settings.Redundancy.ReceiptTimeoutMS)
	require.EqualValues(t, 17, state.Settings.Redundancy.PerInputTokenFirstTokenLagMS)
	require.EqualValues(t, 1810, state.Settings.Redundancy.StreamingAttemptHardTimeoutMS)
	require.EqualValues(t, 2200, state.Settings.Redundancy.NonStreamNoContentTimeoutMS)
	require.EqualValues(t, 2600, state.Settings.Redundancy.NonStreamMaxAttemptWaitMS)
	require.Equal(t, 0.4, state.Settings.Redundancy.ParallelAdvantageThreshold)
	require.Equal(t, 1500*time.Millisecond, ReceiptTimeout)
	require.Equal(t, 17*time.Millisecond, PerInputTokenFirstTokenLag)
	require.Equal(t, 1810*time.Millisecond, StreamingAttemptHardTimeout)
	require.Equal(t, 2200*time.Millisecond, nonStreamingNoContentTimeout)
	require.Equal(t, 2600*time.Millisecond, nonStreamingMaxAttemptWait)
}

func TestAdminSettingsRejectsInvalidTuning(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
		sharedParticipantRequestLimiter.UpdateSettings(DefaultParticipantThrottleSettings())
		ApplyRedundancySettings(DefaultRedundancySettings())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	g := NewManagedGateway(nil, NewGatewayLimiter(2, 200), GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, t.TempDir(), store)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/settings",
		strings.NewReader(`{"participant_throttle":{"empty_stream_threshold":0}}`))
	rec := httptest.NewRecorder()
	g.handleAdminSettings(rec, req)

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "empty_stream_threshold")
}

func TestAdminSettingsUpdatesEscrowRotationSettlementEnabled(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	g := NewManagedGateway(nil, NewGatewayLimiter(2, 200), GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, t.TempDir(), store)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/settings",
		strings.NewReader(`{"escrow_rotation":{"settlement_enabled":true}}`))
	rec := httptest.NewRecorder()
	g.handleAdminSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, state.Settings.EscrowRotation.SettlementEnabled)
}

func TestDebugRotationReportsCountdownAndLatestStatus(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, store.Close())
	})
	settings := GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
		EscrowRotation: EscrowRotationSettings{
			Enabled:      true,
			PrePoCBlocks: 300,
			Models: []EscrowRotationModelSettings{{
				ModelID:       "Qwen/Test",
				TempCount:     4,
				TargetCount:   8,
				Amount:        1000,
				PrivateKeyEnv: "DEVSHARD_PRIVATE_KEY",
			}},
		},
	}.WithTuningDefaults()
	require.NoError(t, store.Initialize(settings, nil))
	require.NoError(t, store.SaveRotationStatus(GatewayRotationStatus{
		ModelID:           "Qwen/Test",
		Stage:             "prepare_temp",
		Epoch:             10,
		Role:              rotationRoleTemp,
		TargetCount:       4,
		ExistingCount:     1,
		CreatedCount:      3,
		SettledCount:      8,
		SettleFailedCount: 0,
		Completed:         true,
		UpdatedAt:         "2026-05-04T00:00:00Z",
	}))
	g := &Gateway{
		store:     store,
		settings:  settings,
		phaseGate: &ChainPhaseGate{},
	}
	g.phaseGate.storeSnapshot(ChainPhaseSnapshot{
		BlockHeight:            1000,
		EpochIndex:             10,
		EpochPhase:             epochPhaseInference,
		epochSwitchBlockHeight: 1400,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/debug/rotation", nil)
	rec := httptest.NewRecorder()
	g.handleDebugRotation(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Settings struct {
			SettlementEnabled bool `json:"settlement_enabled"`
		} `json:"settings"`
		Chain struct {
			BlocksToEpochSwitch     int64 `json:"blocks_to_epoch_switch"`
			BlocksUntilNextRotation int64 `json:"blocks_until_next_rotation"`
		} `json:"chain"`
		Latest []GatewayRotationStatus `json:"latest"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.False(t, body.Settings.SettlementEnabled)
	require.EqualValues(t, 400, body.Chain.BlocksToEpochSwitch)
	require.EqualValues(t, 100, body.Chain.BlocksUntilNextRotation)
	require.Len(t, body.Latest, 1)
	require.Equal(t, "prepare_temp", body.Latest[0].Stage)
	require.EqualValues(t, 3, body.Latest[0].CreatedCount)
	require.EqualValues(t, 8, body.Latest[0].SettledCount)
	require.True(t, body.Latest[0].Completed)
}

func TestGatewayMetricsEndpointExposedAndUpdated(t *testing.T) {
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	handler := buildGatewayHandler(g, runtimeOptions{apiKeys: map[string]struct{}{"secret": {}}})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	handler.ServeHTTP(metricsRec, metricsReq)
	require.Equal(t, http.StatusOK, metricsRec.Code)

	body := metricsRec.Body.String()
	require.Contains(t, body, "devshard_http_requests_total")
	require.Contains(t, body, `path="/v1/chat/completions"`)
	require.Contains(t, body, `status="429"`)
	require.Contains(t, body, `devshard_gateway_limit_rejections_total`)
	require.Contains(t, body, `reason="max_input_tokens_in_flight"`)
	require.Contains(t, body, `devshard_gateway_inflight_requests`)
	require.Contains(t, body, `devshard_runtime_active`)
	require.Contains(t, body, `devshard_id="12"`)
	require.Contains(t, body, `model="Qwen/Test"`)
}

func TestGatewayMetricsCollectorIncludesParticipantLimiterState(t *testing.T) {
	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	rt := &devshardRuntime{
		id:              "12",
		model:           "Qwen/Test",
		participantKeys: []string{"shared-host", "other-host"},
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(0, 0), "Qwen/Test")
	g.participantLimiter = limiter
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter(nil))

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_gateway_participants_exhausted", nil, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_participants_tracked", nil, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_escrow_participant_limited", map[string]string{"devshard_id": "12", "model": "Qwen/Test"}, 1)
	requireMetricGaugeValue(t, families, "devshard_gateway_escrow_blocked_participants", map[string]string{"devshard_id": "12", "model": "Qwen/Test"}, 1)
}

func TestGatewayStatusCodeForErrorMapsUpstream503To429(t *testing.T) {
	code := gatewayStatusCodeForError(&transport.UpstreamStatusError{
		Path:       "/sessions/12/chat/completions",
		StatusCode: http.StatusServiceUnavailable,
		Body:       "nginx limit",
	})
	require.Equal(t, http.StatusTooManyRequests, code)
}

func TestParticipantLimiterBypassedDuringRelaxedPoC(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	setPoCPhaseState(true, "poc")

	limiter := NewParticipantRequestLimiter(1, 10)
	limiter.ObserveResult("shared-host", "/sessions/12/chat/completions", http.StatusServiceUnavailable)

	require.NoError(t, limiter.AllowRequest("shared-host", "/sessions/12/chat/completions"))
	require.NoError(t, limiter.CanAcceptEscrow([]string{"shared-host"}))
}

func TestGatewayLimiterBypassedDuringRelaxedPoC(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	setPoCPhaseState(true, "poc")

	var forwarded bool
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			forwarded = true
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	rt.active.Store(true)

	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(1, 1), "Qwen/Test")
	g.settings.ModelLimits = []GatewayModelLimitSettings{{ModelID: "Qwen/Test", AccessMode: string(gatewayAccessModeOpen)}}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello world"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, forwarded)
}

func TestGatewayMetricsCollectorIncludesHostConnectionSnapshots(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(0, 0), "Qwen/Test")
	collector := newGatewayMetricsCollectorWithHostConnections(g, fakeHostConnectionSnapshotter{
		{
			Address:        "10.1.2.3",
			Active:         2,
			Idle:           1,
			HoldAfterClose: 4,
			OpenTotal:      3,
		},
	})

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)

	families, err := registry.Gather()
	require.NoError(t, err)
	requireMetricGaugeValue(t, families, "devshard_host_transport_open_connections", map[string]string{"address": "10.1.2.3"}, 3)
	requireMetricGaugeValue(t, families, "devshard_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "active"}, 2)
	requireMetricGaugeValue(t, families, "devshard_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "idle"}, 1)
	requireMetricGaugeValue(t, families, "devshard_host_transport_connections", map[string]string{"address": "10.1.2.3", "state": "hold_after_close"}, 4)
}

type fakeHostConnectionSnapshotter []transport.HostConnectionSnapshot

func (f fakeHostConnectionSnapshotter) Snapshots() []transport.HostConnectionSnapshot {
	return append([]transport.HostConnectionSnapshot(nil), f...)
}

func requireMetricGaugeValue(t *testing.T, families []*dto.MetricFamily, name string, labels map[string]string, want float64) {
	t.Helper()
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.GetMetric() {
			if metricLabelsMatch(metric, labels) {
				require.NotNil(t, metric.Gauge)
				require.Equal(t, want, metric.Gauge.GetValue())
				return
			}
		}
	}
	t.Fatalf("metric %s with labels %v not found", name, labels)
}

func metricLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	if metric == nil || len(metric.GetLabel()) != len(want) {
		return false
	}
	for _, label := range metric.GetLabel() {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}
