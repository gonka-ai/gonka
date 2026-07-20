//go:build testenvci

package citest

import (
	"context"
	"testing"
	"time"

	"common/chain"
	chaintx "common/chain/tx"
	"devshard/signing"
	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestG1_GatewayEscrowCreateGRPC creates a devshard escrow via common/chain/tx gRPC
// against the dockerized mock-chain (Track A + B). Gateway wiring migration is Track C.
func TestG1_GatewayEscrowCreateGRPC(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootMockChainStack(t, "citest-g1-*")
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "mock-chain")
		}
	})
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockChainRPC+"/health", 5*time.Minute, "mock-chain RPC health")

	conn := harness.DialMockChainGRPC(t, cfg)
	txMgr, err := chaintx.New(conn, chaintx.Config{
		ChainID:      cfg.ChainID,
		PollInterval: 500 * time.Millisecond,
		PollTimeout:  30 * time.Second,
	})
	require.NoError(t, err)

	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	model := config.PrimaryModelID(cfg)
	result, err := txMgr.CreateDevshardEscrow(context.Background(), signer, 1_000_000, model)
	require.NoError(t, err)
	require.Greater(t, result.EscrowID, uint64(1))
	require.Equal(t, signer.Address(), result.Creator)

	chainClient := chain.NewFromConn(conn)
	resp, err := chainClient.InferenceQueryClient().DevshardEscrow(context.Background(),
		&inferencetypes.QueryGetDevshardEscrowRequest{Id: result.EscrowID})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, model, resp.Escrow.ModelId)
}
