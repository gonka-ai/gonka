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
