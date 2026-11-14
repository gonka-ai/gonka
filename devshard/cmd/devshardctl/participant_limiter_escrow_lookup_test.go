package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/transport"
)

const lookupLimitedChatPath = "/sessions/1/chat/completions"

func observeLookupLimited(l *ParticipantRequestLimiter, key string) {
	l.ObserveResultWithBodyForModel(key, "", lookupLimitedChatPath,
		http.StatusTooManyRequests, "too many escrow lookups", transport.DevshardErrorEscrowLookupLimited, "")
}

func TestParticipantRequestLimiterEscrowLookupLimitedIsAStrike(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	observeLookupLimited(limiter, "host")

	require.False(t, limiter.IsBlocked("host"), "one escrow_lookup_limited must not quarantine the host")
	require.NoError(t, limiter.AllowRequest("host", lookupLimitedChatPath))
	snap := limiter.Snapshot([]string{"host"})["host"]
	require.False(t, snap.Quarantined)
	require.Equal(t, 1, snap.FailureStrikes)
}

func TestParticipantRequestLimiterRepeatedEscrowLookupLimitedQuarantines(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	for i := 0; i < participantFailureStrikeThreshold-1; i++ {
		observeLookupLimited(limiter, "host")
		require.False(t, limiter.IsBlocked("host"), "strike %d", i+1)
	}
	observeLookupLimited(limiter, "host")

	require.True(t, limiter.IsBlocked("host"), "a host that keeps answering escrow_lookup_limited reaches the 429 quarantine")
	snap := limiter.Snapshot([]string{"host"})["host"]
	require.True(t, snap.Quarantined)
	require.Greater(t, time.Duration(snap.QuarantineRemainingMS)*time.Millisecond, httpThrottleQuarantine-time.Minute)
}

func TestParticipantRequestLimiterSuccessClearsEscrowLookupStrike(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	observeLookupLimited(limiter, "host")
	limiter.ObserveSuccessfulInference("host")
	observeLookupLimited(limiter, "host")
	observeLookupLimited(limiter, "host")

	require.False(t, limiter.IsBlocked("host"), "a success between refusals resets the streak")
}

func TestParticipantRequestLimiterPlain429StillQuarantines(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	limiter.ObserveResultWithBodyForModel("host", "", lookupLimitedChatPath,
		http.StatusTooManyRequests, "too many requests", "", "")
	require.True(t, limiter.IsBlocked("host"))
}

func TestParticipantRequestLimiterEscrowLookupCodeOnOtherStatusQuarantines(t *testing.T) {
	limiter := NewParticipantRequestLimiter(2, 10)
	limiter.ObserveResultWithBodyForModel("host", "", lookupLimitedChatPath,
		http.StatusServiceUnavailable, "busy", transport.DevshardErrorEscrowLookupLimited, "")
	require.True(t, limiter.IsBlocked("host"), "the code softens only a 429")
}
