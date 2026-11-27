package engine

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// Test flow:
//  1. For each skip reason the session returns with a nil error (nonce_closed, nonce_cap_reached, timeout_not_armed), build a served winner whose Finish is only queued and plan its timeout votes.
//  2. Settle the plan through a `SessionTimeouts` over a scripted handler returning that skip.
//  3. Assert the events are one started and one skipped, never completed, with the reason devshardctl records for it.
//  4. Assert the handler ran once and no refused-vote retry is queued.
func TestASessionSkipIsRecordedAsSkippedNotCompleted(t *testing.T) {
	cases := []struct {
		detail     string
		wantReason TimeoutReason
	}{
		{detail: "nonce_closed", wantReason: TimeoutReasonNonceFinished},
		{detail: "nonce_cap_reached", wantReason: "nonce_cap_reached"},
		{detail: "timeout_not_armed", wantReason: "timeout_not_armed"},
	}
	for _, testCase := range cases {
		t.Run(testCase.detail, func(t *testing.T) {
			winner := cleanAttempt()
			winner.NonceFinished, winner.FinishApplied = true, false
			outcome := race(winner)
			handler := &scriptedTimeoutHandler{result: user.TimeoutResult{Outcome: "skipped", DetailReason: testCase.detail}}
			var events []TimeoutEvent

			retries := settleSteps(t.Context(), &SessionTimeouts{handler: handler}, outcome.TimeoutPlan(), false, func(event TimeoutEvent) { events = append(events, event) })

			require.Equal(t, 1, handler.calls)
			require.Empty(t, retries)
			require.Len(t, events, 2)
			require.Equal(t, TimeoutActionStarted, events[0].Action)
			require.Equal(t, TimeoutActionSkipped, events[1].Action)
			require.Equal(t, testCase.wantReason, events[1].Reason)
		})
	}
}

// Test flow:
//  1. Settle a timeout step through a `SessionTimeouts` over a scripted handler that returns a skip together with an error, as a vote cancelled mid-wait does.
//  2. Assert the outcome stays a failure carrying the handler's detail, so only a skip with a nil error changed.
func TestASessionSkipThatCarriesAnErrorStaysAFailure(t *testing.T) {
	handler := &scriptedTimeoutHandler{
		result: user.TimeoutResult{Outcome: "skipped", DetailReason: "context_canceled"},
		err:    errVoteCause,
	}

	vote, err := (&SessionTimeouts{handler: handler}).SettleTimeout(t.Context(), TimeoutStep{Nonce: 7})
	action, reason := TimeoutOutcome(vote, err, false)

	require.Equal(t, TimeoutActionFailed, action)
	require.Equal(t, TimeoutReason("context_canceled"), reason)
}
