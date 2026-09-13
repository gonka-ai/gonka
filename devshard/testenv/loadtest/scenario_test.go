package loadtest

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadScenario_NormalLoad(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "normal-load.yaml"))
	require.NoError(t, err)
	require.Equal(t, "normal-load", scenario.Scenario)
	require.Len(t, scenario.Topology.MockML.Nodes, 2)
	require.Equal(t, uint64(1_000_000_000), scenario.Topology.Chain.EscrowAmount)
	require.Equal(t, uint32(100_000), scenario.Topology.Chain.MaxNonce)
	require.Equal(t, "30s", scenario.Workload.Duration)
	require.Equal(t, "3m", scenario.DrainTimeout)
}

func TestLoadScenario_ExtendedDrain(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "extended-drain.yaml"))
	require.NoError(t, err)
	require.Equal(t, "extended-drain", scenario.Scenario)
	require.Equal(t, "30s", scenario.Workload.Duration)
	require.Equal(t, "11m", scenario.DrainTimeout)
}

func TestLoadProfile_Fast(t *testing.T) {
	profile, err := LoadProfile("profiles", "fast")
	require.NoError(t, err)
	require.Equal(t, "fast", profile.Profile)
	require.Equal(t, 100, profile.Workers)
}
