package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// emptyStreamAttempt is an attempt the host acknowledged and then delivered no content on. Whether
// that is the host's fault turns entirely on whether the model reported tokens it never emitted.
func emptyStreamAttempt(completionTokens int64) *inflight {
	attempt := &inflight{sendTime: time.Now()}
	attempt.setReceiptAt(time.Now())
	attempt.usageComplTokens.Store(completionTokens)
	return attempt
}

// The limiter already spares a reasoning burn from quarantine. The ledger did not: both shapes
// reached it as "empty_stream", so a host's delivery score sank for tokens the runtime stripped.
func TestAttemptFailureReason_SeparatesAModelBurnFromAHostsOwnEmptyStream(t *testing.T) {
	burn := emptyStreamAttempt(120)
	hostEmpty := emptyStreamAttempt(0)

	require.Equal(t, "model_burn_empty", gatewayAttemptFailureReason(burn, nil, kimiK26ModelID))
	require.Equal(t, "empty_stream", gatewayAttemptFailureReason(hostEmpty, nil, kimiK26ModelID))
}

// completion_tokens is host-reported, so honouring it outside the reasoning route would let any host
// dodge the empty-stream verdict by inventing usage.
func TestAttemptFailureReason_BurnIsOnlyExcusedOnTheReasoningRoute(t *testing.T) {
	burn := emptyStreamAttempt(120)

	require.Equal(t, "empty_stream", gatewayAttemptFailureReason(burn, nil, "deepseek-ai/DeepSeek-V4-Flash-0731"))
}

// Callers that only write a log line pass no model; folding burn into empty_stream there changes
// nothing they report.
func TestAttemptFailureReason_WithoutAModelBurnStaysFoldedIn(t *testing.T) {
	burn := emptyStreamAttempt(120)

	require.Equal(t, "empty_stream", gatewayAttemptFailureReason(burn, nil, ""))
}
