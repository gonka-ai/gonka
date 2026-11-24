package store

import (
	"bytes"
	"slices"
	"testing"
	"time"
)

func owedVote(nonce uint64, promptBytes int) OwedVote {
	return OwedVote{
		EscrowID: "301", Nonce: nonce, StartedAt: time.Unix(1_700_000_000, 0).UTC(),
		RequestID: "request-1", Participant: "participant-0", Model: "model-a", Prompt: bytes.Repeat([]byte("p"), promptBytes),
	}
}

// Test flow:
//  1. Owe one vote whose prompt is exactly the bound and one a byte over it.
//  2. Drain the writer and assert only the vote at the bound reached the backend.
func TestAnOwedVoteOverThePromptBoundIsNotKept(t *testing.T) {
	backend := &fakeBackend{}
	owedVotes, err := New(backend).NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	owedVotes.Owe(owedVote(1, maxOwedVotePromptBytes))
	owedVotes.Owe(owedVote(2, maxOwedVotePromptBytes+1))
	owedVotes.drain()
	if len(backend.savedVotes) != 1 || backend.savedVotes[0].Nonce != 1 {
		t.Fatalf("saved votes = %d, want the one at the bound only", len(backend.savedVotes))
	}
}

// Test flow:
//  1. Stall the backend's writes and owe more votes than the queue holds.
//  2. Release the writer and drain it.
//  3. Assert every vote not kept is counted as dropped, none as failed, and the kept ones are the queue plus the one the writer held.
func TestAFullOwedVoteQueueShedsInsteadOfBlocking(t *testing.T) {
	backend := &fakeBackend{hold: make(chan struct{})}
	owedVotes, err := New(backend).NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	owed := owedVoteQueueDepth + 10
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		for nonce := range owed {
			owedVotes.Owe(owedVote(uint64(nonce), 16))
		}
	}()
	select {
	case <-returned:
	case <-time.After(stallBound):
		t.Fatal("Owe() blocked behind the stalled writer for 5s")
	}
	close(backend.hold)
	owedVotes.drain()
	kept := len(backend.savedVotes)
	if kept < owedVoteQueueDepth || kept > owedVoteQueueDepth+1 {
		t.Fatalf("kept %d votes of %d owed, want the queue depth %d or one more", kept, owed, owedVoteQueueDepth)
	}
	if stats := owedVotes.Stats(); stats.Dropped != int64(owed-kept) || stats.Failed != 0 {
		t.Fatalf("Stats() = %+v, want Dropped = %d and Failed = 0", stats, owed-kept)
	}
}

// Test flow:
//  1. Make the backend refuse writes, owe one vote, pay another and drain the writer.
//  2. Assert both are counted as failed and none as dropped.
//  3. Owe a vote to the drained writer and assert it is counted as dropped.
func TestAFailedOrLateOwedVoteWriteIsCounted(t *testing.T) {
	owedVotes, err := New(&fakeBackend{voteErr: errRefused}).NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	owedVotes.Owe(owedVote(1, 16))
	owedVotes.Paid("301", 2)
	owedVotes.drain()
	if stats := owedVotes.Stats(); stats.Failed != 2 || stats.Dropped != 0 {
		t.Fatalf("Stats() after two refused writes = %+v, want Failed = 2 and Dropped = 0", stats)
	}
	owedVotes.Owe(owedVote(3, 16))
	if stats := owedVotes.Stats(); stats.Dropped != 1 {
		t.Fatalf("Stats() after owing to a drained writer = %+v, want Dropped = 1", stats)
	}
}

// Test flow:
//  1. Open the writer, owe a vote and pay another, then close the store.
//  2. Assert both writes reached the backend, in order, before it closed.
//  3. Assert a second writer is refused while the first is open.
func TestStoreCloseDrainsTheOwedVotesAndRefusesASecondWriter(t *testing.T) {
	backend := &fakeBackend{}
	composed := New(backend)
	owedVotes, err := composed.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	if _, err := composed.NewOwedVotes(); err == nil {
		t.Fatal("second NewOwedVotes() = nil, want a refusal")
	}
	owedVotes.Owe(owedVote(1, 16))
	owedVotes.Paid("301", 2)
	if err := composed.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
	if len(backend.savedVotes) != 1 || backend.deletedVotes != 1 {
		t.Fatalf("backend saw %d saves and %d deletes, want 1 and 1", len(backend.savedVotes), backend.deletedVotes)
	}
	if want := []string{"save vote", "delete vote", "close"}; !slices.Equal(backend.recorded(), want) {
		t.Fatalf("backend events = %v, want %v", backend.recorded(), want)
	}
}
