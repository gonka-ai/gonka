package grpcface_test

import (
	"context"
	"testing"
	"time"

	"common/chain"
	chaintx "common/chain/tx"
	"devshard/signing"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestMockChainGRPC_CreateDevshardEscrowTx(t *testing.T) {
	st := seed.Defaults()
	rpcSvc, err := rpcface.NewService(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)

	srv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{
		Store:  st,
		RPC:    rpcSvc,
		Ledger: txledger.New(),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		srv.Stop()
		_ = lis.Close()
	})

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	txMgr, err := chaintx.New(conn, chaintx.Config{
		ChainID:      st.GetChainID(),
		FeeAmount:    1_000,
		GasLimit:     200_000,
		PollInterval: time.Millisecond,
		PollTimeout:  2 * time.Second,
	})
	require.NoError(t, err)

	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	result, err := txMgr.CreateDevshardEscrow(context.Background(), signer, 500_000, "test-model")
	require.NoError(t, err)
	require.Greater(t, result.EscrowID, uint64(1))

	chainClient := chain.NewFromConn(conn)
	resp, err := chainClient.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: result.EscrowID})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, "test-model", resp.Escrow.ModelId)
}
