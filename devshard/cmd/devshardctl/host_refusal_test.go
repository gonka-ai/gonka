package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build failed attempts whose host refused over Connect: a rate limit and an internal fault.
//  2. Read each attempt's failure reason and its recorded response status.
//  3. Assert both are read as the HTTP status the Connect code stands for, the way an HTTP refusal is.
func TestAConnectRefusalIsReadAsItsHTTPStatus(t *testing.T) {
	rateLimited := &inflight{err: fmt.Errorf("escrow 1 nonce 7: %w", connect.NewError(connect.CodeResourceExhausted, errors.New("rate limit exceeded")))}
	internal := &inflight{err: connect.NewError(connect.CodeInternal, errors.New("boom"))}

	reasons := []string{gatewayAttemptFailureReason(rateLimited, nil, ""), gatewayAttemptFailureReason(internal, nil, "")}
	status, _ := collectFailureResponseMeta(rateLimited)
	body, _ := collectFailureResponseBody(rateLimited)

	if reasons[0] != gatewayHTTPFailureReason(http.StatusTooManyRequests) || reasons[1] != gatewayHTTPFailureReason(http.StatusInternalServerError) {
		t.Fatalf("failure reasons = %v, want the 429 and 500 reasons", reasons)
	}
	if status != http.StatusTooManyRequests || string(body) != "rate limit exceeded" {
		t.Fatalf("response meta = (%d, %q), want (429, \"rate limit exceeded\")", status, body)
	}
}

// Test flow:
//  1. Build failed attempts cancelled over Connect: a loser stopped after the winner and a plain context cancellation.
//  2. Read each attempt's failure reason and recorded response status.
//  3. Assert neither is read as a host refusal, so a cancellation is never charged to the host as an HTTP 500.
func TestAConnectCancellationIsNotAHostRefusal(t *testing.T) {
	cancelled := &inflight{err: connect.NewError(connect.CodeCanceled, context.Canceled)}
	plain := &inflight{err: fmt.Errorf("send: %w", context.Canceled)}

	_, refused := hostRefusal(cancelled.err)
	status, _ := collectFailureResponseMeta(cancelled)

	require.False(t, refused)
	require.NotEqual(t, http.StatusInternalServerError, status)
	require.Equal(t, gatewayAttemptFailureReason(plain, nil, ""), gatewayAttemptFailureReason(cancelled, nil, ""))
}
