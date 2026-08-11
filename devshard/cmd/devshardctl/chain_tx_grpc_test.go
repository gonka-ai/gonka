package main

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
	"devshard/testenv/mockchain/store"
	"devshard/testenv/mockchain/txledger"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestGRPCChainTxClient_CreateDevshardEscrow_MockChain(t *testing.T) {
	st := seed.Defaults()
	rpcSvc, err := rpcface.NewService(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	ledger := txledger.New()

	grpcSrv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{
		Store:  st,
		RPC:    rpcSvc,
		Ledger: ledger,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		grpcSrv.Stop()
		_ = lis.Close()
	})
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	chainClient := chain.NewFromConn(conn)
	txMgr, err := chaintx.New(conn, chaintx.Config{
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  2 * time.Second,
	})
	require.NoError(t, err)

	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	result, err := txMgr.CreateDevshardEscrow(t.Context(), signer, 1_000_000, "test-model")
	require.NoError(t, err)
	require.Greater(t, result.EscrowID, uint64(1))
	require.Equal(t, signer.Address(), result.Creator)

	resp, err := chainClient.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: result.EscrowID})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, result.EscrowID, resp.Escrow.Id)
}

func startGRPCWithTx(t *testing.T, st *store.Store, rpcSvc *rpcface.Service) (grpc.ClientConnInterface, func()) {
	t.Helper()
	ledger := txledger.New()
	grpcSrv, lis, err := grpcface.NewInProcessServer(grpcface.Deps{
		Store:  st,
		RPC:    rpcSvc,
		Ledger: ledger,
	})
	require.NoError(t, err)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	cleanup := func() {
		_ = conn.Close()
		grpcSrv.Stop()
		_ = lis.Close()
	}
	return conn, cleanup
}

func TestGRPCChainTxClient_SettleDevshardEscrow_MockChain(t *testing.T) {
	st := seed.Defaults()
	rpcSvc, err := rpcface.NewService(st, rpcface.Config{BlockInterval: time.Hour})
	require.NoError(t, err)
	conn, cleanup := startGRPCWithTx(t, st, rpcSvc)
	t.Cleanup(cleanup)

	txMgr, err := chaintx.New(conn, chaintx.Config{
		ChainID:      "gonka-test",
		FeeAmount:    123,
		GasLimit:     456,
		PollInterval: time.Millisecond,
		PollTimeout:  2 * time.Second,
	})
	require.NoError(t, err)

	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	created, err := txMgr.CreateDevshardEscrow(t.Context(), signer, 1_000_000, "test-model")
	require.NoError(t, err)

	result, err := txMgr.SettleDevshardEscrow(t.Context(), signer, chaintx.SettleParams{
		EscrowID:                    created.EscrowID,
		StateRoot:                   []byte("state-root"),
		Nonce:                       1,
		RestHash:                    []byte("rest-hash"),
		Fees:                        10,
		StateRootAndProtocolVersion: []byte("v2"),
		HostStats:                   []chaintx.HostStats{{SlotID: 0}},
		Signatures:                  []chaintx.SlotSignature{{SlotID: 0, Signature: []byte("sig")}},
	})
	require.NoError(t, err)
	require.Equal(t, created.EscrowID, result.EscrowID)
	require.NotEmpty(t, result.TxHash)
	require.Equal(t, signer.Address(), result.Settler)

	escrow := st.GetEscrow(created.EscrowID)
	require.NotNil(t, escrow)
	require.True(t, escrow.Settled)
}
