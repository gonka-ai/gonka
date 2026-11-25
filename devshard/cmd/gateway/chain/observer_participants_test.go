package chain

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func participantsJSONForEpoch(inferenceURL, epochIDJSON string) string {
	return fmt.Sprintf(`{
		"active_participants": {
			"epoch_id": %s,
			"participants": [
				{
					"index": "gonka1abc",
					"inference_url": %q,
					"models": ["model-a"],
					"ml_nodes": [{"ml_nodes": [{"node_id": "node-1", "timeslot_allocation": [true, true], "poc_weight": 7}]}]
				}
			]
		}
	}`, epochIDJSON, inferenceURL)
}

// Test flow:
//  1. Serve epoch 4 with participants that report epoch 4, and refresh three times.
//  2. Move the chain to epoch 5 and refresh once more.
//  3. Assert the participants endpoint was asked once for epoch 4 and once again for epoch 5, while epoch info was asked on every refresh.
func TestPhaseObserver_FetchesParticipantsOncePerEpoch(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"4"`))
	observer := newClockedObserver(t, server, nil, newFakeClock(time.Unix(100, 0)))

	for range 3 {
		observer.refresh(context.Background())
	}
	sameEpoch := stub.participantsHitCount()
	stub.setEpoch(http.StatusOK, observerEpochJSON(50, 5, EpochPhaseInference))
	observer.refresh(context.Background())

	if sameEpoch != 1 {
		t.Errorf("participants fetched %d times within epoch 4, want 1", sameEpoch)
	}
	if got := stub.participantsHitCount(); got != 2 {
		t.Errorf("participants fetched %d times after epoch 5 began, want 2", got)
	}
	if got := stub.epochHitCount(); got != 4 {
		t.Errorf("epoch info fetched %d times, want 4", got)
	}
	if got := observer.Snapshot().InferenceURLs["gonka1abc"]; got != server.URL {
		t.Errorf("InferenceURLs[gonka1abc] = %q, want %q from the cached participants", got, server.URL)
	}
}

// Test flow:
//  1. Serve latest epoch 7 between its PoC start and its set_new_validators, so the effective epoch is 6, with participants reporting epoch 6.
//  2. Refresh twice, then move past set_new_validators so the effective epoch is 7 and refresh again.
//  3. Assert the participants are read once while the effective epoch is 6 and again once it turns 7.
func TestPhaseObserver_KeysParticipantsOnTheEffectiveEpoch(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochSwitchJSON(950, 1000, 2000))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"6"`))
	observer := newClockedObserver(t, server, nil, newFakeClock(time.Unix(100, 0)))

	observer.refresh(context.Background())
	observer.refresh(context.Background())
	beforeSwitch := stub.participantsHitCount()
	stub.setEpoch(http.StatusOK, observerEpochSwitchJSON(1000, 1000, 2000))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"7"`))
	observer.refresh(context.Background())

	if beforeSwitch != 1 {
		t.Errorf("participants fetched %d times while the effective epoch was 6, want 1", beforeSwitch)
	}
	if got := stub.participantsHitCount(); got != 2 {
		t.Errorf("participants fetched %d times after the effective epoch turned 7, want 2", got)
	}
}

// Test flow:
//  1. Serve epoch 4 with participants that report epoch 4, refresh, then refresh again after five minutes.
//  2. Refresh once more after the ten-minute hold has passed.
//  3. Assert a verified answer is held for the whole ten minutes and read again after them.
func TestPhaseObserver_HoldsVerifiedParticipantsForTenMinutes(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	clock := newFakeClock(time.Unix(100, 0))
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 4, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"4"`))
	observer := newClockedObserver(t, server, nil, clock)

	observer.refresh(context.Background())
	clock.Advance(participantsCacheMaxAge / 2)
	observer.refresh(context.Background())
	withinHold := stub.participantsHitCount()
	clock.Advance(participantsCacheMaxAge)
	observer.refresh(context.Background())

	if withinHold != 1 {
		t.Errorf("participants fetched %d times within the hold, want 1", withinHold)
	}
	if got := stub.participantsHitCount(); got != 2 {
		t.Errorf("participants fetched %d times after the hold, want 2", got)
	}
}

// Test flow:
//  1. Serve epoch 5 with participants that still report epoch 4, and refresh twice.
//  2. Serve participants with no epoch id, refresh, refresh again before a minute passes, then after it.
//  3. Assert a stale epoch id is refetched on the next refresh and a missing one is held for one minute.
func TestPhaseObserver_RefetchesParticipantsThatDoNotProveTheirEpoch(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	clock := newFakeClock(time.Unix(100, 0))
	stub.setEpoch(http.StatusOK, observerEpochJSON(42, 5, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"4"`))
	observer := newClockedObserver(t, server, nil, clock)

	observer.refresh(context.Background())
	observer.refresh(context.Background())
	staleEpoch := stub.participantsHitCount()

	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	observer.refresh(context.Background())
	clock.Advance(participantsUnverifiedMaxAge / 2)
	observer.refresh(context.Background())
	withinMinute := stub.participantsHitCount()
	clock.Advance(participantsUnverifiedMaxAge)
	observer.refresh(context.Background())

	if staleEpoch != 2 {
		t.Errorf("participants fetched %d times while they reported an older epoch, want 2", staleEpoch)
	}
	if withinMinute != 3 {
		t.Errorf("participants fetched %d times within a minute of an unverified answer, want 3", withinMinute)
	}
	if got := stub.participantsHitCount(); got != 4 {
		t.Errorf("participants fetched %d times after the unverified answer aged out, want 4", got)
	}
}

// Test flow:
//  1. Make the public API answer 429 for epoch info and participants, with the chain client serving both.
//  2. Refresh, then refresh again inside the backoff, then once after it ends with the public API healthy again and the participants aged out.
//  3. Assert the first refresh asks the API for epoch info once and sends the participants read that follows straight to the chain client.
//  4. Assert the API is skipped inside the backoff and asked for both reads after it.
func TestPhaseObserver_ThrottledPublicAPIIsSkippedForTheBackoff(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	clock := newFakeClock(time.Unix(100, 0))
	stub.setEpoch(http.StatusTooManyRequests, "")
	stub.setParticipants(http.StatusTooManyRequests, "")
	chainClient := fallbackChain{
		phaseObserverStub: stub,
		epochBody:         observerEpochJSON(1000, 7, EpochPhaseInference),
		participantsBody:  participantsJSONForEpoch(server.URL, `"7"`),
	}
	observer := newClockedObserver(t, server, chainClient, clock)

	observer.refresh(context.Background())
	firstEpoch, firstParticipants := stub.epochHitCount(), stub.participantsHitCount()
	clock.Advance(publicAPIThrottleBackoff / 2)
	observer.refresh(context.Background())
	insideEpoch, insideParticipants := stub.epochHitCount(), stub.participantsHitCount()
	clock.Advance(participantsCacheMaxAge)
	stub.setEpoch(http.StatusOK, observerEpochJSON(1001, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"7"`))
	observer.refresh(context.Background())

	if firstEpoch != 1 || firstParticipants != 0 {
		t.Errorf("first refresh asked the API for epoch %d and participants %d times, want 1 and 0", firstEpoch, firstParticipants)
	}
	if insideEpoch != 1 || insideParticipants != 0 {
		t.Errorf("API asked for epoch %d and participants %d times inside the backoff, want 1 and 0", insideEpoch, insideParticipants)
	}
	if got := stub.epochHitCount(); got != 2 {
		t.Errorf("epoch info asked of the API %d times after the backoff, want 2", got)
	}
	if got := stub.participantsHitCount(); got != 1 {
		t.Errorf("participants asked of the API %d times after the backoff, want 1", got)
	}
	if snapshot := observer.Snapshot(); snapshot.BlockHeight != 1001 || snapshot.LastError != "" {
		t.Errorf("snapshot at block %d with LastError %q, want block 1001 from the API and no error", snapshot.BlockHeight, snapshot.LastError)
	}
}

// Test flow:
//  1. Make the public API answer 429 for epoch info while the chain client serves both reads, and refresh to start the backoff.
//  2. Make the chain client's epoch read fail and the public API healthy, and refresh inside the backoff.
//  3. Let the chain client serve epoch info again and refresh once more, still inside the original backoff.
//  4. Assert the failed chain read ended the backoff: both later refreshes ask the public API for epoch info.
func TestPhaseObserver_FailedChainReadEndsTheThrottleBackoff(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	clock := newFakeClock(time.Unix(100, 0))
	stub.setEpoch(http.StatusTooManyRequests, "")
	stub.setParticipants(http.StatusOK, participantsJSONForEpoch(server.URL, `"7"`))
	chainClient := &fallbackChain{
		phaseObserverStub: stub,
		epochBody:         observerEpochJSON(1000, 7, EpochPhaseInference),
		participantsBody:  participantsJSONForEpoch(server.URL, `"7"`),
	}
	observer := newClockedObserver(t, server, chainClient, clock)

	observer.refresh(context.Background())
	chainClient.epochFailure = errors.New("node unavailable")
	stub.setEpoch(http.StatusOK, observerEpochJSON(1001, 7, EpochPhaseInference))
	observer.refresh(context.Background())
	chainClient.epochFailure = nil
	stub.setEpoch(http.StatusOK, observerEpochJSON(1002, 7, EpochPhaseInference))
	observer.refresh(context.Background())

	if got := stub.epochHitCount(); got != 3 {
		t.Errorf("epoch info asked of the API %d times, want 3", got)
	}
	if snapshot := observer.Snapshot(); snapshot.BlockHeight != 1002 {
		t.Errorf("BlockHeight = %d, want 1002 from the public API", snapshot.BlockHeight)
	}
}

// Test flow:
//  1. Start the backoff with a 429, then make the chain client's epoch read fail while the public API still answers 429.
//  2. Refresh inside the backoff.
//  3. Assert the chain client is asked for epoch info once in that refresh, not again after the public API's second 429.
func TestPhaseObserver_ThrottledRefreshAsksTheChainOnce(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	clock := newFakeClock(time.Unix(100, 0))
	stub.setEpoch(http.StatusTooManyRequests, "")
	chainClient := &countingFallbackChain{fallbackChain: fallbackChain{
		phaseObserverStub: stub,
		epochBody:         observerEpochJSON(1000, 7, EpochPhaseInference),
		participantsBody:  participantsJSONForEpoch(server.URL, `"7"`),
	}}
	observer := newClockedObserver(t, server, chainClient, clock)

	observer.refresh(context.Background())
	chainClient.epochFailure = errors.New("node unavailable")
	before := chainClient.epochReads
	observer.refresh(context.Background())

	if got := chainClient.epochReads - before; got != 1 {
		t.Errorf("chain client asked for epoch info %d times in one throttled refresh, want 1", got)
	}
}

type countingFallbackChain struct {
	fallbackChain
	epochReads int
}

func (c *countingFallbackChain) epochInfoPayload(ctx context.Context) (chainEpochInfoResponse, error) {
	c.epochReads++
	return c.fallbackChain.epochInfoPayload(ctx)
}
