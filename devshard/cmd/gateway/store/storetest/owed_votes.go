package storetest

import (
	"bytes"
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/store"
)

func owedVote(nonce uint64, promptBytes int) store.OwedVote {
	return store.OwedVote{
		EscrowID: "301", Nonce: nonce, StartedAt: time.Unix(1_700_000_000, 0).UTC(),
		RequestID: "request-1", Participant: "participant-0", Model: "model-a", Prompt: bytes.Repeat([]byte("p"), promptBytes),
	}
}

// Test flow:
//  1. Open a store, owe three votes and pay the second.
//  2. Close the store, which drains the writer, and open a new one on the same database.
//  3. Assert the new store loads the first and third votes, every field and the prompt as written.
func anOwedVoteOutlivesTheStoreUntilItIsPaid(t *testing.T, open Opener) {
	gatewayStore := open()
	owedVotes, err := gatewayStore.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	for nonce := uint64(1); nonce <= 3; nonce++ {
		owedVotes.Owe(owedVote(nonce, 200))
	}
	owedVotes.Paid("301", 2)
	if err := gatewayStore.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}

	reopened := open()
	reopenedVotes, err := reopened.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes(reopened) = %v, want nil", err)
	}
	votes, err := reopenedVotes.Load(context.Background())
	if err != nil || len(votes) != 2 {
		t.Fatalf("Load() = %d votes, %v, want the two unpaid", len(votes), err)
	}
	for index, nonce := range []uint64{1, 3} {
		want := owedVote(nonce, 200)
		got := votes[index]
		if got.EscrowID != want.EscrowID || got.Nonce != want.Nonce || !got.StartedAt.Equal(want.StartedAt) || got.RequestID != want.RequestID ||
			got.Participant != want.Participant || got.Model != want.Model || !bytes.Equal(got.Prompt, want.Prompt) {
			t.Fatalf("Load()[%d] = %+v, want %+v", index, got, want)
		}
	}
}
