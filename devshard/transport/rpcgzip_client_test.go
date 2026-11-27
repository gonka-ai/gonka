package transport_test

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"devshard/storage"
	"devshard/transport"
	"devshard/transport/rpcpb"
	"devshard/transport/rpcserver"
	"devshard/types"
)

func TestRPCClient_GetDiffsGzipBombStopsInflating(t *testing.T) {
	var n atomic.Int32
	// Zeros compress under the 10 MiB client cap and decode past it. A full
	// drain would report the decoded size; stopping says it could not.
	body := make([]byte, transport.DefaultRPCQueryReadMaxBytes+1<<20)
	rpc := newQueryRPCClient(t, queryLookup{
		nDiffs: &n,
		diffs: []types.DiffRecord{{
			Diff: types.Diff{Nonce: 1, UserSig: body},
		}},
	}, transport.EndpointDiffs, 0)

	_, err := rpc.GetDiffs(context.Background(), 1, 1)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unable to determine message size")
	require.False(t, transport.IsRetryableNonInference(err))
	require.Equal(t, int32(1), n.Load(), "over-cap GetDiffs must not retry")
}

func TestRPCClient_GetPayloadGzipBombStopsInflating(t *testing.T) {
	var n atomic.Int32
	// Smallest GetPayload stub is the 32 MiB bucket. The body decodes past
	// that bucket and compresses far under it.
	body := make([]byte, 32<<20+64<<10)
	lookup := largeRPCLookup{core: largeRPCCore{}}
	h := rpcserver.NewPayloadHandler(lookup, func(context.Context, rpcserver.SessionCore, string, *rpcpb.GetPayloadRequest) (*rpcpb.GetPayloadResponse, error) {
		n.Add(1)
		return &rpcpb.GetPayloadResponse{ResponsePayload: body}, nil
	})
	rpc := newLargeRPCClient(t, lookup, transport.EndpointPayload,
		transport.DefaultClientConfig(), rpcserver.WithPayloadService(h))

	_, err := rpc.GetPayload(context.Background(), &rpcpb.GetPayloadRequest{InferenceId: "1"}, 512)
	require.Equal(t, connect.CodeResourceExhausted, connect.CodeOf(err))
	require.Contains(t, err.Error(), "unable to determine message size")
	require.False(t, transport.IsRetryableNonInference(err))
	status, _, _, ok := transport.ConnectApplicationStatus(err)
	require.True(t, ok)
	require.Equal(t, http.StatusRequestEntityTooLarge, status, "validation maps 413 to ErrPayloadTooLarge, not an executor fault")
	require.Equal(t, int32(1), n.Load(), "over-cap GetPayload must not retry")
}

func TestRPCClient_GetDiffsRejectsMoreRecordsThanOnePage(t *testing.T) {
	var n atomic.Int32
	records := make([]types.DiffRecord, storage.DiffPageMaxNonces+1)
	for i := range records {
		records[i] = types.DiffRecord{Diff: types.Diff{Nonce: uint64(i + 1)}}
	}
	rpc := newQueryRPCClient(t, queryLookup{nDiffs: &n, diffs: records}, transport.EndpointDiffs, 0)

	_, err := rpc.GetDiffs(context.Background(), 1, uint64(len(records)))
	require.ErrorIs(t, err, transport.ErrDiffPageOversized)
	require.Equal(t, int32(1), n.Load())
}

func TestRPCClient_GetDiffsAcceptsFullPage(t *testing.T) {
	records := make([]types.DiffRecord, storage.DiffPageMaxNonces)
	for i := range records {
		records[i] = types.DiffRecord{Diff: types.Diff{Nonce: uint64(i + 1)}}
	}
	rpc := newQueryRPCClient(t, queryLookup{diffs: records}, transport.EndpointDiffs, 0)

	diffs, err := rpc.GetDiffs(context.Background(), 1, uint64(len(records)))
	require.NoError(t, err)
	require.Len(t, diffs, storage.DiffPageMaxNonces)
}

func TestRPCClient_GetDiffsRejectsPageOverByteBudget(t *testing.T) {
	half := make([]byte, storage.DiffPageMaxBytes/2+1)
	tx := func(nonce uint64) types.DiffRecord {
		return types.DiffRecord{Diff: types.Diff{Nonce: nonce, Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: nonce, PromptHash: half}}}}}}
	}
	rpc := newQueryRPCClient(t, queryLookup{diffs: []types.DiffRecord{tx(1), tx(2)}}, transport.EndpointDiffs, 0)

	_, err := rpc.GetDiffs(context.Background(), 1, 2)
	require.ErrorIs(t, err, transport.ErrDiffPageOversized)
}
