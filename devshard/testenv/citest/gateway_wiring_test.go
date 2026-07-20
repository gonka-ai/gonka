package citest

import (
	"os"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

func TestGeneratedCompose_GatewayPhase7Wiring(t *testing.T) {
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-gateway-wiring-*")
	harness.WriteSingleVersiondConfig(t, stack.WorkDir)
	stack.RunGencompose(t)

	body, err := os.ReadFile(stack.ComposePath)
	require.NoError(t, err)
	text := string(body)

	require.Contains(t, text, "DEVSHARD_ESCROW_ID: \"1\"")
	require.Contains(t, text, "DEVSHARD_MODEL: \"test-model\"")
	require.Contains(t, text, "DEVSHARD_CHAIN_GRPC: mock-chain:9090")
	require.Contains(t, text, "DEVSHARD_PUBLIC_API: http://mock-dapi:9100")
	require.NotContains(t, text, "DEVSHARD_CHAIN_REST:")
	require.NotContains(t, text, "DEVSHARD_TX_QUERY_REST:")
	require.Contains(t, text, "args:")
	require.Contains(t, text, "DEVSHARD_VERSION: \"v2\"")
	require.Contains(t, text, "/v1/status")
	require.Contains(t, text, "mock-dapi")
}
