package harness

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequireGatewayGRPCOnlyCompose(t *testing.T) {
	dir := t.TempDir()
	okPath := dir + "/ok.yml"
	require.NoError(t, os.WriteFile(okPath, []byte(`
services:
  devshardctl:
    environment:
      DEVSHARD_CHAIN_GRPC: mock-chain:9090
  mock-chain:
    ports:
      - "9090:9090"
`), 0o644))
	RequireGatewayGRPCOnlyCompose(t, okPath)
}
