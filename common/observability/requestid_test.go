package observability_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"common/observability"
)

func TestNormalizeRequestID_AcceptsSafeValues(t *testing.T) {
	cases := []string{
		"req-1-2",
		"req-1754660000123456789-42",
		"validate-123",
		"550e8400-e29b-41d4-a716-446655440000",
		"a.b_c:d-E",
		strings.Repeat("a", observability.MaxRequestIDLength),
	}
	for _, in := range cases {
		got, ok := observability.NormalizeRequestID(in)
		require.True(t, ok, "accepted: %q", in)
		require.Equal(t, in, got)
	}
}

func TestNormalizeRequestID_TrimsSurroundingSpace(t *testing.T) {
	got, ok := observability.NormalizeRequestID("  req-trimmed  ")
	require.True(t, ok)
	require.Equal(t, "req-trimmed", got)
}

func TestNormalizeRequestID_RejectsInvalid(t *testing.T) {
	cases := []string{
		"",
		"   ",
		"req id",
		"req\nid",
		"req\rid",
		"req\tid",
		"req/id",
		"req@id",
		"req\"id",
		"req'id",
		"req;id",
		"req\\id",
		strings.Repeat("a", observability.MaxRequestIDLength+1),
		"req-" + strings.Repeat("x", observability.MaxRequestIDLength),
	}
	for _, in := range cases {
		got, ok := observability.NormalizeRequestID(in)
		require.False(t, ok, "rejected: %q", in)
		require.Empty(t, got)
	}
}

func TestSetRequestID_RejectsInvalid(t *testing.T) {
	ctx := observability.SetRequestID(context.Background(), "req-ok")
	id, ok := observability.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "req-ok", id)

	ctx2 := observability.SetRequestID(ctx, "bad id\n")
	id2, ok := observability.RequestID(ctx2)
	require.True(t, ok)
	require.Equal(t, "req-ok", id2, "invalid SetRequestID must leave existing id unchanged")

	ctx3 := observability.SetRequestID(context.Background(), strings.Repeat("x", observability.MaxRequestIDLength+1))
	_, ok = observability.RequestID(ctx3)
	require.False(t, ok)
}

func TestWithRequestID_MintsWhenExplicitInvalid(t *testing.T) {
	ctx, id := observability.WithRequestID(context.Background(), "not valid!")
	require.NotEmpty(t, id)
	require.True(t, strings.HasPrefix(id, "req-"))
	got, ok := observability.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, id, got)
}

func TestWithRequestID_AcceptsExplicitValid(t *testing.T) {
	ctx, id := observability.WithRequestID(context.Background(), "validate-99")
	require.Equal(t, "validate-99", id)
	got, ok := observability.RequestID(ctx)
	require.True(t, ok)
	require.Equal(t, "validate-99", got)
}
