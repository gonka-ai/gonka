package grpcface_test

import (
	"context"
	"testing"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"
)

// Test flow:
//  1. Serve the mock chain with its RPC service and open the CometBFT block service over gRPC.
//  2. Read the block at a past height and the latest block.
//  3. Assert both carry the hash and time the CometBFT RPC serves for the same height.
//  4. Ask for a height above the tip and assert the refusal is an invalid-argument error.
func TestMockChainGRPC_BlocksCarryTheHashAndTimeTheRPCServes(t *testing.T) {
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
	client := cmtservice.NewServiceClient(conn)
	ctx := context.Background()
	past := int64(120)
	wantPast, wantPastID, err := rpcSvc.BlockAt(&past)
	require.NoError(t, err)
	wantLatest, wantLatestID, err := rpcSvc.BlockAt(nil)
	require.NoError(t, err)

	byHeight, err := client.GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{Height: past})
	require.NoError(t, err)
	latest, err := client.GetLatestBlock(ctx, &cmtservice.GetLatestBlockRequest{})
	require.NoError(t, err)

	require.Equal(t, wantPastID.Hash.Bytes(), byHeight.BlockId.Hash)
	require.Equal(t, past, byHeight.SdkBlock.Header.Height)
	require.True(t, wantPast.Time.Equal(byHeight.SdkBlock.Header.Time))
	require.Equal(t, wantLatestID.Hash.Bytes(), latest.BlockId.Hash)
	require.True(t, wantLatest.Time.Equal(latest.SdkBlock.Header.Time))

	_, err = client.GetBlockByHeight(ctx, &cmtservice.GetBlockByHeightRequest{Height: 10_000})
	require.Equal(t, codes.InvalidArgument, status.Code(err))
}
