package chain

import (
	"fmt"
	"reflect"
	"testing"
)

type recordingHealthNarrator struct {
	turns []string
}

func (n *recordingHealthNarrator) ChainSnapshotStale(lastError string, epoch uint64, height int64) {
	n.turns = append(n.turns, fmt.Sprintf("stale %s epoch %d height %d", lastError, epoch, height))
}

func (n *recordingHealthNarrator) ChainSnapshotRecovered(epoch uint64, height int64) {
	n.turns = append(n.turns, fmt.Sprintf("recovered epoch %d height %d", epoch, height))
}

// Test flow:
//  1. Create a PhaseObserver and attach a recording health narrator.
//  2. Publish the same stale error twice, then a recovered snapshot twice.
//  3. Assert the narrator recorded only the stale-to-recovered turn, not each repeated publish.
func TestTheHealthEdgeIsNarratedOnlyWhenItTurns(t *testing.T) {
	observer, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}
	narrator := &recordingHealthNarrator{}
	observer.SetNarrator(narrator)

	observer.publish(PhaseSnapshot{LastError: "fetch epoch info: 503"})
	observer.publish(PhaseSnapshot{LastError: "fetch epoch info: 503"})
	observer.publish(PhaseSnapshot{EpochIndex: 7, BlockHeight: 100})
	observer.publish(PhaseSnapshot{EpochIndex: 7, BlockHeight: 105})

	want := []string{"stale fetch epoch info: 503 epoch 0 height 0", "recovered epoch 7 height 100"}
	if !reflect.DeepEqual(narrator.turns, want) {
		t.Fatalf("narrated %v, want %v", narrator.turns, want)
	}
}

// Test flow:
//  1. Create a PhaseObserver without setting a narrator.
//  2. Publish a snapshot carrying an error.
//  3. Assert Snapshot() still reflects the published error.
func TestAnUnnarratedObserverStillPublishes(t *testing.T) {
	observer, err := NewPhaseObserver(ObserverConfig{PublicAPIBaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewPhaseObserver(): %v", err)
	}

	observer.publish(PhaseSnapshot{EpochIndex: 7, LastError: "fetch participants: 503"})

	if got := observer.Snapshot().LastError; got != "fetch participants: 503" {
		t.Fatalf("LastError = %q, want the published snapshot's", got)
	}
}
