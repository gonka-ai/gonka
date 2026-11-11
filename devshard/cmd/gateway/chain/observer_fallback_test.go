package chain

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

const fallbackTestPollDeadline = 5 * time.Second

type fallbackChain struct {
	*phaseObserverStub
	epochBody         string
	participantsBody  string
	epochFailure      error
	participantsError error
	epochHangs        bool
}

func (c fallbackChain) epochInfoPayload(ctx context.Context) (chainEpochInfoResponse, error) {
	if c.epochHangs {
		<-ctx.Done()
		return chainEpochInfoResponse{}, ctx.Err()
	}
	if c.epochFailure != nil {
		return chainEpochInfoResponse{}, c.epochFailure
	}
	var payload chainEpochInfoResponse
	err := json.Unmarshal([]byte(c.epochBody), &payload)
	return payload, err
}

func (c fallbackChain) participantsPayload(context.Context) (chainCurrentParticipantsResponse, error) {
	if c.participantsError != nil {
		return chainCurrentParticipantsResponse{}, c.participantsError
	}
	var payload chainCurrentParticipantsResponse
	err := json.Unmarshal([]byte(c.participantsBody), &payload)
	return payload, err
}

type hangingChain struct {
	Reader
	hungRead string
}

func (c hangingChain) Models(ctx context.Context) (map[string]ModelParams, error) {
	if c.hungRead == "Models" {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return c.Reader.Models(ctx)
}

func (c hangingChain) EscrowParams(ctx context.Context) (EscrowParams, bool, error) {
	if c.hungRead == "EscrowParams" {
		<-ctx.Done()
		return EscrowParams{}, false, ctx.Err()
	}
	return c.Reader.EscrowParams(ctx)
}

func (c hangingChain) PreservedNodes(ctx context.Context) (*PreservedNodes, bool, error) {
	if c.hungRead == "PreservedNodes" {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}
	return c.Reader.PreservedNodes(ctx)
}

// Test flow:
//  1. Make the public API answer 503 for epoch info and participants, and let the chain client serve both.
//  2. Refresh the observer once.
//  3. Assert the snapshot is healthy: LastHealthyAt advanced, the epoch and the two miners come from the chain, and LastError is empty.
func TestPhaseObserver_PublicAPIDownFallsBackToTheChainClient(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusServiceUnavailable, "")
	stub.setParticipants(http.StatusServiceUnavailable, "")
	chainClient := fallbackChain{
		phaseObserverStub: stub,
		epochBody:         observerEpochJSON(1000, 7, EpochPhaseInference),
		participantsBody:  observerTwoMinerParticipantsJSON(server.URL),
	}

	observer := newPoCPhaseObserver(t, server, chainClient)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if snapshot.LastHealthyAt.IsZero() || !snapshot.LastHealthyAt.Equal(snapshot.LastUpdatedAt) {
		t.Errorf("LastHealthyAt = %v, LastUpdatedAt = %v, want the poll to count as healthy", snapshot.LastHealthyAt, snapshot.LastUpdatedAt)
	}
	if snapshot.EpochIndex != 7 || snapshot.BlockHeight != 1000 {
		t.Errorf("epoch = %d at block %d, want 7 at 1000 from the chain client", snapshot.EpochIndex, snapshot.BlockHeight)
	}
	if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want %v", snapshot.Preserved, want)
	}
	if snapshot.LastError != "" {
		t.Errorf("LastError = %q, want empty", snapshot.LastError)
	}
}

// Test flow:
//  1. Make the public API answer 200 with a participants body that is not JSON, and let the chain client serve the participants.
//  2. Refresh the observer once.
//  3. Assert the participants come from the chain client and the poll counts as healthy.
func TestPhaseObserver_UndecodableParticipantsFallBackToTheChainClient(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochJSON(1000, 7, EpochPhaseInference))
	stub.setParticipants(http.StatusOK, "<html>not json</html>")
	chainClient := fallbackChain{phaseObserverStub: stub, participantsBody: observerTwoMinerParticipantsJSON(server.URL)}

	observer := newPoCPhaseObserver(t, server, chainClient)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want %v", snapshot.Preserved, want)
	}
	if snapshot.LastHealthyAt.IsZero() || snapshot.LastError != "" {
		t.Errorf("LastHealthyAt = %v, LastError = %q, want a healthy poll", snapshot.LastHealthyAt, snapshot.LastError)
	}
}

// Test flow:
//  1. Make the public API answer 503 for epoch info and make the chain client fail too.
//  2. Refresh the observer once.
//  3. Assert LastError names both failures, the public API's and the chain client's, and LastHealthyAt did not advance.
func TestPhaseObserver_EpochInfoFailingEverywhereNamesBothSources(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusServiceUnavailable, "")
	chainClient := fallbackChain{phaseObserverStub: stub, epochFailure: errors.New("node down")}

	observer := newPoCPhaseObserver(t, server, chainClient)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	for _, want := range []string{"fetch epoch info", "status 503", "chain fallback", "node down"} {
		if !strings.Contains(snapshot.LastError, want) {
			t.Errorf("LastError = %q, want substring %q", snapshot.LastError, want)
		}
	}
	if !snapshot.LastHealthyAt.IsZero() {
		t.Errorf("LastHealthyAt = %v, want it untouched", snapshot.LastHealthyAt)
	}
}

// Test flow:
//  1. Make the public API answer 503 for epoch info and make the chain client's epoch read hang until its context ends.
//  2. Refresh the observer under a five-second poll deadline with a 20ms read deadline.
//  3. Assert the refresh publishes before the poll deadline and LastError names the chain fallback.
func TestPhaseObserver_HungChainFallbackIsBoundedByItsOwnDeadline(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusServiceUnavailable, "")
	observer := newPoCPhaseObserver(t, server, fallbackChain{phaseObserverStub: stub, epochHangs: true})
	observer.chainReadTimeout = 20 * time.Millisecond
	pollContext, cancel := context.WithTimeout(context.Background(), fallbackTestPollDeadline)
	defer cancel()

	observer.refresh(pollContext)

	if pollContext.Err() != nil {
		t.Fatalf("the poll deadline expired: the hung chain fallback was not bounded by its own deadline")
	}
	if !strings.Contains(observer.Snapshot().LastError, "chain fallback") {
		t.Errorf("LastError = %q, want the chain fallback named", observer.Snapshot().LastError)
	}
}

// Test flow:
//  1. For each optional chain read (Models, EscrowParams, PreservedNodes) make that read hang until its context ends, with a healthy public API during a PoC generation phase and a 20ms read deadline.
//  2. Refresh the observer under a five-second poll deadline.
//  3. Assert the poll deadline did not expire, the poll counts as healthy, and LastError names the hung read.
func TestPhaseObserver_HungOptionalChainReadDoesNotCostTheSnapshotItsHealth(t *testing.T) {
	cases := []struct {
		hungRead  string
		wantError string
	}{
		{hungRead: "Models", wantError: "fetch governance models"},
		{hungRead: "EscrowParams", wantError: "fetch devshard escrow params"},
		{hungRead: "PreservedNodes", wantError: "fetch preserved snapshot"},
	}
	for _, testCase := range cases {
		t.Run(testCase.hungRead, func(t *testing.T) {
			stub := newPhaseObserverStub()
			server := httptest.NewServer(stub.handler())
			defer server.Close()
			stub.setEpoch(http.StatusOK, observerEpochPoCJSON(EpochPhasePoCGenerate, 950))
			stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
			observer := newPoCPhaseObserver(t, server, hangingChain{Reader: stub, hungRead: testCase.hungRead})
			observer.chainReadTimeout = 20 * time.Millisecond
			pollContext, cancel := context.WithTimeout(context.Background(), fallbackTestPollDeadline)
			defer cancel()

			observer.refresh(pollContext)
			snapshot := observer.Snapshot()

			if pollContext.Err() != nil {
				t.Fatalf("the poll deadline expired: the hung %s read was not bounded by its own deadline", testCase.hungRead)
			}
			if snapshot.LastHealthyAt.IsZero() {
				t.Errorf("LastHealthyAt is zero, want the participants read to have counted as healthy")
			}
			if !strings.Contains(snapshot.LastError, testCase.wantError) {
				t.Errorf("LastError = %q, want substring %q", snapshot.LastError, testCase.wantError)
			}
		})
	}
}
