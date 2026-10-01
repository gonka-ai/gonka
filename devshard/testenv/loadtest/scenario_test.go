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
	require.Equal(t, "mock", scenario.Environment.Chain)
	require.Equal(t, "mock", scenario.Environment.DAPI)
	require.Len(t, scenario.Topology.MockML.Nodes, 2)
	require.Equal(t, uint64(1_000_000_000), scenario.Topology.Chain.EscrowAmount)
	require.Equal(t, uint32(100_000), scenario.Topology.Chain.MaxNonce)
	require.Equal(t, 3, scenario.Topology.Participants)
	require.Equal(t, 0.02, scenario.Assertions.Devshard.MaxGhostRate)
	require.Equal(t, "closed_loop", scenario.Workload.Traffic.ResolvedType())
	require.Equal(t, 2, scenario.Workload.MaxInFlight)
	require.Equal(t, "30s", scenario.Workload.Duration)
	require.Equal(t, "30s", scenario.DrainTimeout)
}

func TestLoadScenario_RealDAPIBootstrap(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "real-dapi-bootstrap.yaml"))
	require.NoError(t, err)
	require.Equal(t, "real-dapi-bootstrap", scenario.Scenario)
	require.True(t, scenario.Environment.IsRealDAPI())
	require.True(t, scenario.Environment.Bootstrap)
}

func TestLoadScenario_NormalLoadRealDAPI(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "normal-load-real-dapi.yaml"))
	require.NoError(t, err)
	require.Equal(t, "normal-load-real-dapi", scenario.Scenario)
	require.True(t, scenario.Environment.IsRealDAPI())
	require.False(t, scenario.Environment.Bootstrap)
	require.Equal(t, "dapi", scenario.Topology.MockML.Allocator)
	require.Equal(t, "30s", scenario.Workload.Duration)
}

func TestLoadScenario_SingleSlowMLNode(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "single-slow-ml-node.yaml"))
	require.NoError(t, err)
	require.Equal(t, "single-slow-ml-node", scenario.Scenario)
	require.Equal(t, "constant", scenario.Workload.Traffic.ResolvedType())
	require.Equal(t, 4.0, scenario.Workload.Traffic.RPS)
	require.Equal(t, 10, scenario.Workload.MaxInFlight)
	require.Len(t, scenario.Topology.MockML.Nodes, 4)
	require.Equal(t, "slow", scenario.Topology.MockML.Nodes[3].Profile)
	require.Equal(t, "60s", scenario.DrainTimeout)
}

func TestLoadProfile_Fast(t *testing.T) {
	profile, err := LoadProfile("profiles", "fast")
	require.NoError(t, err)
	require.Equal(t, "fast", profile.Profile)
	require.Equal(t, 100, profile.Workers)
}

func TestLoadProfile_Slow(t *testing.T) {
	profile, err := LoadProfile("profiles", "slow")
	require.NoError(t, err)
	require.Equal(t, "slow", profile.Profile)
	require.Equal(t, "1s", profile.TTFT)
}
