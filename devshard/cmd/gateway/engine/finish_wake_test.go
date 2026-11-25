package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fixedDuePoster struct {
	*simPoster
	due time.Time
}

func (p fixedDuePoster) VoteDeadline(uint64, time.Time) time.Time { return p.due }

// Test flow:
//  1. Run a served winner whose nonce stays open, and assert its execution vote is armed 30 minutes out.
//  2. Apply the winner's finish and assert the vote leaves the queue without waiting out the 30 minutes.
func TestHeldVoteLeavesTheQueueWhenItsFinishApplies(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	harness := newQueueHarness()
	sim.engine.settles = harness.queue
	due := harness.now().Add(30 * time.Minute)
	sim.engine.deps.Timeouts = func(string, any) (TimeoutPoster, bool) {
		return fixedDuePoster{simPoster: sim.poster, due: due}, true
	}
	fireVoteOnCleanup(t, harness)
	runServedWinner(t, sim, false)
	awaitScheduled(t, harness, 1)
	require.Equal(t, []time.Duration{30 * time.Minute}, harness.scheduledDelays())

	sim.target.finish(lateFinishNonce)
	sim.engine.OnFinishApplied("escrow-1", lateFinishNonce)
	require.Eventually(t, func() bool { return sim.engine.OwedTimeoutVotes() == 0 }, 2*time.Second, time.Millisecond)
}
