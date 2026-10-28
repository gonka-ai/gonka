package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

type pollBudgetAPI struct {
	blockHeight       atomic.Int64
	phase             atomic.Value
	participantsEpoch atomic.Uint64
	epochStatus       atomic.Int64
	epochHits         atomic.Int64
	participantsHits  atomic.Int64
}

func newPollBudgetAPI(t *testing.T) (*pollBudgetAPI, *httptest.Server) {
	t.Helper()
	api := &pollBudgetAPI{}
	api.blockHeight.Store(150)
	api.phase.Store(epochPhaseInference)
	api.participantsEpoch.Store(11)
	api.epochStatus.Store(http.StatusOK)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/epochs/latest":
			api.epochHits.Add(1)
			if status := int(api.epochStatus.Load()); status != http.StatusOK {
				http.Error(w, "limited", status)
				return
			}
			_, _ = fmt.Fprintf(w, `{
				"block_height":"%d",
				"phase":"%s",
				"latest_epoch":{"index":"12","poc_start_block_height":"100"},
				"epoch_stages":{"epoch_index":"12","set_new_validators":"200","next_poc_start":"300"},
				"next_epoch_stages":{"epoch_index":"13","set_new_validators":"400"}
			}`, api.blockHeight.Load(), api.phase.Load())
		case "/v1/epochs/current/participants":
			api.participantsHits.Add(1)
			_, _ = fmt.Fprintf(w, `{
				"active_participants":{
					"epoch_id":%d,
					"participants":[{
						"index":"gonka1participant",
						"inference_url":"http://participant.example:8080",
						"ml_nodes":[{"ml_nodes":[{"node_id":"n1","poc_weight":"7","timeslot_allocation":[true]}]}]
					}]
				}
			}`, api.participantsEpoch.Load())
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return api, server
}

func newPollBudgetGate(serverURL string) *ChainPhaseGate {
	gate := NewChainPhaseGate(serverURL, 0)
	gate.SetCapacityState(NewCapacityState(), nil)
	return gate
}

func TestDeriveEffectiveEpochIndex(t *testing.T) {
	resp := &chainEpochInfoResponse{
		BlockHeight: 150,
		LatestEpoch: chainLatestEpoch{Index: 12},
		EpochStages: chainEpochStages{SetNewValidators: 200},
	}
	require.Equal(t, uint64(11), deriveEffectiveEpochIndex(resp))
	resp.BlockHeight = 200
	require.Equal(t, uint64(12), deriveEffectiveEpochIndex(resp))
	resp.EpochStages.SetNewValidators = 0
	require.Equal(t, uint64(12), deriveEffectiveEpochIndex(resp))
	resp.LatestEpoch.Index = 0
	resp.EpochStages.SetNewValidators = 500
	require.Equal(t, uint64(0), deriveEffectiveEpochIndex(resp))
}

func TestChainPhaseGateFetchesParticipantsOncePerEffectiveEpoch(t *testing.T) {
	api, server := newPollBudgetAPI(t)
	gate := newPollBudgetGate(server.URL)

	for i := 0; i < 10; i++ {
		gate.refresh()
	}
	require.Equal(t, int64(10), api.epochHits.Load())
	require.Equal(t, int64(1), api.participantsHits.Load())
	require.Equal(t, "http://participant.example:8080", gate.InferenceURLs()["gonka1participant"])

	api.blockHeight.Store(200)
	gate.refresh()
	require.Equal(t, int64(2), api.participantsHits.Load(), "crossing set_new_validators must refetch")

	gate.refresh()
	require.Equal(t, int64(3), api.participantsHits.Load(), "a lagging epoch_id must be retried")

	api.participantsEpoch.Store(12)
	gate.refresh()
	gate.refresh()
	gate.refresh()
	require.Equal(t, int64(4), api.participantsHits.Load())
}

func TestChainPhaseGateRefetchesParticipantsAfterCacheMaxAge(t *testing.T) {
	api, server := newPollBudgetAPI(t)
	gate := newPollBudgetGate(server.URL)

	gate.refresh()
	gate.refresh()
	require.Equal(t, int64(1), api.participantsHits.Load())

	gate.participantsFetchedAt = gate.participantsFetchedAt.Add(-participantsCacheMaxAge)
	gate.refresh()
	require.Equal(t, int64(2), api.participantsHits.Load())
}

func TestChainPhaseGateBacksOffRateLimitedPublicAPI(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		t.Run(fmt.Sprintf("status_%d", status), func(t *testing.T) {
			api, server := newPollBudgetAPI(t)
			api.epochStatus.Store(int64(status))
			gate := newPollBudgetGate(server.URL)
			gate.SetChainQueryClient(&preservedSnapshotStub{
				epochInfoResp: &inferencetypes.QueryEpochInfoResponse{
					BlockHeight: 150,
					Params: inferencetypes.Params{EpochParams: &inferencetypes.EpochParams{
						EpochLength:           100,
						PocStageDuration:      20,
						PocValidationDelay:    5,
						PocValidationDuration: 20,
						SetNewValidatorsDelay: 5,
					}},
					LatestEpoch: inferencetypes.Epoch{Index: 12, PocStartBlockHeight: 100},
				},
			})

			for i := 0; i < 5; i++ {
				gate.refresh()
			}
			require.Equal(t, int64(1), api.epochHits.Load())
			require.Equal(t, uint64(12), gate.Snapshot().EpochIndex)

			gate.publicAPIBackoffUntil = gate.publicAPIBackoffUntil.Add(-publicAPIRateLimitBackoff)
			api.epochStatus.Store(http.StatusOK)
			gate.refresh()
			require.Equal(t, int64(2), api.epochHits.Load())
		})
	}
}

func TestChainPhaseGateDoesNotBackOffWithoutChainClient(t *testing.T) {
	api, server := newPollBudgetAPI(t)
	api.epochStatus.Store(http.StatusServiceUnavailable)
	gate := newPollBudgetGate(server.URL)

	gate.refresh()
	gate.refresh()
	require.Equal(t, int64(2), api.epochHits.Load())
}

func TestChainPhaseGateActivatesVersionsOnlyDuringRelaxedPoC(t *testing.T) {
	api, server := newPollBudgetAPI(t)
	gate := newPollBudgetGate(server.URL)

	api.phase.Store(epochPhasePoCValidate)
	gate.refresh()
	require.False(t, gate.versions.isActive(), "PoC mode off never routes to validation-capable nodes")

	setPoCModeForTest(t, pocRequestModeRelaxed)
	api.phase.Store(epochPhaseInference)
	gate.refresh()
	require.False(t, gate.versions.isActive(), "relaxed mode outside PoC must not poll /v1/versions")

	api.phase.Store(epochPhasePoCGenerate)
	gate.refresh()
	require.True(t, gate.versions.isActive(), "warm the cache from PoC generation on")

	api.phase.Store(epochPhasePoCValidate)
	gate.refresh()
	require.True(t, gate.versions.isActive())

	api.phase.Store(epochPhaseInference)
	gate.refresh()
	require.False(t, gate.versions.isActive())
}

func TestChainPhaseGateMissingEpochIDIsRetriedSoon(t *testing.T) {
	var block atomic.Int64
	var updated atomic.Bool
	block.Store(199)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/epochs/latest":
			_, _ = fmt.Fprintf(w, `{"block_height":%d,"phase":"Inference","latest_epoch":{"index":12,"poc_start_block_height":100},"epoch_stages":{"set_new_validators":200}}`, block.Load())
		case "/v1/epochs/current/participants":
			key := "old"
			if updated.Load() {
				key = "new"
			}
			_, _ = fmt.Fprintf(w, `{"active_participants":{"participants":[{"index":%q,"inference_url":"http://miner"}]}}`, key)
		}
	}))
	defer srv.Close()

	gate := newPollBudgetGate(srv.URL)
	gate.refresh()
	block.Store(200)
	gate.refresh()
	updated.Store(true)
	gate.refresh()
	_, cachedOld := gate.InferenceURLs()["old"]
	require.True(t, cachedOld, "an unverified payload is cached briefly")

	gate.participantsFetchedAt = gate.participantsFetchedAt.Add(-participantsUnverifiedMaxAge)
	gate.refresh()
	_, ok := gate.InferenceURLs()["new"]
	require.True(t, ok, "an unverified payload must be refetched after participantsUnverifiedMaxAge, got %v", gate.InferenceURLs())
}

func TestChainPhaseGateRepublishesRoutingSetWhenVersionsArrive(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	var phase atomic.Value
	phase.Store("Inference")
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/epochs/latest":
			_, _ = fmt.Fprintf(w, `{"block_height":500,"phase":"Inference","latest_epoch":{"index":12,"poc_start_block_height":100},"epoch_stages":{"set_new_validators":200},"active_confirmation_poc_event":{"phase":%q,"trigger_height":450}}`, phase.Load())
		case "/v1/epochs/current/participants":
			_, _ = fmt.Fprintf(w, `{"active_participants":{"epoch_id":12,"participants":[{"index":"m","inference_url":%q,"models":["model"],"ml_nodes":[{"ml_nodes":[{"node_id":"n","poc_weight":100,"timeslot_allocation":[true,false]}]}]}]}}`, srv.URL)
		case "/v1/versions":
			_, _ = fmt.Fprint(w, `{"mlnodes":[{"node_id":"n","poc_validation_inference":true}]}`)
		}
	}))
	defer srv.Close()

	gate := NewChainPhaseGate(srv.URL, time.Millisecond)
	gate.SetCapacityState(NewCapacityState(), nil)
	gate.refresh()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		gate.versions.Run(ctx, time.Millisecond)
	}()
	defer func() {
		cancel()
		<-done
	}()

	phase.Store(confirmationPoCValidation)
	gate.refresh()
	require.Eventually(t, func() bool {
		return gate.versions.IsNodeValidationCapable("m", "n")
	}, time.Second, time.Millisecond)

	gate.refresh()
	require.True(t, isPoCPreservedParticipantForModel("model", "m"))
}

func TestChainPhaseGateRecoveredCapabilityReachesCPoCValidation(t *testing.T) {
	setPoCModeForTest(t, pocRequestModeRelaxed)
	var phase atomic.Value
	phase.Store(confirmationPoCGracePeriod)
	var capable atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/epochs/latest":
			_, _ = fmt.Fprintf(w, `{"block_height":500,"phase":"Inference","latest_epoch":{"index":12,"poc_start_block_height":100},"epoch_stages":{"set_new_validators":200},"active_confirmation_poc_event":{"phase":%q,"trigger_height":450}}`, phase.Load())
		case "/v1/epochs/current/participants":
			_, _ = fmt.Fprintf(w, `{"active_participants":{"epoch_id":12,"participants":[{"index":"m","inference_url":%q,"models":["model"],"ml_nodes":[{"ml_nodes":[{"node_id":"n","poc_weight":100,"timeslot_allocation":[true,false]}]}]}]}}`, srv.URL)
		case "/v1/versions":
			_, _ = fmt.Fprintf(w, `{"mlnodes":[{"node_id":"n","poc_validation_inference":%t}]}`, capable.Load())
		}
	}))
	defer srv.Close()

	gate := newPollBudgetGate(srv.URL)
	now := time.Unix(1000, 0)
	gate.versions.now = func() time.Time { return now }
	gate.refresh()
	gate.versions.Poll(context.Background())
	capable.Store(true)
	now = now.Add(12 * time.Second)
	phase.Store(confirmationPoCGeneration)
	gate.refresh()
	gate.versions.Poll(context.Background())
	now = now.Add(240 * time.Second)
	gate.versions.Poll(context.Background())
	phase.Store(confirmationPoCValidation)
	gate.refresh()
	require.True(t, isPoCPreservedParticipantForModel("model", "m"))
}

type blockingEpochInfoChain struct {
	*preservedSnapshotStub
	block       atomic.Bool
	entered     chan struct{}
	hasDeadline atomic.Bool
}

func (s *blockingEpochInfoChain) EpochInfo(ctx context.Context, req *inferencetypes.QueryEpochInfoRequest, opts ...grpc.CallOption) (*inferencetypes.QueryEpochInfoResponse, error) {
	if !s.block.Load() {
		return s.preservedSnapshotStub.EpochInfo(ctx, req, opts...)
	}
	_, has := ctx.Deadline()
	s.hasDeadline.Store(has)
	close(s.entered)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestChainPhaseGateBackoffChainQueryIsBoundedAndCancelledByStop(t *testing.T) {
	var limited atomic.Bool
	limited.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limited.Load() {
			http.Error(w, "limited", http.StatusServiceUnavailable)
			return
		}
		_, _ = fmt.Fprint(w, `{"block_height":500,"phase":"Inference","latest_epoch":{"index":12}}`)
	}))
	defer srv.Close()

	chain := &blockingEpochInfoChain{
		preservedSnapshotStub: &preservedSnapshotStub{epochInfoResp: &inferencetypes.QueryEpochInfoResponse{
			BlockHeight: 500,
			LatestEpoch: inferencetypes.Epoch{Index: 12, PocStartBlockHeight: 100},
			Params: inferencetypes.Params{EpochParams: &inferencetypes.EpochParams{
				EpochLength:           1000,
				PocStageDuration:      35,
				PocValidationDuration: 240,
			}},
		}},
		entered: make(chan struct{}),
	}
	gate := NewChainPhaseGate(srv.URL, 0)
	gate.SetChainQueryClient(chain)
	_, err := gate.fetchEpochInfo()
	require.NoError(t, err)
	require.True(t, gate.publicAPIBackingOff())

	limited.Store(false)
	chain.block.Store(true)
	done := make(chan *chainEpochInfoResponse, 1)
	go func() {
		resp, _ := gate.fetchEpochInfo()
		done <- resp
	}()
	<-chain.entered
	require.True(t, chain.hasDeadline.Load(), "chain query must carry a deadline")

	gate.lifeCancel()
	select {
	case resp := <-done:
		require.NotNil(t, resp, "a cancelled chain query must fall back to the healthy API")
		require.Equal(t, int64(500), int64(resp.BlockHeight))
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the gate lifecycle must unblock the chain query")
	}
	require.False(t, gate.publicAPIBackingOff(), "a failed chain query must end the backoff")
}
