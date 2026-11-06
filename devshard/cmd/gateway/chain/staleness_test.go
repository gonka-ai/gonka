package chain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test flow:
//  1. Start a stub public-API server returning epoch and participants data, and a fake clock at a fixed instant.
//  2. Create a PhaseObserver against the stub with an hour-long poll interval and the fake clock.
//  3. Run one refresh and assert LastHealthyAt is set to that instant.
//  4. Make the participants read fail, advance the clock, and run refresh again.
//  5. Assert LastUpdatedAt moves to the new instant but LastHealthyAt stays at the last complete poll, and an error is published.
func TestOnlyACompletePollRefreshesTheHealthyClock(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1000, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 42))

	clock := newFakeClock(time.Unix(100, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver: %v", err)
	}

	observer.refresh(context.Background())
	healthy := observer.Snapshot().LastHealthyAt
	if !healthy.Equal(time.Unix(100, 0)) {
		t.Fatalf("LastHealthyAt after a complete poll = %v, want the poll's own instant", healthy)
	}

	stub.setParticipants(http.StatusInternalServerError, "")
	clock.Advance(time.Minute)
	observer.refresh(context.Background())

	published := observer.Snapshot()
	if !published.LastUpdatedAt.Equal(time.Unix(160, 0)) {
		t.Errorf("LastUpdatedAt = %v, want the failed poll's instant", published.LastUpdatedAt)
	}
	if !published.LastHealthyAt.Equal(healthy) {
		t.Errorf("LastHealthyAt = %v, want the last complete poll's %v: a poll whose participants read failed does not refresh the age", published.LastHealthyAt, healthy)
	}
	if published.LastError == "" {
		t.Error("a failed participants read published no error")
	}
}

// Test flow:
//  1. Start a stub public-API server returning epoch and participants data, with the chain's max-nonce lookup failing.
//  2. Create a PhaseObserver against the stub with an hour-long poll interval, the failing chain and a fake clock.
//  3. Run one refresh.
//  4. Assert LastHealthyAt still reflects the poll's instant and the tolerated failure is reported as an error.
func TestANonceCeilingFailureStillRefreshesTheHealthyClock(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1000, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, observerParticipantsJSON("gonka1abc", server.URL, 42))
	stub.setMaxNonceValue(0, false, errors.New("params unavailable"))

	clock := newFakeClock(time.Unix(100, 0))
	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: server.URL,
		PollInterval:     time.Hour,
		HTTPClient:       server.Client(),
		Chain:            stub,
		Now:              clock.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver: %v", err)
	}

	observer.refresh(context.Background())

	published := observer.Snapshot()
	if !published.LastHealthyAt.Equal(time.Unix(100, 0)) {
		t.Errorf("LastHealthyAt = %v, want the poll's instant: the nonce ceiling falls back within the poll and does not hold it back", published.LastHealthyAt)
	}
	if published.LastError == "" {
		t.Error("the tolerated failure was not reported")
	}
}

// Test flow:
//  1. Start an upstream server whose handler blocks until the request context is done.
//  2. Create and start a PhaseObserver against it with a short poll interval.
//  3. Wait until the hung request is reached, then poll the observer's snapshot until it reports an error within a bounded deadline.
//  4. Assert the deadline error appears; fail the test if it never does.
func TestAHungReadDoesNotWedgeThePollLoop(t *testing.T) {
	reached := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(reached)
		<-r.Context().Done()
	}))
	defer upstream.Close()

	observer, err := NewPhaseObserver(ObserverConfig{
		PublicAPIBaseURL: upstream.URL,
		HTTPClient:       upstream.Client(),
		PollInterval:     50 * time.Millisecond,
		Now:              time.Now,
	})
	if err != nil {
		t.Fatalf("NewPhaseObserver: %v", err)
	}

	observer.Start(context.Background())
	defer observer.Stop()

	<-reached
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if observer.Snapshot().LastError != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the poll loop never reported a deadline: one hung read wedges it and the shard refuses everything")
}
