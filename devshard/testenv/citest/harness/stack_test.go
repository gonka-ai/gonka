package harness

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWithoutComposeService(t *testing.T) {
	names := []string{"mock-chain", "versiond-router", gatewayComposeService, "versiond-0"}
	require.Equal(t, []string{"mock-chain", "versiond-router", "versiond-0"},
		withoutComposeService(names, gatewayComposeService))
	require.Equal(t, names, withoutComposeService(names, "missing"))
	require.Empty(t, withoutComposeService(nil, gatewayComposeService))
}
