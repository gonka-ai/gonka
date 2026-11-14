package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/statetest"
	"devshard/internal/testutil"
	"devshard/memwatch"
	"devshard/signing"
	"devshard/types"
	"devshard/user"
)

func TestGatewayMemoryInventorySumsRuntimes(t *testing.T) {
	thin := countedSession(t, "escrow-thin", 1)
	fat := countedSession(t, "escrow-fat", 3)
	rtThin := &devshardRuntime{id: "escrow-thin", session: thin}
	rtFat := &devshardRuntime{id: "escrow-fat", session: fat}
	rtThin.activeUserRequests.Store(1)
	rtFat.activeUserRequests.Store(4)
	rtFat.pendingRaceCleanup.Store(2)

	g := NewGateway([]*devshardRuntime{rtThin, rtFat}, NewGatewayLimiter(10, 1000), "llama")
	release, ok := tryAcquireTimeoutVotePost(timeoutVotePosts)
	require.True(t, ok)
	t.Cleanup(release)

	inv := g.memoryInventory().(gatewayInventory)
	require.Equal(t, 2, inv.Runtimes)
	require.Equal(t, 4, inv.LiveInferences)
	require.Equal(t, "escrow-fat", inv.FattestEscrow)
	require.Equal(t, 3, inv.FattestLive)
	require.Equal(t, 5, inv.ActiveUserRequests)
	require.Equal(t, 2, inv.PendingRaceCleanup)
	require.GreaterOrEqual(t, inv.TimeoutVotePosts, 1)
}

func TestDebugMemStatsKeepsItsKeys(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(10, 1000), "llama")
	rec := httptest.NewRecorder()
	g.handleDebugMemStats(rec, httptest.NewRequest(http.MethodGet, "/v1/debug/memstats", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	for _, key := range []string{
		"heap_inuse",
		"heap_alloc",
		"heap_sys",
		"heap_idle",
		"heap_released",
		"heap_objects",
		"stack_inuse",
		"sys",
		"next_gc",
		"num_gc",
		"gc_cpu_fraction",
		"loaded_runtimes",
		"num_goroutine",
	} {
		_, ok := got[key]
		require.Truef(t, ok, "missing %s in %s", key, rec.Body.String())
	}
	require.Equal(t, got["heap_inuse"].(float64)+got["heap_idle"].(float64), got["heap_sys"].(float64))
	require.Equal(t, float64(0), got["loaded_runtimes"])
}

func TestDebugMemoryServesTheLogger(t *testing.T) {
	g := NewGateway(nil, NewGatewayLimiter(10, 1000), "llama")
	require.True(t, isAdminPath("/v1/debug/memory"))

	rec := httptest.NewRecorder()
	g.handleDebugMemory(rec, httptest.NewRequest(http.MethodGet, "/v1/debug/memory", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	g.memory = memwatch.New(memwatch.Config{Inventory: g.memoryInventory})
	g.memory.Sample()
	rec = httptest.NewRecorder()
	g.handleDebugMemory(rec, httptest.NewRequest(http.MethodGet, "/v1/debug/memory", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	var got gatewayInventory
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	require.Equal(t, 0, got.Runtimes)
}

func countedSession(t *testing.T, escrowID string, live int) *user.Session {
	t.Helper()
	signer := testutil.MustGenerateKey(t)
	hostKey := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{hostKey})
	verifier := signing.NewSecp256k1Verifier()
	sm := statetest.MustStateMachine(t, escrowID, types.SessionConfig{VoteThreshold: 1}, group, 1000, signer.Address(), verifier)
	st := sm.ExportState()
	st.Inferences = make(map[uint64]*types.InferenceRecord, live)
	for i := 0; i < live; i++ {
		st.Inferences[uint64(i+1)] = &types.InferenceRecord{Status: types.StatusPending}
	}
	require.NoError(t, sm.RestoreState(st))

	sess, err := user.NewSession(sm, signer, escrowID, group, []user.HostClient{&user.InProcessClient{}}, verifier)
	require.NoError(t, err)
	t.Cleanup(func() { _ = sess.Close() })
	return sess
}
