package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
	"devshard/user"
)

func TestTheReasonSeparatesOurRefusalFromTheHosts413(t *testing.T) {
	ours := &inflight{err: fmt.Errorf("send: %w", user.ErrPromptTooLargeForHost)}
	require.Equal(t, "request_too_large", gatewayAttemptFailureReason(ours, nil, "m"),
		"a body we refused to send is not an HTTP outcome at all")

	tail := &inflight{err: fmt.Errorf("send: %w", user.ErrTailTooLargeForHost)}
	require.Equal(t, "request_too_large", gatewayAttemptFailureReason(tail, nil, "m"),
		"every refusal in the family reports the same way, not just the prompt one")

	theirs := &inflight{err: &transport.UpstreamStatusError{
		Path: "/chat/completions", StatusCode: http.StatusRequestEntityTooLarge, Body: "request body too large",
	}}
	require.Equal(t, "http_413", gatewayAttemptFailureReason(theirs, nil, "m"),
		"a 413 from a host we measured as fitting stays the host's answer")
}

func TestAnOversizeRefusalReachesTheClientAs413(t *testing.T) {
	require.Equal(t, http.StatusRequestEntityTooLarge,
		gatewayStatusCodeForError(fmt.Errorf("inference: %w", user.ErrPromptTooLargeForHost)),
		"a prompt no host can read will not become readable on retry, so 502 invites a pointless one")
	require.Equal(t, http.StatusBadGateway,
		gatewayStatusCodeForError(fmt.Errorf("inference: %w", user.ErrTailTooLargeForHost)),
		"a tail that does not fit is the escrow's own state, not the caller's body")
	require.Equal(t, http.StatusBadGateway, gatewayStatusCodeForError(fmt.Errorf("boom")),
		"ordinary upstream failures stay retryable")
}
