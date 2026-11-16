package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/user"
)

// Test flow:
//  1. Record three receipted attempts without content: ones the gateway cancelled inside their first-token budget, ones it cancelled past that budget, and ones the host ended on its own.
//  2. Require the ones cancelled inside the budget to leave the host out of quarantine and to carry the gateway_cancelled reason.
//  3. Require the others to quarantine it as an empty stream: past its budget the silence is the host's own miss.
func TestAnAttemptTheGatewayEndedInsideItsBudgetIsNotChargedToItsHost(t *testing.T) {
	cases := []struct {
		name               string
		receiptAge         time.Duration
		isCancelledByRace  bool
		isQuarantined      bool
		failureReasonLabel string
	}{
		{name: "cancelled inside its first-token budget", isCancelledByRace: true, isQuarantined: false, failureReasonLabel: "gateway_cancelled"},
		{name: "cancelled past its first-token budget", receiptAge: time.Hour, isCancelledByRace: true, isQuarantined: true, failureReasonLabel: "empty_stream"},
		{name: "ended by the host", isQuarantined: true, failureReasonLabel: "empty_stream"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupTestProxyWithClients(t, []user.HostClient{&receiptThenHangClient{}, &receiptThenHangClient{}})
			redundancy := env.proxy.redundancy
			redundancy.participantLimiter = NewParticipantRequestLimiter(100, 100)
			participantKey := redundancy.participantKeyForHost(0)
			params := defaultParams()

			var attempt *inflight
			for range 3 {
				sentAt := time.Now().Add(-testCase.receiptAge)
				attempt = &inflight{hostIdx: 0, nonce: 1, sendTime: sentAt, resp: &host.HostResponse{}, err: context.Canceled}
				attempt.setReceiptAt(sentAt)
				if testCase.isCancelledByRace {
					redundancy.cancelAttempt(attempt, params)
				}
				redundancy.recordSample(attempt, params, false)
			}

			require.Equal(t, testCase.isQuarantined, redundancy.participantLimiter.IsShadowQuarantinedForModel(participantKey, redundancy.model))
			require.Equal(t, testCase.failureReasonLabel, gatewayAttemptFailureReason(attempt, redundancy.session, redundancy.model))
		})
	}
}
