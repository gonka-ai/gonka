package mockdapi

import (
	"context"
	"testing"
	"time"

	"common/chain"
	"common/chainoracle/blocks"

	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Test flow:
//  1. Serve the mock chain with its RPC service and build the node-manager block source over its gRPC.
//  2. Read a past height and the latest block through the source.
//  3. Assert the hash and time equal what the chain's CometBFT RPC serves at the same height.
//  4. Ask for a height above the tip and assert the source reports it as not found.
func TestChainBlocksServeTheHashesTheChainCarries(t *testing.T) {
	st := seed.Defaults()
	rpcSvc, err := rpcface.NewService(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{Store: st, RPC: rpcSvc, Ledger: txledger.New()})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	source := newChainBlocks(chain.NewFromConn(conn))
	ctx := context.Background()
	past := int64(120)
	wantPast, wantPastID, err := rpcSvc.BlockAt(&past)
	require.NoError(t, err)
	wantLatest, wantLatestID, err := rpcSvc.BlockAt(nil)
	require.NoError(t, err)

	gotPast, err := source.At(ctx, past)
	require.NoError(t, err)
	gotLatest, err := source.Latest(ctx)
	require.NoError(t, err)

	require.Equal(t, wantPastID.Hash.Bytes(), gotPast.BlockHash)
	require.True(t, wantPast.Time.Equal(gotPast.Time))
	require.Equal(t, wantLatestID.Hash.Bytes(), gotLatest.BlockHash)
	require.True(t, wantLatest.Time.Equal(gotLatest.Time))
	_, err = source.At(ctx, 10_000)
	require.ErrorIs(t, err, blocks.ErrHeaderNotFound)
}
