package loadtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteRealDAPIStack_RendersLocalTestNetWithoutMocks(t *testing.T) {
	testenvDir := filepath.Join(t.TempDir(), "devshard", "testenv")
	workDir := filepath.Join(testenvDir, "run")
	require.NoError(t, os.MkdirAll(workDir, 0o755))

	composePath, ports, err := writeRealDAPIStack(testenvDir, workDir)
	require.NoError(t, err)
	require.Positive(t, ports.http)
	require.Positive(t, ports.mlAPI)
	require.Positive(t, ports.nodeManager)

	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	compose := string(body)
	require.Contains(t, compose, "dockerfile: inference-chain/Dockerfile")
	require.Contains(t, compose, "dockerfile: decentralized-api/Dockerfile")
	require.Contains(t, compose, "GOOS: linux")
	require.Contains(t, compose, "GOARCH:")
	require.Contains(t, compose, "NODE_MANAGER_GRPC_PORT")
	require.False(t, realDAPIComposeHasMocks(compose))

	nodeConfig, err := os.ReadFile(filepath.Join(workDir, "node-config.json"))
	require.NoError(t, err)
	require.Contains(t, string(nodeConfig), "mock-openai-0")
	require.Contains(t, string(nodeConfig), "mock-openai-1")
	require.Contains(t, string(nodeConfig), realDAPIGovernanceModel)
}
