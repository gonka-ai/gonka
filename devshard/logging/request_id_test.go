package logging

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestIDHelpers(t *testing.T) {
	ctx := WithRequestID(context.Background(), "req-test")
	id, ok := RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-test", id)

	dst := PropagateRequestID(context.Background(), ctx)
	propagated, ok := RequestID(dst)
	require.True(t, ok)
	require.Equal(t, "req-test", propagated)
}

func TestWithRequestIDGeneratesStableID(t *testing.T) {
	ctx := WithRequestID(context.Background())
	first, ok := RequestID(ctx)
	require.True(t, ok)
	require.NotEmpty(t, first)

	ctx = WithRequestID(ctx, "ignored")
	second, ok := RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, first, second)
}
