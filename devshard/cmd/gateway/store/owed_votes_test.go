package store

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func owedVote(nonce uint64, promptBytes int) OwedVote {
	return OwedVote{
		EscrowID: "301", Nonce: nonce, StartedAt: time.Unix(1_700_000_000, 0).UTC(),
		RequestID: "request-1", Participant: "participant-0", Model: "model-a", Prompt: bytes.Repeat([]byte("p"), promptBytes),
	}
}

func drainedOwedVotes(t *testing.T, owedVotes *OwedVotes) []OwedVote {
	t.Helper()
	owedVotes.drain()
	votes, err := owedVotes.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() = %v, want nil", err)
	}
	return votes
}

// Test flow:
//  1. Open a store, owe three votes and pay the second.
//  2. Close the store, which drains the writer, and open a new one on the same directory.
//  3. Assert the new store loads the first and third votes, every field and the prompt as written.
func TestAnOwedVoteOutlivesTheStoreUntilItIsPaid(t *testing.T) {
	storageDir := t.TempDir()
	gatewayStore, err := Open(storageDir)
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
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

	reopened, err := Open(storageDir)
	if err != nil {
		t.Fatalf("Open(reopened): %v", err)
	}
	t.Cleanup(func() { reopened.Close() })
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

// Test flow:
//  1. Owe one vote whose prompt is exactly maxOwedVotePromptBytes and one a byte over it.
//  2. Drain the writer and assert only the vote at the bound was kept.
func TestAnOwedVoteOverThePromptBoundIsNotKept(t *testing.T) {
	gatewayStore := openTestStore(t)
	owedVotes, err := gatewayStore.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	owedVotes.Owe(owedVote(1, maxOwedVotePromptBytes))
	owedVotes.Owe(owedVote(2, maxOwedVotePromptBytes+1))

	votes := drainedOwedVotes(t, owedVotes)
	if len(votes) != 1 || votes[0].Nonce != 1 {
		t.Fatalf("Load() = %d votes, want the one at the bound only", len(votes))
	}
}

// Test flow:
//  1. Hold the store's only connection in a transaction, so the writer blocks on its first write.
//  2. Owe owedVoteQueueDepth + 10 votes without blocking.
//  3. Release the connection, drain the writer, and assert owedVoteQueueDepth votes were kept, or one more if the writer had taken it: the queue shed the rest instead of blocking the caller.
//  4. Assert `Stats` counts every vote not kept as dropped and no write as failed.
func TestAFullOwedVoteQueueShedsInsteadOfBlocking(t *testing.T) {
	gatewayStore := openTestStore(t)
	owedVotes, err := gatewayStore.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	holding, err := gatewayStore.db.Begin()
	if err != nil {
		t.Fatalf("Begin() = %v, want nil", err)
	}
	owed := owedVoteQueueDepth + 10
	for nonce := range owed {
		owedVotes.Owe(owedVote(uint64(nonce), 16))
	}
	if err := holding.Rollback(); err != nil {
		t.Fatalf("Rollback() = %v, want nil", err)
	}

	votes := drainedOwedVotes(t, owedVotes)
	if len(votes) < owedVoteQueueDepth || len(votes) > owedVoteQueueDepth+1 {
		t.Fatalf("Load() = %d votes of %d owed, want owedVoteQueueDepth = %d or one more", len(votes), owed, owedVoteQueueDepth)
	}
	if stats := owedVotes.Stats(); stats.Dropped != int64(owed-len(votes)) || stats.Failed != 0 {
		t.Fatalf("Stats() = %+v, want Dropped = %d, every vote not kept, and Failed = 0", stats, owed-len(votes))
	}
}

// Test flow:
//  1. Open the writer and drop the table it writes to.
//  2. Owe one vote, pay another and drain the writer.
//  3. Assert both writes are counted as failed and none as dropped.
//  4. Owe one more vote to the drained writer and assert it is counted as dropped.
func TestAFailedOrLateOwedVoteWriteIsCounted(t *testing.T) {
	gatewayStore := openTestStore(t)
	owedVotes, err := gatewayStore.NewOwedVotes()
	if err != nil {
		t.Fatalf("NewOwedVotes() = %v, want nil", err)
	}
	if _, err := gatewayStore.db.Exec(`DROP TABLE owed_refusal_votes`); err != nil {
		t.Fatalf("dropping the table the writer writes to: %v", err)
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
