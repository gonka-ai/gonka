package engine

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordingVoteBook struct {
	mu   sync.Mutex
	owed []uint64
	paid []uint64
}

func (b *recordingVoteBook) Owe(vote OwedVote) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.owed = append(b.owed, vote.Nonce)
}

func (b *recordingVoteBook) Paid(_ string, nonce uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.paid = append(b.paid, nonce)
}

func (b *recordingVoteBook) snapshot() (owed, paid []uint64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.owed), slices.Clone(b.paid)
}

type heldEscrows struct {
	mu       sync.Mutex
	open     map[string]bool
	holds    int
	releases int
}

func (h *heldEscrows) Target(escrowID string) (DispatchTarget, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.open[escrowID] {
		return nil, nil, false
	}
	h.holds++
	return nil, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.releases++
	}, true
}

func (h *heldEscrows) counts() (holds, releases int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.holds, h.releases
}

func bookedEngine(t *testing.T, poster TimeoutPoster, targets escrowTargets) (*Engine, *queueHarness, *recordingVoteBook) {
	t.Helper()
	deps := completeEngineDeps(t)
	deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, poster != nil }
	book := &recordingVoteBook{}
	deps.OwedVotes = book
	if targets != nil {
		deps.Targets = targets
	}
	races, err := NewEngine(deps)
	require.NoError(t, err)
	harness := newQueueHarness()
	races.settles = harness.queue
	return races, harness, book
}

func awaitPaid(t *testing.T, book *recordingVoteBook, want []uint64) {
	t.Helper()
	require.Eventually(t, func() bool {
		_, paid := book.snapshot()
		return slices.Equal(paid, want)
	}, 2*time.Second, time.Millisecond, "the book's paid nonces must become %v", want)
}

// Test flow:
//  1. Settle a refused attempt through a poster whose vote lands, and assert the book owes its nonce before the timer fires.
//  2. Fire the timer and assert the book was paid that nonce once the vote landed.
func TestAPostedRefusalVoteIsPaidInTheBook(t *testing.T) {
	poster := &failingPoster{}
	races, harness, book := bookedEngine(t, poster, nil)
	attempt := unsettledAttempt()

	races.settle(race(attempt), nil, races.admit())
	owed, paid := book.snapshot()
	require.Equal(t, []uint64{attempt.Nonce}, owed, "the refusal is owed in the book when the race ends")
	require.Empty(t, paid)

	harness.fire(0)
	awaitPosts(t, poster, 1)
	awaitPaid(t, book, []uint64{attempt.Nonce})
	races.Stop()
}

// Test flow:
//  1. Settle a refused attempt through a poster that always fails, fire the first post and wait for its retry to be scheduled.
//  2. Stop the engine, which posts the waiting retry at once and schedules no more.
//  3. Assert the book was never paid the nonce: a vote still owed at a stop stays in the book for the next boot.
func TestAVoteStillOwedAtStopStaysInTheBook(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(10)}
	races, harness, book := bookedEngine(t, poster, nil)

	races.settle(race(unsettledAttempt()), nil, races.admit())
	harness.fire(0)
	awaitScheduled(t, harness, 2)
	races.Stop()

	require.Equal(t, 2, poster.postCount(), "the waiting retry is posted once at Stop")
	_, paid := book.snapshot()
	require.Empty(t, paid, "a vote still owed at Stop is not paid")
}

// Test flow:
//  1. Settle a refused attempt through a poster that always fails and fire the first post and four retry rounds, each once it is scheduled.
//  2. Assert the book was paid the nonce after the last round: a vote the engine gave up on is not replayed.
func TestAVoteGivenUpOnIsPaidInTheBook(t *testing.T) {
	poster := &failingPoster{failures: alwaysFailing(10)}
	races, harness, book := bookedEngine(t, poster, nil)
	attempt := unsettledAttempt()

	races.settle(race(attempt), nil, races.admit())
	for round := range refusedVoteRetries + 1 {
		harness.advance(4 * time.Minute)
		harness.fire(round)
		awaitPosts(t, poster, round+1)
		if round < refusedVoteRetries {
			awaitScheduled(t, harness, round+2)
		}
	}
	awaitPaid(t, book, []uint64{attempt.Nonce})
	races.Stop()
}

// Test flow:
//  1. Replay two owed votes, one on an open escrow and one on an escrow that is not open.
//  2. Assert the vote on the closed escrow was paid at once and only the open escrow is held.
//  3. Fire the replayed vote's timer and assert it was posted, paid, and its escrow hold released.
func TestAReplayedVoteHoldsItsEscrowUntilPosted(t *testing.T) {
	poster := &failingPoster{}
	targets := &heldEscrows{open: map[string]bool{"open-escrow": true}}
	races, harness, book := bookedEngine(t, poster, targets)

	races.ReplayOwedVotes([]OwedVote{
		{EscrowID: "open-escrow", Nonce: 4, StartedAt: testEpoch},
		{EscrowID: "closed-escrow", Nonce: 9, StartedAt: testEpoch},
	})
	_, paid := book.snapshot()
	require.Equal(t, []uint64{9}, paid, "a vote whose escrow is not open is dropped from the book")
	holds, releases := targets.counts()
	require.Equal(t, 1, holds)
	require.Zero(t, releases, "the open escrow stays held until its vote posts")

	harness.fire(0)
	awaitPosts(t, poster, 1)
	awaitPaid(t, book, []uint64{9, 4})
	require.Eventually(t, func() bool { _, released := targets.counts(); return released == 1 }, 2*time.Second, time.Millisecond, "the escrow hold must be released after the replayed vote posts")
	races.Stop()
}
