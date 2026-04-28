package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"devshard/transport"
	"devshard/types"
)

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

func TestNewRESTBridgeForProtocolUsesLegacyEscrowEndpointForV0211(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(map[string]any{
			"escrow": map[string]any{
				"id":          "83",
				"creator":     "gonka1creator",
				"amount":      "1000000000",
				"slots":       []string{"gonka1host"},
				"epoch_index": "1",
				"app_hash":    "deadbeef",
				"settled":     false,
			},
			"found": true,
		})
	}))
	t.Cleanup(srv.Close)

	_, err := newRESTBridgeForProtocol(srv.URL, types.ProtocolV0211).GetEscrow("83")
	require.NoError(t, err)
	require.Equal(t, "/productscience/inference/inference/subnet_escrow/83", gotPath)
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

	_, err := newRESTBridgeForProtocol(srv.URL, types.ProtocolV0212).GetEscrow("83")
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

func TestGatewayHandleDevshardFinalizeRequiresNoActiveRequests(t *testing.T) {
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

	req := httptest.NewRequest(http.MethodPost, "/devshard/12/v1/finalize", nil)
	rec := httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusConflict, rec.Code)
	require.False(t, forwarded)

	rt.activeRequests.Store(0)
	rt.active.Store(false)
	rec = httptest.NewRecorder()
	g.handleDevshard(rec, req)

	require.Equal(t, http.StatusNoContent, rec.Code)
	require.True(t, forwarded)
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
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"Qwen/Test","messages":[{"role":"user","content":"hello"}]}`))
	rec := httptest.NewRecorder()

	g.handlePooledChat(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)
	require.Equal(t, "12", rec.Header().Get("X-Devshard-ID"))
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
	g.capacity.SetHostWeights(map[string]float64{"host-a": 1, "host-b": 1}, false)
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
	require.InDelta(t, 2.0, body.Capacity.BaselineWeight, 1e-9)
	require.InDelta(t, 1.0, body.Capacity.TotalWeight, 1e-9)
	require.InDelta(t, 1.0, body.Capacity.LostWeight, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.AvailablePercent, 1e-9)
	require.InDelta(t, 50.0, body.Capacity.LostPercent, 1e-9)
	require.Equal(t, 1, body.Capacity.UnavailableHostCount)
	require.Equal(t, 2, body.Capacity.CurrentWeightMatched)
	require.Equal(t, 0, body.Capacity.CurrentWeightFallback)
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

	require.NoError(t, store.SaveParticipantThrottle("shared-host", 0, time.Now().Add(-time.Hour), 503, time.Time{}, 0))

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
	DefaultRequestMaxTokens = 10_000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
		sharedParticipantRequestLimiter.UpdateSettings(DefaultParticipantThrottleSettings())
		ApplyRedundancySettings(DefaultRedundancySettings())
	})

	body, req, err := normalizeChatRequest([]byte(`{"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 10_000, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":10000`)

	body, req, err = normalizeChatRequest([]byte(`{"max_tokens":10001,"messages":[{"role":"user","content":"hello"}]}`))
	require.NoError(t, err)
	require.EqualValues(t, 10_000, req.MaxTokens)
	require.Contains(t, string(body), `"max_tokens":10000`)
}

func TestFinalizeRuntimeConfigsUsesPerEscrowStorageDirectories(t *testing.T) {
	baseDir := "/tmp/devshardctl"
	runtimes, err := finalizeRuntimeConfigs([]RuntimeConfig{{
		ID:            "12",
		PrivateKeyHex: "abc123",
	}}, "Qwen/Test", baseDir)
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, filepath.Join(baseDir, "escrow-12", "state.db"), runtimes[0].StoragePath)
	require.Equal(t, "Qwen/Test", runtimes[0].Model)
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
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, nil))

	oldDefault := DefaultRequestMaxTokens
	DefaultRequestMaxTokens = 1000
	t.Cleanup(func() {
		DefaultRequestMaxTokens = oldDefault
	})

	limiter := NewGatewayLimiter(2, 200)
	g := NewManagedGateway(nil, limiter, GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, t.TempDir(), store)

	req := httptest.NewRequest(http.MethodPost, "/v1/admin/settings",
		strings.NewReader(`{"chain_rest":"http://node:2317","public_api":"http://api:9900","default_model":"Qwen/Qwen3-235B-A22B-Instruct-2507-FP8","max_concurrent_requests":7,"max_input_tokens_in_flight":700,"default_request_max_tokens":7777,"participant_throttle":{"request_burst":42,"recovery_per_minute":7,"http_quarantine_ms":1100,"transport_failure_quarantine_ms":1200,"empty_stream_quarantine_ms":1300,"stalled_winner_quarantine_ms":1400,"empty_stream_threshold":2},"redundancy":{"receipt_timeout_ms":1500,"first_token_timeout_floor_ms":1600,"per_input_token_first_token_lag_ms":17,"inter_chunk_stall_timeout_ms":1800,"non_stream_response_floor_ms":1900,"per_input_token_response_lag_ms":20,"secondary_wait_after_winner_ms":2100,"parallel_advantage_threshold":0.4,"unresponsive_threshold":0.8}}`))
	rec := httptest.NewRecorder()
	g.handleAdminSettings(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.EqualValues(t, 7777, DefaultRequestMaxTokens)

	snap := limiter.Snapshot()
	require.EqualValues(t, 7, snap.MaxConcurrent)
	require.EqualValues(t, 700, snap.MaxInputTokens)

	state, ok, err := store.LoadState()
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "http://node:2317", state.Settings.ChainREST)
	require.Equal(t, "http://api:9900", state.Settings.PublicAPI)
	require.Equal(t, "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8", state.Settings.DefaultModel)
	require.EqualValues(t, 7777, state.Settings.DefaultRequestMaxTokens)
	require.EqualValues(t, 7, state.Settings.MaxConcurrentRequests)
	require.EqualValues(t, 700, state.Settings.MaxInputTokensInFlight)
	require.EqualValues(t, 42, state.Settings.ParticipantThrottle.RequestBurst)
	require.EqualValues(t, 2, state.Settings.ParticipantThrottle.EmptyStreamQuarantineThreshold)
	require.EqualValues(t, 1500, state.Settings.Redundancy.ReceiptTimeoutMS)
	require.EqualValues(t, 17, state.Settings.Redundancy.PerInputTokenFirstTokenLagMS)
	require.Equal(t, 0.4, state.Settings.Redundancy.ParallelAdvantageThreshold)
	require.Equal(t, 1500*time.Millisecond, ReceiptTimeout)
	require.Equal(t, 17*time.Millisecond, PerInputTokenFirstTokenLag)
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

func TestGatewayMetricsEndpointExposedAndUpdated(t *testing.T) {
	rt := &devshardRuntime{
		id:    "12",
		model: "Qwen/Test",
		handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		}),
	}
	g := NewGateway([]*devshardRuntime{rt}, NewGatewayLimiter(10, 1), "Qwen/Test")
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
