package harness

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// RequireGatewayGRPCOnlyCompose asserts devshardctl has gRPC chain env only (no LCD REST wiring).
func RequireGatewayGRPCOnlyCompose(t *testing.T, composePath string) {
	t.Helper()
	body, err := os.ReadFile(composePath)
	require.NoError(t, err)
	text := string(body)

	devshardctl := extractComposeServiceBlock(text, "devshardctl:")
	require.NotEmpty(t, devshardctl, "devshardctl service block not found in %s", composePath)
	require.Contains(t, devshardctl, "DEVSHARD_CHAIN_GRPC:")
	require.NotContains(t, devshardctl, "DEVSHARD_CHAIN_REST")
	require.NotContains(t, devshardctl, "DEVSHARD_TX_QUERY_REST")
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
