package harness

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequireGatewayGRPCOnlyCompose asserts gateway has gRPC chain env only (no LCD REST wiring).
func RequireGatewayGRPCOnlyCompose(t *testing.T, composePath string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	text := string(body)

	gateway := extractComposeServiceBlock(text, "gateway:")
	require.NotEmpty(t, gateway, "gateway service block not found in %s", composePath)
	require.Contains(t, gateway, "DEVSHARD_CHAIN_GRPC:")
	require.NotContains(t, gateway, "DEVSHARD_CHAIN_REST")
	require.NotContains(t, gateway, "DEVSHARD_TX_QUERY_REST")
}

func extractComposeServiceBlock(compose, serviceName string) string {
	serviceName = strings.TrimSuffix(serviceName, ":")
	lines := strings.Split(compose, "\n")
	var buf []string
	inBlock := false
	for _, line := range lines {
		if strings.HasPrefix(line, "  "+serviceName+":") {
			inBlock = true
			buf = append(buf, line)
			continue
		}
		if !inBlock {
			continue
		}
		// Next service under `services:` (2-space indent, not 4-space child).
		if strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "    ") && strings.Contains(line, ":") {
			break
		}
		buf = append(buf, line)
	}
	return strings.Join(buf, "\n")
}
