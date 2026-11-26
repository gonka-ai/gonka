package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParticipantLimiterIgnoresRestrictedToEscrowOwner403(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResultWithBody(
		"good-host",
		"/sessions/23/chat/completions",
		http.StatusForbidden,
		`{"message":"restricted to escrow owner"}`,
	)
	require.False(t, limiter.IsBlocked("good-host"))
	_, prob := limiter.NoWinnerStatusForModel("good-host", "")
	require.False(t, prob)

	// Generic 403 without wrong-owner body still quarantines as before.
	limiter.ObserveResult("other-forbidden-host", "/sessions/23/chat/completions", http.StatusForbidden)
	require.True(t, limiter.IsBlocked("other-forbidden-host"))
}

func TestSuspiciousWinnerReleaseAllowsAllSuspiciousFleetToCrown(t *testing.T) {
	// When every host in the escrow is on probation, a streaming suspicious rival
	// cannot self-crown and must not block releasing an already-finished suspicious answer.
	suspiciousStreamingRival := &inflight{
		nonce:      2,
		suspicious: true,
		sendTime:   time.Now(),
		done:       make(chan struct{}),
	}
	suspiciousStreamingRival.setFirstTokenAt(time.Now())

	winner, released := suspiciousWinnerRelease([]*inflight{
		finishedSuspiciousAttempt(1),
		suspiciousStreamingRival,
	})
	require.True(t, released)
	require.NotNil(t, winner)
	require.Equal(t, uint64(1), winner.nonce)
}

func TestAdminUnquarantineForgetClearsProbation(t *testing.T) {
	limiter := NewParticipantRequestLimiter(10, 10)
	limiter.ObserveResult("host-a", "/sessions/1/chat/completions", http.StatusNotFound)
	require.True(t, limiter.IsBlocked("host-a"))

	gw := &Gateway{participantLimiter: limiter}

	// Default soft unquarantine keeps post-quarantine probation.
	softBody, _ := json.Marshal(map[string]any{"participant_key": "host-a"})
	rec := httptest.NewRecorder()
	gw.handleAdminUnquarantine(rec, httptest.NewRequest(http.MethodPost, "/v1/admin/participants/unquarantine", bytes.NewReader(softBody)))
	require.Equal(t, http.StatusOK, rec.Code)
	require.False(t, limiter.IsBlocked("host-a"))
	_, softProb := limiter.NoWinnerStatusForModel("host-a", "")
	require.True(t, softProb)

	// Forget / clear_probation lifts both quarantine and probation strikes.
	hardBody, _ := json.Marshal(map[string]any{"participant_key": "host-a", "forget": true})
	rec2 := httptest.NewRecorder()
	gw.handleAdminUnquarantine(rec2, httptest.NewRequest(http.MethodPost, "/v1/admin/participants/unquarantine", bytes.NewReader(hardBody)))
	require.Equal(t, http.StatusOK, rec2.Code)
	require.False(t, limiter.IsBlocked("host-a"))
	_, hardProb := limiter.NoWinnerStatusForModel("host-a", "")
	require.False(t, hardProb)
}
