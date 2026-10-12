package loadtest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadScenario_NormalLoad(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "normal-load.yaml"))
	require.NoError(t, err)
	require.Equal(t, "normal-load", scenario.Scenario)
	require.Len(t, scenario.Topology.MockML.Nodes, 2)
	require.Equal(t, uint64(2_000_000_000_000_000), scenario.Topology.Chain.EscrowAmount)
	require.Equal(t, uint32(100_000), scenario.Topology.Chain.MaxNonce)
	require.Equal(t, 3, scenario.Topology.Participants)
	require.Equal(t, 0.02, scenario.Assertions.Devshard.MaxGhostRate)
	require.Equal(t, "closed_loop", scenario.Workload.Traffic.ResolvedType())
	require.Equal(t, 0.0, scenario.Workload.Traffic.RPS)
	require.Equal(t, 2, scenario.Workload.MaxInFlight)
	require.Equal(t, "30s", scenario.Workload.Duration)
	require.Equal(t, "30s", scenario.DrainTimeout)
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

func TestLoadScenario_SpikeLoad(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "spike-load.yaml"))
	require.NoError(t, err)
	require.Equal(t, "spike-load", scenario.Scenario)
	require.Equal(t, "spikes", scenario.Workload.Traffic.ResolvedType())
	require.Equal(t, 4.0, scenario.Workload.Traffic.BaseRPS)
	require.Equal(t, 20.0, scenario.Workload.Traffic.SpikeRPS)
	require.Equal(t, "5s", scenario.Workload.Traffic.SpikeDuration)
	require.Equal(t, "20s", scenario.Workload.Traffic.Interval)
	require.Equal(t, 32, scenario.Workload.MaxInFlight)
	require.Equal(t, "60s", scenario.Workload.Duration)
}

func TestLoadScenario_SingleFailingMLNode(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "single-failing-ml-node.yaml"))
	require.NoError(t, err)
	require.Equal(t, "single-failing-ml-node", scenario.Scenario)
	require.Len(t, scenario.Topology.MockML.Nodes, 4)
	require.Equal(t, "failing", scenario.Topology.MockML.Nodes[3].Profile)
	require.Equal(t, 0.15, scenario.Assertions.Requests.ErrorRate)
	require.Equal(t, "60s", scenario.Workload.Duration)
	require.Equal(t, "90s", scenario.DrainTimeout)
}

func TestLoadScenario_SingleTimeoutMLNode(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "single-timeout-ml-node.yaml"))
	require.NoError(t, err)
	require.Equal(t, "single-timeout-ml-node", scenario.Scenario)
	require.Len(t, scenario.Topology.MockML.Nodes, 4)
	require.Equal(t, "timeout", scenario.Topology.MockML.Nodes[3].Profile)
	require.Equal(t, 0.35, scenario.Assertions.Requests.ErrorRate)
	require.Equal(t, "30s", scenario.Gateway.Redundancy.SecondaryWaitAfterWinner)
	require.Equal(t, 30*time.Second, scenario.SecondaryWaitAfterWinner())
	require.Equal(t, "60s", scenario.DrainTimeout)
}

func TestScenario_DefaultSecondaryWaitAfterWinner(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "normal-load.yaml"))
	require.NoError(t, err)
	require.Empty(t, scenario.Gateway.Redundancy.SecondaryWaitAfterWinner)
	require.Equal(t, DefaultSecondaryWaitAfterWinner, scenario.SecondaryWaitAfterWinner())
}

func TestLoadScenario_EscrowRotation(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "escrow-rotation.yaml"))
	require.NoError(t, err)
	require.True(t, scenario.Gateway.EscrowRotation.Enabled)
	require.True(t, scenario.Gateway.EscrowRotation.SettlementEnabled)
	require.Equal(t, int64(1), scenario.Gateway.EscrowRotation.PrePoCBlocks)
	require.Equal(t, uint64(200), scenario.Gateway.EscrowRotation.NonceDeactivationLimit)
	require.Equal(t, 1, scenario.Gateway.EscrowRotation.TempCount)
	require.Equal(t, 1, scenario.Gateway.EscrowRotation.TargetCount)
	require.Equal(t, uint64(5_000_000_000), scenario.Gateway.EscrowRotation.Amount)
	require.Equal(t, "DEVSHARD_PRIVATE_KEY", scenario.Gateway.EscrowRotation.PrivateKeyEnv)
	require.Equal(t, "10m", scenario.Workload.Duration)
	require.Equal(t, 1.0, scenario.Workload.Traffic.RPS)
	require.True(t, scenario.Assertions.EscrowRotation.RequireNewEscrow)
}

func TestLoadProfile_Fast(t *testing.T) {
	profile, err := LoadProfile("profiles", "fast")
	require.NoError(t, err)
	require.Equal(t, "fast", profile.Profile)
	require.Equal(t, "2ms", profile.TTFT)
	require.Equal(t, "0s", profile.TokenInterval)
	require.Equal(t, 100, profile.Workers)
}

func TestLoadProfile_Slow(t *testing.T) {
	profile, err := LoadProfile("profiles", "slow")
	require.NoError(t, err)
	require.Equal(t, "slow", profile.Profile)
	require.Equal(t, "1s", profile.TTFT)
	require.Equal(t, "50ms", profile.TokenInterval)
}

func TestLoadProfile_Realistic(t *testing.T) {
	profile, err := LoadProfile("profiles", "realistic")
	require.NoError(t, err)
	require.Equal(t, "realistic", profile.Profile)
	require.Equal(t, "250ms", profile.TTFT)
	require.Equal(t, "20ms", profile.TokenInterval)
}

func TestLoadProfile_Failing(t *testing.T) {
	profile, err := LoadProfile("profiles", "failing")
	require.NoError(t, err)
	require.Equal(t, "failing", profile.Profile)
	require.Equal(t, 0.7, profile.FailureRate)
	require.Equal(t, 503, profile.HTTPStatus)
}

func TestLoadProfile_Timeout(t *testing.T) {
	profile, err := LoadProfile("profiles", "timeout")
	require.NoError(t, err)
	require.Equal(t, "timeout", profile.Profile)
	require.True(t, profile.Hang)
}
