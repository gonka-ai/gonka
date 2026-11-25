package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type alternatingPoster struct {
	mu    sync.Mutex
	posts int
}

func (p *alternatingPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *alternatingPoster) Voteable(uint64) bool { return true }

func (p *alternatingPoster) SettleTimeout(context.Context, TimeoutStep) (TimeoutVote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.posts++
	if p.posts%2 == 0 {
		return TimeoutVote{Deferred: true}, nil
	}
	return TimeoutVote{}, errVoteCause
}

func (p *alternatingPoster) postCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.posts
}

// Test flow:
//  1. Settle one unsettled attempt through a poster whose posts alternate between a refusal and a deferral, so every retry comes back deferred and every deferred post comes back refused.
//  2. Fire each timer as it is scheduled, up to three times the retry budget.
//  3. Assert the engine gave up on the vote within that many timers and paid it in the book, because a deferral does not reset the count of retry rounds already spent.
func TestADeferralDoesNotRestartTheRetryRounds(t *testing.T) {
	const timerBudget = 3 * (refusedVoteRetries + 1)
	poster := &alternatingPoster{}
	races, harness, book := bookedEngine(t, poster, nil)
	attempt := unsettledAttempt()

	races.settle(race(attempt), nil, races.admit())
	for position := range timerBudget {
		require.Eventually(t, func() bool {
			_, paid := book.snapshot()
			return len(paid) > 0 || len(harness.scheduledDelays()) > position
		}, 2*time.Second, time.Millisecond, "timer %d was neither scheduled nor was the vote given up", position)
		if _, paid := book.snapshot(); len(paid) > 0 {
			break
		}
		harness.advance(4 * time.Minute)
		harness.fire(position)
		require.Eventually(t, func() bool { return poster.postCount() > position }, 2*time.Second, time.Millisecond, "timer %d did not post", position)
	}
	awaitPaid(t, book, []uint64{attempt.Nonce})
	races.Stop()
}
