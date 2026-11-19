package warmup

import (
	"context"
	"testing"
	"time"

	"devshard/cmd/gateway/engine"
)

const stopWaitLimit = 5 * time.Second

type contextPoster struct {
	onSettle func(ctx context.Context)
}

func (p *contextPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *contextPoster) SettleTimeout(ctx context.Context, _ engine.TimeoutStep) (engine.TimeoutVote, error) {
	p.onSettle(ctx)
	return engine.TimeoutVote{}, ctx.Err()
}

func warmupVotingThrough(poster *contextPoster) *Prober {
	warmup := unfinishedWarmup(&stubPoster{}, &spyTimeouts{}, true, true)
	warmup.posters = func(string, any) (engine.TimeoutPoster, bool) { return poster, true }
	return warmup
}

// Test flow:
//  1. Build a warmup whose receipted probe fails, so it votes an execution timeout that is due long after the warmup's own budget.
//  2. Warm one escrow and capture the context the vote is posted under.
//  3. Assert that context carries no deadline: the budget bounds the wait for the group and the probe, never the vote, or the vote is cut before it is due.
func TestTheWarmupVoteOutlivesTheWarmupBudget(t *testing.T) {
	var voteHasDeadline bool
	poster := &contextPoster{onSettle: func(ctx context.Context) { _, voteHasDeadline = ctx.Deadline() }}

	warmupVotingThrough(poster).warm("escrow-1", "model-a")

	if voteHasDeadline {
		t.Fatal("the vote was posted under a deadline, want it bounded only by the gateway's shutdown")
	}
}

// Test flow:
//  1. Build a warmup whose receipted probe fails, so it votes an execution timeout.
//  2. While the vote waits, stop the gateway.
//  3. Assert the vote's context is cancelled: a vote with no deadline still ends with the process.
func TestStoppingTheGatewayCancelsTheWarmupVote(t *testing.T) {
	stop := make(chan struct{})
	var voteCancelled bool
	poster := &contextPoster{onSettle: func(ctx context.Context) {
		close(stop)
		select {
		case <-ctx.Done():
			voteCancelled = true
		case <-time.After(stopWaitLimit):
		}
	}}
	warmup := warmupVotingThrough(poster)
	warmup.stop = stop

	warmup.warm("escrow-1", "model-a")

	if !voteCancelled {
		t.Fatal("stopping the gateway left the warmup vote running")
	}
}
