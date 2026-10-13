package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/user"
)

func receiptedAttemptWithoutContent(hostIdx int, isEndedByGateway bool) *inflight {
	attempt := &inflight{hostIdx: hostIdx, nonce: 1, resp: &host.HostResponse{}, err: context.Canceled}
	attempt.setReceiptAt(time.Now())
	if isEndedByGateway {
		attempt.cancelByGateway()
	}
	return attempt
}

// Test flow:
//  1. Record three receipted attempts without content that the gateway itself ended, then three a host ended on its own.
//  2. Require the gateway-ended ones to leave the host out of quarantine and to carry the gateway_cancelled reason.
//  3. Require the host-ended ones to quarantine it as an empty stream, as before.
func TestAnAttemptTheGatewayEndedIsNotChargedToItsHost(t *testing.T) {
	cases := []struct {
		name               string
		isEndedByGateway   bool
		isQuarantined      bool
		failureReasonLabel string
	}{
		{name: "ended by the gateway", isEndedByGateway: true, isQuarantined: false, failureReasonLabel: "gateway_cancelled"},
		{name: "ended by the host", isEndedByGateway: false, isQuarantined: true, failureReasonLabel: "empty_stream"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupTestProxyWithClients(t, []user.HostClient{receiptThenHangClient{}, receiptThenHangClient{}})
			redundancy := env.proxy.redundancy
			redundancy.participantLimiter = NewParticipantRequestLimiter(100, 100)
			participantKey := redundancy.participantKeyForHost(0)
			params := defaultParams()

			var attempt *inflight
			for range 3 {
				attempt = receiptedAttemptWithoutContent(0, testCase.isEndedByGateway)
				redundancy.recordSample(attempt, params, false)
			}

			require.Equal(t, testCase.isQuarantined, redundancy.participantLimiter.IsShadowQuarantinedForModel(participantKey, redundancy.model))
			require.Equal(t, testCase.failureReasonLabel, gatewayAttemptFailureReason(attempt, redundancy.session, redundancy.model))
		})
	}
}
