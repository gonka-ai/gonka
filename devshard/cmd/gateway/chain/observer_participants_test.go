package chain

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func (s *phaseObserverStub) hits() (epoch, participants int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epochHits, s.participantsHits
}

func participantsJSONForEpoch(inferenceURL string, epochID string) string {
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
	}`, epochID, inferenceURL)
}

func newClockedObserver(t *testing.T, server *httptest.Server, chainReader Reader, clock *fakeClock) *PhaseObserver {
	t.Helper()
	cfg := ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	}
	if chainReader != nil {
		cfg.Chain = chainReader
	}
	observer, err := NewPhaseObserver(cfg)
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	return observer
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
	_, sameEpoch := stub.hits()
	stub.setEpoch(http.StatusOK, observerEpochJSON(50, 5, EpochPhaseInference))
	observer.refresh(context.Background())
	epochHits, nextEpoch := stub.hits()

	if sameEpoch != 1 {
		t.Errorf("participants fetched %d times within epoch 4, want 1", sameEpoch)
	}
	if nextEpoch != 2 {
		t.Errorf("participants fetched %d times after epoch 5 began, want 2", nextEpoch)
	}
	if epochHits != 4 {
		t.Errorf("epoch info fetched %d times, want 4", epochHits)
	}
	if got := observer.Snapshot().InferenceURLs["gonka1abc"]; got != server.URL {
		t.Errorf("InferenceURLs[gonka1abc] = %q, want %q from the cached participants", got, server.URL)
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
	_, staleEpoch := stub.hits()

	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 7))
	observer.refresh(context.Background())
	clock.Advance(participantsUnverifiedMaxAge / 2)
	observer.refresh(context.Background())
	_, withinMinute := stub.hits()
	clock.Advance(participantsUnverifiedMaxAge)
	observer.refresh(context.Background())
	_, afterMinute := stub.hits()

	if staleEpoch != 2 {
		t.Errorf("participants fetched %d times while they reported an older epoch, want 2", staleEpoch)
	}
	if withinMinute != 3 {
		t.Errorf("participants fetched %d times within a minute of an unverified answer, want 3", withinMinute)
	}
	if afterMinute != 4 {
		t.Errorf("participants fetched %d times after the unverified answer aged out, want 4", afterMinute)
	}
}

// Test flow:
//  1. Make the public API answer 429 for epoch info and participants, with the chain client serving both.
//  2. Refresh, then refresh again inside the backoff, then once after it ends with the public API healthy again.
//  3. Assert the API is asked for epoch info once on the first refresh and the participants read that follows goes straight to the chain client.
//  4. Assert the API is skipped inside the backoff and asked again after it.
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
	firstEpoch, firstParticipants := stub.hits()
	clock.Advance(publicAPIThrottleBackoff / 2)
	observer.refresh(context.Background())
	insideEpoch, _ := stub.hits()
	clock.Advance(publicAPIThrottleBackoff)
	stub.setEpoch(http.StatusOK, observerEpochJSON(1001, 7, EpochPhaseInference))
	observer.refresh(context.Background())
	afterEpoch, _ := stub.hits()

	if firstEpoch != 1 || firstParticipants != 0 {
		t.Errorf("first refresh asked the API for epoch %d and participants %d times, want 1 and 0", firstEpoch, firstParticipants)
	}
	if insideEpoch != 1 {
		t.Errorf("epoch info asked of the API %d times inside the backoff, want 1", insideEpoch)
	}
	if afterEpoch != 2 {
		t.Errorf("epoch info asked of the API %d times after the backoff, want 2", afterEpoch)
	}
	if snapshot := observer.Snapshot(); snapshot.BlockHeight != 1001 || snapshot.LastError != "" {
		t.Errorf("snapshot at block %d with LastError %q, want block 1001 from the API and no error", snapshot.BlockHeight, snapshot.LastError)
	}
}

// Test flow:
//  1. Make the public API answer 429 for epoch info while the chain client serves it, and refresh to start the backoff.
//  2. Make the chain client fail and the public API healthy, and refresh inside the backoff.
//  3. Assert the failed chain read ends the backoff: the API is asked again and the snapshot comes from it.
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
	}
	observer := newClockedObserver(t, server, chainClient, clock)

	observer.refresh(context.Background())
	chainClient.epochFailure = fmt.Errorf("node unavailable")
	stub.setEpoch(http.StatusOK, observerEpochJSON(1001, 7, EpochPhaseInference))
	observer.refresh(context.Background())
	epochHits, _ := stub.hits()

	if epochHits != 2 {
		t.Errorf("epoch info asked of the API %d times, want 2", epochHits)
	}
	if snapshot := observer.Snapshot(); snapshot.BlockHeight != 1001 {
		t.Errorf("BlockHeight = %d, want 1001 from the public API", snapshot.BlockHeight)
	}
}
