package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"

	"devshard/logging"
)

// The race runs detached from the client's context so a host stream still drains after a
// disconnect. Detaching must not cost the request its identity, or no inference stage can be
// joined to the id the client was handed.
func TestDetachedInferenceContext_KeepsTheClientRequestID(t *testing.T) {
	clientCtx, clientID := logging.WithRequestID(context.Background())
	clientCtx, cancel := context.WithCancel(clientCtx)

	inferenceCtx := detachedInferenceContext(clientCtx)
	cancel()

	carriedID, carried := logging.RequestID(inferenceCtx)
	require.True(t, carried)
	require.Equal(t, clientID, carriedID)
	require.NoError(t, inferenceCtx.Err(), "the client going away must not cancel the host stream")
}

// RunInference mints an id when none arrives, so a caller without one still gets a usable trace.
func TestDetachedInferenceContext_MintsNothingOfItsOwn(t *testing.T) {
	inferenceCtx := detachedInferenceContext(context.Background())

	_, carried := logging.RequestID(inferenceCtx)
	require.False(t, carried)
}

func TestDetachedInferenceContext_DropsCancelAndDeadlineKeepsSpan(t *testing.T) {
	parent, clientID := logging.WithRequestID(context.Background())
	parent, cancel := context.WithTimeout(parent, time.Hour)
	defer cancel()
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	parent = trace.ContextWithSpanContext(parent, sc)

	inferenceCtx := detachedInferenceContext(parent)
	cancel()

	carriedID, carried := logging.RequestID(inferenceCtx)
	require.True(t, carried)
	require.Equal(t, clientID, carriedID)
	require.NoError(t, inferenceCtx.Err())
	_, hasDeadline := inferenceCtx.Deadline()
	require.False(t, hasDeadline)
	require.Equal(t, sc, trace.SpanContextFromContext(inferenceCtx))
}
