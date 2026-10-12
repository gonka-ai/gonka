package harness

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestComposeStopArgsOverridesLongGrace(t *testing.T) {
	args := composeStopArgs([]string{"-f", "docker-compose.yml"}, "versiond-1", composeFaultStopTimeout)
	require.Equal(t, []string{
		"compose", "-f", "docker-compose.yml", "stop", "--timeout", "10", "versiond-1",
	}, args)
	require.Less(t, composeFaultStopTimeout, 2*time.Minute,
		"fault-injection stop must finish before StopService's CommandContext")
}

func TestWithoutComposeService(t *testing.T) {
	names := []string{"mock-chain", "versiond-router", gatewayComposeService, "versiond-0"}
	require.Equal(t, []string{"mock-chain", "versiond-router", "versiond-0"},
		withoutComposeService(names, gatewayComposeService))
	require.Equal(t, names, withoutComposeService(names, "missing"))
	require.Empty(t, withoutComposeService(nil, gatewayComposeService))
}
