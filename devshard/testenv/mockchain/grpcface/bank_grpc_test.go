package grpcface_test

import (
	"context"
	"testing"
	"time"

	"devshard/signing"
	"devshard/testenv/mockchain/grpcface"
	"devshard/testenv/mockchain/rpcface"
	"devshard/testenv/mockchain/seed"
	"devshard/testenv/mockchain/txledger"

	banktypes "github.com/cosmos/cosmos-sdk/x/bank/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// Test flow:
//  1. Serve the mock chain with transactions enabled.
//  2. Ask the bank service for a fresh wallet's spendable ngonka.
//  3. Require a funded balance, so a gateway's pre-create check passes as the mock's transactions do.
func TestMockChainGRPC_SpendableBalanceReportsAFundedWallet(t *testing.T) {
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
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	response, err := banktypes.NewQueryClient(conn).SpendableBalanceByDenom(context.Background(),
		&banktypes.QuerySpendableBalanceByDenomRequest{Address: signer.Address(), Denom: "ngonka"})

	require.NoError(t, err)
	require.Equal(t, "ngonka", response.Balance.Denom)
	require.Equal(t, grpcface.MockWalletBalance, response.Balance.Amount.Uint64())
}
