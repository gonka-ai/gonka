package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// Test flow:
//  1. Build a served winner whose Finish is only queued, plan its timeout votes and assert the plan posts one.
//  2. Build a scripted handler that reports the nonce's Finish as applied, as it would be after the deadline wait.
//  3. Settle the plan through a `SessionTimeouts` over the scripted handler.
//  4. Assert the vote handler never ran and the reported events are one started and one skipped for the finished nonce.
func TestAFinishAppliedWhileTheVoteWaitedIsNotVoted(t *testing.T) {
	winner := cleanAttempt()
	winner.NonceFinished, winner.FinishApplied = true, false
	outcome := race(winner)
	plan := outcome.TimeoutPlan()
	require.Len(t, plan, 1)
	require.True(t, plan[0].Post, "a Finish still queued must plan a vote")
	handler := &scriptedTimeoutHandler{finishApplied: true}

	events := settleEvents(outcome, &SessionTimeouts{handler: handler})

	require.Zero(t, handler.calls, "no vote may be sent for a nonce whose Finish applied")
	require.Zero(t, handler.missCalls)
	require.Len(t, events, 2)
	require.Equal(t, TimeoutActionStarted, events[0].Action)
	require.Equal(t, TimeoutActionSkipped, events[1].Action)
	require.Equal(t, TimeoutReasonNonceFinished, events[1].Reason)
}

// Test flow:
//  1. Build a `SessionTimeouts` over a handler whose Finish is still queued and settle a plain vote step.
//  2. Assert the vote handler ran once.
//  3. Settle a miss-claim step through a handler whose Finish is applied and holds a signed Finish.
//  4. Assert the miss handler ran once, since an error-miss claim is not gated.
func TestOnlyAVoteIsGatedOnTheAppliedFinish(t *testing.T) {
	queued := &scriptedTimeoutHandler{}
	_, _ = (&SessionTimeouts{handler: queued}).SettleTimeout(t.Context(), TimeoutStep{Nonce: 7})
	require.Equal(t, 1, queued.calls)

	applied := &scriptedTimeoutHandler{finishApplied: true, finishTx: []byte("signed-finish")}
	step := TimeoutStep{Nonce: 7, Kind: SettleByMissClaim, Proof: &MissProof{ResponsePayload: []byte(`{"events":[]}`)}}
	_, err := (&SessionTimeouts{handler: applied}).SettleTimeout(t.Context(), step)

	require.NotErrorIs(t, err, user.ErrNonceFinishedWhileWaiting)
	require.Equal(t, 1, applied.missCalls)
}
