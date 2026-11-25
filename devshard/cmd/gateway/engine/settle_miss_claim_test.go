package engine

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type missPoster struct {
	now       func() time.Time
	due       time.Time
	hasFinish bool
	mu        sync.Mutex
	posted    []SettleKind
	deferred  int
}

func (p *missPoster) VoteDeadline(uint64, time.Time) time.Time { return p.due }
func (p *missPoster) Voteable(uint64) bool                     { return true }
func (p *missPoster) SettleTimeout(_ context.Context, step TimeoutStep) (TimeoutVote, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if step.Kind == SettleByMissClaim && step.Proof != nil && p.hasFinish {
		p.posted = append(p.posted, step.Kind)
		return TimeoutVote{Kind: TimeoutKindErrorMiss}, nil
	}
	if p.due.After(p.now()) {
		p.deferred++
		return TimeoutVote{Deferred: true}, nil
	}
	p.posted = append(p.posted, step.Kind)
	return TimeoutVote{}, nil
}

func missProof() *MissProof {
	return &MissProof{ResponsePayload: []byte(`{"events":["data: {\"error\":{}}"]}`), Complete: true}
}

// Test flow:
//  1. Settle a race ending in a signed error through a poster whose vote deadline is 32 minutes ahead.
//  2. Assert the miss claim is scheduled at once, fire it and assert it was posted as a miss claim.
func TestAMissClaimIsPostedAtOnce(t *testing.T) {
	races, harness, book := bookedEngine(t, nil, nil)
	poster := &missPoster{now: harness.now, due: harness.now().Add(32 * time.Minute), hasFinish: true}
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	registration := races.admit()
	races.settle(raceEndingIn(attemptEndingInError(missProof())), nil, registration)
	awaitScheduled(t, harness, 1)
	delay := harness.scheduledDelays()[0]
	require.Zero(t, delay, "a miss claim waits on no deadline")
	harness.fire(0)
	awaitReleased(t, registration)
	stopWithin(t, races, "posted miss claim")
	owed, _ := book.snapshot()
	require.Equal(t, []SettleKind{SettleByMissClaim}, poster.posted)
	require.Empty(t, owed)
}

// Test flow:
//  1. Settle a race ending in a signed error through a poster whose vote deadline is 32 minutes ahead.
//  2. Stop the engine before the queue's timer fires.
//  3. Assert the claim was posted as a miss claim and the race's registration is released.
func TestAMissClaimIsPostedWhenStopArrivesBeforeItsTimer(t *testing.T) {
	races, harness, _ := bookedEngine(t, nil, nil)
	poster := &missPoster{now: harness.now, due: harness.now().Add(32 * time.Minute), hasFinish: true}
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	registration := races.admit()
	races.settle(raceEndingIn(attemptEndingInError(missProof())), nil, registration)
	awaitScheduled(t, harness, 1)
	stopWithin(t, races, "miss claim due at once")
	require.Equal(t, []SettleKind{SettleByMissClaim}, poster.posted)
	require.True(t, registration.released.Load())
}

// Test flow:
//  1. Settle a race ending in a signed error through a poster that has no Finish for the nonce.
//  2. Fire the claim and assert it comes back deferred once and is re-armed at the vote's 32-minute deadline.
//  3. Let the Finish arrive, fire the re-armed step and assert it is still posted as a miss claim.
func TestAMissClaimWithoutFinishWaitsForItsDeadline(t *testing.T) {
	races, harness, _ := bookedEngine(t, nil, nil)
	poster := &missPoster{now: harness.now, due: harness.now().Add(32 * time.Minute)}
	races.deps.Timeouts = func(string, any) (TimeoutPoster, bool) { return poster, true }
	registration := races.admit()
	races.settle(raceEndingIn(attemptEndingInError(missProof())), nil, registration)
	awaitScheduled(t, harness, 1)
	harness.fire(0)
	awaitScheduled(t, harness, 2)
	delays := harness.scheduledDelays()
	require.Equal(t, 32*time.Minute, delays[1], "the fallback vote waits for its deadline")
	require.Equal(t, 1, poster.deferred)
	require.False(t, registration.released.Load())
	poster.mu.Lock()
	poster.hasFinish = true
	poster.mu.Unlock()
	harness.advance(32 * time.Minute)
	harness.fire(1)
	awaitReleased(t, registration)
	require.Equal(t, []SettleKind{SettleByMissClaim}, poster.posted, "a Finish that arrived late is still claimed")
	stopWithin(t, races, "posted late claim")
}
