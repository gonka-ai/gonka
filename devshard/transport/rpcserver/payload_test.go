package rpcserver

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/transport/rpcpb"
)

func TestPayloadHandler_ResolvesEscrowOnce(t *testing.T) {
	n := 0
	lookup := countingLookup{core: stubCore{member: true}, n: &n}
	env := newSessionEnvWith(t, lookup, "escrow-1", nil, WithPayloadService(NewPayloadHandler(lookup, func(context.Context, SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		return &rpcpb.GetPayloadResponse{InferenceId: "1"}, nil
	})))
	_, err := env.payload.GetPayload(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetPayloadRequest{InferenceId: "1"}), env.token))
	require.NoError(t, err)
	require.Equal(t, 1, n, "GetPayload must call SessionServerExisting once")
}

func TestPayloadHandler_GetPayloadResolutionMetrics(t *testing.T) {
	labels := map[string]string{"route": rpcGetPayloadRoute, "status": "ok", "reason": "ok"}
	before := metricCounter(t, "devshard_session_resolution_total", labels)
	lookup := stubLookup{core: stubCore{member: true}}
	env := newSessionEnvWith(t, lookup, "escrow-1", nil, WithPayloadService(NewPayloadHandler(lookup, func(context.Context, SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		return &rpcpb.GetPayloadResponse{InferenceId: "1"}, nil
	})))
	_, err := env.payload.GetPayload(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetPayloadRequest{InferenceId: "1"}), env.token))
	require.NoError(t, err)
	require.Equal(t, before+1, metricCounter(t, "devshard_session_resolution_total", labels))
}

func TestPayloadHandler_GroupNonMemberRejected(t *testing.T) {
	lookup := stubLookup{core: stubCore{}}
	env := newSessionEnvWith(t, lookup, "escrow-1", nil, WithPayloadService(NewPayloadHandler(lookup, func(context.Context, SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		t.Fatal("serve must not run for a non-member")
		return nil, nil
	})))
	_, err := env.payload.GetPayload(context.Background(), withSession(
		connect.NewRequest(&rpcpb.GetPayloadRequest{InferenceId: "1"}), env.token))
	require.Error(t, err)
	require.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	require.Contains(t, err.Error(), "restricted to group members")
}
