//go:build testenvci

package citest

import (
	"testing"
	"time"

	"common/chain"
	"devshard/bridge"
	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

// TestG2_GatewayEscrowReadGRPC verifies escrow reads via bridge.GRPCBridge (no LCD).
func TestG2_GatewayEscrowReadGRPC(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootMockChainStack(t, "citest-g2-*")
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "mock-chain")
		}
	})
	harness.WaitGETOK(t, harness.HTTPClient(), eps.MockChainRPC+"/health", 5*time.Minute, "mock-chain RPC health")

	conn := harness.DialMockChainGRPC(t, cfg)
	br := bridge.NewGRPCBridge(chain.NewFromConn(conn))
	info, err := br.GetEscrow("1")
	require.NoError(t, err)
	require.Equal(t, "1", info.EscrowID)
	require.NotEmpty(t, info.Slots)
}
