package citest

import (
	"os"
	"testing"

	"devshard/testenv/citest/harness"

	"github.com/stretchr/testify/require"
)

func TestGeneratedComposeGatewayWiring(t *testing.T) {
	harness.RequireDocker(t)

	stack := harness.NewStack(t, "citest-gateway-wiring-*")
	harness.WriteSingleVersiondConfig(t, stack.WorkDir)
	stack.RunGencompose(t)

	body, err := os.ReadFile(stack.ComposePath)
	require.NoError(t, err)
	text := string(body)

	require.Contains(t, text, `DEVSHARDS_JSON: "[{\"id\":\"1\",\"model\":\"test-model\",\"private_key_env\":\"GATEWAY_ESCROW_KEY\"}]"`)
	require.Contains(t, text, "DEVSHARD_CHAIN_GRPC: mock-chain:9090")
	require.Contains(t, text, "DEVSHARD_CHAIN_RPC: http://mock-chain:26657")
	require.Contains(t, text, "NODE_RPC_URL: http://mock-chain:26657")
	require.Contains(t, text, "DEVSHARD_PUBLIC_API: http://mock-dapi:9100")
	require.Contains(t, text, "DEVSHARD_NODE_MANAGER_ADDR: mock-dapi:9400")
	require.Contains(t, text, "GATEWAY_MAX_TOKENS_CAP")
	require.Contains(t, text, "DEVSHARD_GATEWAY_HOST_PING_DISABLED")
	require.Contains(t, text, "DEVSHARD_GATEWAY_HOST_PING_INTERVAL")
	require.Contains(t, text, "DEVSHARD_GATEWAY_HOST_PING_TIMEOUT")
	require.Contains(t, text, "DEVSHARD_GATEWAY_HOST_PING_CONCURRENCY")
	require.NotContains(t, text, "DEVSHARD_CHAINORACLE_URL:")
	require.NotContains(t, text, "DEVSHARD_GATEWAY_CHAIN_ORACLE:")
	require.NotContains(t, text, "DEVSHARD_CHAIN_REST:")
	require.NotContains(t, text, "DEVSHARD_TX_QUERY_REST:")
	require.Contains(t, text, "args:")
	require.Contains(t, text, "DEVSHARD_VERSION: \"v2\"")
	require.Contains(t, text, "/healthz")
	require.Contains(t, text, "mock-dapi")
}
