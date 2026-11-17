package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Observe a host refusing a chat with the peer RPC quota's 429, then another host refusing with a plain 429.
//  2. Read each host's quarantine.
//  3. Assert the quota refusal quarantines only until the budget refills, while the plain 429 keeps the hour-long quarantine.
func TestParticipantRequestLimiterPeerQuotaRefusalQuarantinesUntilTheBudgetRefills(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)

	limiter.ObserveResultWithBodyForModel("quota-host", "", lookupLimitedChatPath, http.StatusTooManyRequests, "rate limit exceeded", "", "")
	limiter.ObserveResultWithBodyForModel("busy-host", "", lookupLimitedChatPath, http.StatusTooManyRequests, "too many requests", "", "")
	snapshots := limiter.Snapshot([]string{"quota-host", "busy-host"})

	quotaRemaining := time.Duration(snapshots["quota-host"].QuarantineRemainingMS) * time.Millisecond
	busyRemaining := time.Duration(snapshots["busy-host"].QuarantineRemainingMS) * time.Millisecond
	require.True(t, snapshots["quota-host"].Quarantined, "a quota refusal still pauses the host until its budget refills")
	require.LessOrEqual(t, quotaRemaining, peerQuotaQuarantine, "a quota refusal must not hold an honest host for the throttle hour")
	require.Greater(t, busyRemaining, httpThrottleQuarantine-time.Minute, "any other 429 keeps the throttle quarantine")
}

// Test flow:
//  1. Observe each refusal a host or its proxy gives a sender that sends too fast, a full peer table, a body that only contains the rate-limit sentence, and two refusals that mean the host itself is busy.
//  2. Read each host's quarantine.
//  3. Assert only the exact sender-rate sentences get the short pause, a full peer table gets its own few minutes, and everything else keeps the hour.
func TestParticipantRequestLimiterTreatsEverySenderRateRefusalAsShort(t *testing.T) {
	testCases := []struct {
		name        string
		status      int
		body        string
		wantMax     time.Duration
		wantAtLeast time.Duration
	}{
		{name: "peer budget spent", status: http.StatusTooManyRequests, body: "rate limit exceeded", wantMax: peerQuotaQuarantine},
		{name: "peer budget spent over HTTP", status: http.StatusTooManyRequests, body: `{"message":"rate limit exceeded"}`, wantMax: peerQuotaQuarantine},
		{name: "proxy zone over Connect", status: http.StatusServiceUnavailable, body: "HTTP status 429 Too Many Requests", wantMax: peerQuotaQuarantine},
		{name: "peer table full", status: http.StatusTooManyRequests, body: "too many peers", wantMax: peerTableFullQuarantine, wantAtLeast: peerTableFullQuarantine - time.Second},
		{name: "sentence inside another body", status: http.StatusTooManyRequests, body: "busy: rate limit exceeded by the model", wantAtLeast: httpThrottleQuarantine - time.Minute},
		{name: "host busy 429", status: http.StatusTooManyRequests, body: "too many requests", wantAtLeast: httpThrottleQuarantine - time.Minute},
		{name: "host busy 503", status: http.StatusServiceUnavailable, body: "no capacity", wantAtLeast: httpThrottleQuarantine - time.Minute},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			limiter := NewParticipantRequestLimiter(2, 10)

			limiter.ObserveResultWithBodyForModel("host", "", lookupLimitedChatPath, testCase.status, testCase.body, "", "")
			remaining := time.Duration(limiter.Snapshot([]string{"host"})["host"].QuarantineRemainingMS) * time.Millisecond

			require.True(t, limiter.IsBlocked("host"))
			require.GreaterOrEqual(t, remaining, testCase.wantAtLeast)
			if testCase.wantMax > 0 {
				require.LessOrEqual(t, remaining, testCase.wantMax)
			}
		})
	}
}
