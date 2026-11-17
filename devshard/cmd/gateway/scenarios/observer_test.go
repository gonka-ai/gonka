package scenarios

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"devshard/cmd/gateway/chain"
)

// participantWeight is each host's PoC weight, large enough that the default per-weight cap admits requests.
const participantWeight = 10_000

// epochLength is the blocks between two switches, which the reply names as the next epoch's stages, as the real one always does.
const epochLength = 1000

// fakePublicAPI answers the observer's two public endpoints from the fake chain, with no network.
type fakePublicAPI struct {
	chain  *fakeChain
	models []string
}

func newFakePublicAPIClient(blockchain *fakeChain, models []string) *http.Client {
	return &http.Client{Transport: fakePublicAPI{chain: blockchain, models: models}}
}

func (api fakePublicAPI) RoundTrip(request *http.Request) (*http.Response, error) {
	var body []byte
	var err error
	switch request.URL.Path {
	case "/v1/epochs/latest":
		if api.chain.publicAPIDown() {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		}
		body, err = json.Marshal(api.epochInfo())
	case "/v1/epochs/current/participants":
		body, err = json.Marshal(api.participants())
	default:
		return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
	}
	if err != nil {
		return nil, err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(string(body))),
		Request:    request,
	}, nil
}

func (api fakePublicAPI) epochInfo() map[string]any {
	epoch := api.chain.snapshotEpoch()
	pocStart := epoch.pocStart
	if api.chain.effectiveHidden() {
		pocStart = 0
	}
	info := map[string]any{
		"block_height":               epoch.blockHeight,
		"phase":                      string(epoch.phase),
		"latest_epoch":               map[string]any{"index": epoch.latest, "poc_start_block_height": pocStart},
		"epoch_stages":               map[string]any{"set_new_validators": epoch.setNewValidators, "next_poc_start": epoch.pocStart},
		"next_epoch_stages":          map[string]any{"set_new_validators": epoch.setNewValidators + epochLength},
		"is_confirmation_poc_active": false,
	}
	if api.chain.confirmationPoCActive() {
		info["is_confirmation_poc_active"] = true
		info["active_confirmation_poc_event"] = map[string]any{"phase": 2, "trigger_height": epoch.blockHeight}
	}
	return info
}

func (api fakePublicAPI) participants() map[string]any {
	listed := make([]map[string]any, 0, len(api.chain.shape.participants))
	for index, address := range api.chain.shape.participants {
		nodes := make([]map[string]any, 0, len(api.models))
		for range api.models {
			nodes = append(nodes, map[string]any{"ml_nodes": []map[string]any{{
				"node_id":             "node-" + address,
				"poc_weight":          participantWeight,
				"timeslot_allocation": []bool{true, true},
			}}})
		}
		listed = append(listed, map[string]any{
			"index":         address,
			"inference_url": "",
			"models":        api.models,
			"ml_nodes":      nodes,
			"position":      index,
		})
	}
	return map[string]any{"active_participants": map[string]any{"participants": listed}}
}

// Test flow:
//  1. Serve a chain at latest epoch 7 with four participants through the in-memory public API.
//  2. Start a real phase observer over it inside a bubble and wait for its first refresh.
//  3. Assert the snapshot carries epoch 7, the block height, the switch height, and a weight for every participant of the model.
func TestThePhaseObserverReadsTheFakeChain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		blockchain := testFakeChain(inferenceEpoch())
		observer, err := chain.NewPhaseObserver(chain.ObserverConfig{
			PublicAPIBaseURL: "http://observer.scenario",
			Chain:            blockchain,
			HTTPClient:       newFakePublicAPIClient(blockchain, []string{"scenario-model"}),
			Now:              time.Now,
		})
		if err != nil {
			t.Fatalf("NewPhaseObserver() = %v, want nil", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		observer.Start(ctx)
		synctest.Wait()
		snapshot := observer.Snapshot()
		cancel()
		observer.Stop()

		if snapshot.EpochIndex != 7 || snapshot.BlockHeight != 1000 || snapshot.EpochSwitchBlockHeight != 2100 {
			t.Fatalf("Snapshot() epoch %d height %d switch %d, want 7 1000 2100", snapshot.EpochIndex, snapshot.BlockHeight, snapshot.EpochSwitchBlockHeight)
		}
		if got := len(snapshot.FullWeightsByModel["scenario-model"]); got != 4 {
			t.Fatalf("len(FullWeightsByModel[scenario-model]) = %d, want 4", got)
		}
		if snapshot.TokenPrice != 1 || snapshot.FeePerNonce != 10 {
			t.Fatalf("Snapshot() price %d fee %d, want 1 10", snapshot.TokenPrice, snapshot.FeePerNonce)
		}
	})
}

// observedSnapshot runs a real phase observer over the fake chain for one refresh and returns what it published; call it inside a bubble.
func observedSnapshot(t *testing.T, blockchain *fakeChain) chain.PhaseSnapshot {
	t.Helper()
	observer, err := chain.NewPhaseObserver(chain.ObserverConfig{
		PublicAPIBaseURL: "http://observer.scenario",
		Chain:            blockchain,
		HTTPClient:       newFakePublicAPIClient(blockchain, []string{"scenario-model"}),
		Now:              time.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver() = %v, want nil", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	observer.Start(ctx)
	synctest.Wait()
	snapshot := observer.Snapshot()
	cancel()
	observer.Stop()
	return snapshot
}

// Test flow:
//  1. Table-driven: a fake chain before PoC, inside PoC, after set_new_validators, and with its effective epoch hidden.
//  2. Read each through a real phase observer inside a bubble.
//  3. Assert the snapshot's effective epoch is the chain's own, or unknown (0) when hidden.
func TestThePhaseObserverDerivesTheFakeChainsEffectiveEpoch(t *testing.T) {
	testCases := []struct {
		name    string
		prepare func(*fakeChain)
		want    uint64
	}{
		{name: "before PoC", prepare: func(*fakeChain) {}, want: 7},
		{name: "inside PoC", prepare: func(blockchain *fakeChain) { blockchain.startPoC() }, want: 7},
		{name: "after set_new_validators", prepare: func(blockchain *fakeChain) {
			blockchain.startPoC()
			blockchain.setNewValidators(3000, 3100)
		}, want: 8},
		{name: "hidden", prepare: func(blockchain *fakeChain) { blockchain.hideEffectiveEpoch() }, want: 0},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				blockchain := testFakeChain(inferenceEpoch())
				testCase.prepare(blockchain)

				snapshot := observedSnapshot(t, blockchain)

				if snapshot.EffectiveEpochIndex != testCase.want {
					t.Fatalf("Snapshot().EffectiveEpochIndex = %d, want %d (chain effective %d)", snapshot.EffectiveEpochIndex, testCase.want, blockchain.snapshotEpoch().effective)
				}
			})
		})
	}
}

// Test flow:
//  1. Take the fake chain's public API down and request the latest epoch.
//  2. Assert it answers 503.
//  3. Bring the API back, turn a confirmation PoC on, and read the epoch info.
//  4. Assert it names the active confirmation PoC triggered at the chain's height.
func TestTheFakePublicAPIGoesDownAndNamesAConfirmationPoC(t *testing.T) {
	blockchain := testFakeChain(inferenceEpoch())
	api := fakePublicAPI{chain: blockchain, models: []string{"scenario-model"}}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://observer.scenario/v1/epochs/latest", nil)
	if err != nil {
		t.Fatalf("http.NewRequestWithContext() = %v, want nil", err)
	}
	blockchain.setPublicAPIDown(true)

	response, err := api.RoundTrip(request)
	if err != nil {
		t.Fatalf("RoundTrip() = %v, want nil", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("RoundTrip() status with the API down = %d, want 503", response.StatusCode)
	}
	blockchain.setPublicAPIDown(false)
	blockchain.setConfirmationPoC(true)
	info := api.epochInfo()
	event, named := info["active_confirmation_poc_event"].(map[string]any)
	if info["is_confirmation_poc_active"] != true || !named || event["trigger_height"] != int64(1000) {
		t.Fatalf("epochInfo() = %v, want an active confirmation PoC triggered at 1000", info)
	}
}
