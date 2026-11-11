package chain

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func observerAllNodesTakenIntoPoCParticipantsJSON(baseURL string) string {
	return strings.Replace(observerTwoMinerParticipantsJSON(baseURL), "[true, true]", "[true, false]", 1)
}

// Test flow:
//  1. Configure an Inference epoch two blocks before the next PoC at 950 and two miners whose every node reports [true, false].
//  2. Run a table over the chain holding the previous episode's snapshot and the chain holding no snapshot yet; refresh with the stub as chain reader.
//  3. Assert both miners stay preserved with their full weight, LastError is empty and requests are not blocked: the snapshot for the coming PoC is not written until its start block.
func TestPhaseObserver_LeadWindowBeforeTheSnapshotExistsKeepsEveryMinerServing(t *testing.T) {
	cases := []struct {
		name          string
		snapshot      *PreservedNodes
		snapshotFound bool
	}{
		{name: "previous episode snapshot", snapshot: observerPreservedSnapshot(900), snapshotFound: true},
		{name: "no snapshot yet"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := newPhaseObserverStub()
			server := httptest.NewServer(stub.handler())
			defer server.Close()
			stub.setEpoch(http.StatusOK, observerEpochBeforePoCJSON(948, 950))
			stub.setParticipants(http.StatusOK, observerAllNodesTakenIntoPoCParticipantsJSON(server.URL))
			stub.setPreservedNodes(testCase.snapshot, testCase.snapshotFound, nil)

			observer := newPoCPhaseObserver(t, server, stub)
			observer.refresh(context.Background())
			snapshot := observer.Snapshot()

			if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
				t.Errorf("Preserved = %v, want every miner %v", snapshot.Preserved, want)
			}
			if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(snapshot.CurrentWeights, want) {
				t.Errorf("CurrentWeights = %v, want full %v", snapshot.CurrentWeights, want)
			}
			if snapshot.LastError != "" {
				t.Errorf("LastError = %q, want empty", snapshot.LastError)
			}
			if snapshot.RequestsBlocked {
				t.Error("RequestsBlocked = true, want false: only the serving set looks ahead")
			}
		})
	}
}

// Test flow:
//  1. Configure an Inference epoch two blocks before the next PoC at 950, two miners whose every node reports [true, false], and no snapshot on chain.
//  2. Refresh and assert both miners serve.
//  3. Publish the snapshot anchored at 950 listing only gonka1abc, refresh again, and assert the serving set is that snapshot.
func TestPhaseObserver_LeadWindowSwitchesToTheSnapshotOnceItIsWritten(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochBeforePoCJSON(948, 950))
	stub.setParticipants(http.StatusOK, observerAllNodesTakenIntoPoCParticipantsJSON(server.URL))
	stub.setPreservedNodes(nil, false, nil)
	observer := newPoCPhaseObserver(t, server, stub)

	observer.refresh(context.Background())
	before := observer.Snapshot()
	stub.setPreservedNodes(observerPreservedSnapshot(950), true, nil)
	observer.refresh(context.Background())
	after := observer.Snapshot()

	if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(before.Preserved, want) {
		t.Errorf("Preserved before the snapshot = %v, want %v", before.Preserved, want)
	}
	if want := []string{"gonka1abc"}; !reflect.DeepEqual(after.Preserved, want) {
		t.Errorf("Preserved after the snapshot = %v, want %v", after.Preserved, want)
	}
	if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 0}; !reflect.DeepEqual(after.CurrentWeights, want) {
		t.Errorf("CurrentWeights after the snapshot = %v, want %v", after.CurrentWeights, want)
	}
}

// Test flow:
//  1. Configure an Inference epoch two blocks before the next PoC at 950 with the two-miner participants and a snapshot read that fails.
//  2. Refresh and assert the legacy lookahead still applies: only the miner keeping a node through PoC serves, and LastError names the failed read.
func TestPhaseObserver_LeadWindowWithFailedSnapshotReadKeepsLegacyLookahead(t *testing.T) {
	stub := newPhaseObserverStub()
	server := httptest.NewServer(stub.handler())
	defer server.Close()
	stub.setEpoch(http.StatusOK, observerEpochBeforePoCJSON(948, 950))
	stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
	stub.setPreservedNodes(nil, false, errors.New("chain unreachable"))

	observer := newPoCPhaseObserver(t, server, stub)
	observer.refresh(context.Background())
	snapshot := observer.Snapshot()

	if want := []string{"gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
		t.Errorf("Preserved = %v, want legacy %v", snapshot.Preserved, want)
	}
	if !strings.Contains(snapshot.LastError, "chain unreachable") {
		t.Errorf("LastError = %q, want substring %q", snapshot.LastError, "chain unreachable")
	}
}

// Test flow:
//  1. Configure an Inference epoch whose confirmation-PoC event is COMPLETED and two miners, one of them without a preserved node.
//  2. Run a table over the chain still holding the event's snapshot and the chain holding none; refresh with the stub as chain reader.
//  3. Assert both miners keep their full weight, LastError is empty and requests are not blocked: the event no longer runs, so every node counts.
func TestPhaseObserver_CompletedConfirmationPoCServesEveryMiner(t *testing.T) {
	cases := []struct {
		name          string
		snapshot      *PreservedNodes
		snapshotFound bool
	}{
		{name: "snapshot still on chain", snapshot: observerPreservedSnapshot(555), snapshotFound: true},
		{name: "no snapshot", snapshotFound: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			stub := newPhaseObserverStub()
			server := httptest.NewServer(stub.handler())
			defer server.Close()
			stub.setEpoch(http.StatusOK, observerEpochConfirmationJSON(ConfirmationPoCCompleted, 555))
			stub.setParticipants(http.StatusOK, observerTwoMinerParticipantsJSON(server.URL))
			stub.setPreservedNodes(testCase.snapshot, testCase.snapshotFound, nil)

			observer := newPoCPhaseObserver(t, server, stub)
			observer.refresh(context.Background())
			snapshot := observer.Snapshot()

			if want := []string{"gonka1abc", "gonka1bcd"}; !reflect.DeepEqual(snapshot.Preserved, want) {
				t.Errorf("Preserved = %v, want every miner %v", snapshot.Preserved, want)
			}
			if want := map[string]float64{"gonka1abc": 100, "gonka1bcd": 40}; !reflect.DeepEqual(snapshot.CurrentWeights, want) {
				t.Errorf("CurrentWeights = %v, want full %v", snapshot.CurrentWeights, want)
			}
			if snapshot.LastError != "" {
				t.Errorf("LastError = %q, want empty", snapshot.LastError)
			}
			if snapshot.RequestsBlocked {
				t.Error("RequestsBlocked = true, want false")
			}
		})
	}
}
