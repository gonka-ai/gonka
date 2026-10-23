package loadtest

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestScenario_LogTail(t *testing.T) {
	for _, value := range []string{"", "5000", "100", "all", "0", "-1", "full", "1.5"} {
		t.Run(value, func(t *testing.T) {
			scenario := testScenario()
			// Numeric YAML scalars should work without quotes as well.
			if value != "" {
				require.NoError(t, yaml.Unmarshal([]byte("logs:\n  tail: "+value+"\n"), &scenario.Diagnostics))
			}
			err := scenario.Validate()
			switch value {
			case "", "5000", "100", "all":
				require.NoError(t, err)
				if value == "" {
					require.Equal(t, "5000", scenario.Diagnostics.LogTail())
				} else {
					require.Equal(t, value, scenario.Diagnostics.LogTail())
				}
			default:
				require.ErrorContains(t, err, "diagnostics.logs.tail")
			}
		})
	}
	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Equal(t, "all", scenario.Diagnostics.LogTail())
}

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

func TestLoadScenario_MissrateCatchUp(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "missrate-catch-up.yaml"))
	require.NoError(t, err)
	require.Equal(t, "missrate-catch-up", scenario.Scenario)
	require.Equal(t, "10s", scenario.Protocol.RefusalTimeout)
	require.Equal(t, "30s", scenario.Protocol.ExecutionTimeout)
	require.Len(t, scenario.Workload.HostFaults, 1)
	require.Equal(t, "versiond-2", scenario.Workload.HostFaults[0].Host)
	require.Equal(t, uint64(1), scenario.Assertions.Accounting.MinProtocolMisses)
	require.Equal(t, uint64(1), scenario.Assertions.Accounting.MinTimeoutsApplied)
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

func TestScenario_TransportFailureQuarantine(t *testing.T) {
	for _, value := range []string{"", "30m", "30s", "1ms", "invalid", "0s", "-1s", "500us"} {
		t.Run(value, func(t *testing.T) {
			scenario := testScenario()
			scenario.Gateway.ParticipantThrottle.TransportFailureQuarantine = value
			err := scenario.Validate()
			switch value {
			case "", "30m", "30s", "1ms":
				require.NoError(t, err)
			default:
				require.ErrorContains(t, err, "gateway.participant_throttle.transport_failure_quarantine")
			}
		})
	}
	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Equal(t, "70s", scenario.Gateway.ParticipantThrottle.TransportFailureQuarantine)
	require.Equal(t, "70s", scenario.Gateway.ParticipantThrottle.HTTPQuarantine)
}

func TestScenario_HTTPQuarantine(t *testing.T) {
	for _, value := range []string{"", "60m", "70s", "1ms", "invalid", "0s", "-1s", "500us"} {
		t.Run(value, func(t *testing.T) {
			scenario := testScenario()
			scenario.Gateway.ParticipantThrottle.HTTPQuarantine = value
			err := scenario.Validate()
			switch value {
			case "", "60m", "70s", "1ms":
				require.NoError(t, err)
			default:
				require.ErrorContains(t, err, "gateway.participant_throttle.http_quarantine")
			}
		})
	}
}

func TestLoadScenario_EscrowRotation(t *testing.T) {
	scenario, err := LoadScenario(filepath.Join("scenarios", "escrow-rotation.yaml"))
	require.NoError(t, err)
	require.True(t, scenario.Gateway.EscrowRotation.Enabled)
	require.True(t, scenario.Gateway.EscrowRotation.SettlementEnabled)
	require.Equal(t, int64(1), scenario.Gateway.EscrowRotation.PrePoCBlocks)
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

// Test flow:
// 1. Bind both nodes of a three-participant scenario to versiond hosts and cap one of them.
// 2. Check it validates.
// 3. Break the binding or the cap one at a time and expect the named validation error.
func TestScenario_MockMLNodeHostAndMaxConcurrent(t *testing.T) {
	valid := func() Scenario {
		s := testScenario()
		s.Topology.Participants = 3
		s.Topology.MockML.Nodes[0].Host = "versiond-0"
		s.Topology.MockML.Nodes[1].Host = "versiond-3"
		s.Topology.MockML.Nodes[1].MaxConcurrent = 2
		return s
	}
	base := valid()
	require.NoError(t, base.Validate())
	for _, tc := range []struct {
		name   string
		change func(*Scenario)
		want   string
	}{
		{"host outside topology", func(s *Scenario) { s.Topology.MockML.Nodes[1].Host = "versiond-4" }, "mock_ml.nodes[1].host"},
		{"not a versiond host", func(s *Scenario) { s.Topology.MockML.Nodes[0].Host = "mock-chain" }, "mock_ml.nodes[0].host"},
		{"host without participants", func(s *Scenario) { s.Topology.Participants = 0 }, "mock_ml.nodes[0].host"},
		{"negative cap", func(s *Scenario) { s.Topology.MockML.Nodes[1].MaxConcurrent = -1 }, "max_concurrent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(&s)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
}

// Test flow:
// 1. Start from a valid scenario with streaming on, per-host env, ML node bindings, cases, the new quarantines, per-model settlement and a request mix.
// 2. Check it validates, so the old stream ban is gone.
// 3. Break one new field at a time and expect the named validation error.
func TestScenario_ChunkAFieldsValidation(t *testing.T) {
	valid := func() Scenario {
		s := mixScenario()
		s.Topology.Participants = 3
		s.Workload.Request.Stream = true
		s.Workload.Request.Timeout = "90s"
		s.Topology.Hosts = []TopologyHost{{ID: "versiond-3", Env: map[string]string{"DEVSHARD_RPC_MSGS_PER_MIN": "3000"}}}
		s.Topology.MockML.Nodes[0].Host = "versiond-0"
		s.Topology.MockML.Nodes[1].Host = "versiond-3"
		s.Cases = []Case{{ID: "host-over-peer-chat-budget", KnownBug: "#1988"}}
		s.Gateway.ParticipantThrottle = GatewayParticipantThrottle{EmptyStreamQuarantine: "45s", StalledWinnerQuarantine: "45s"}
		enabled := false
		s.Gateway.EscrowRotation = GatewayEscrowRotation{Enabled: true, PrePoCBlocks: 10, TempCount: 1, TargetCount: 1, Amount: 1, PrivateKeyEnv: "KEY",
			Models: []GatewayRotationModel{{Model: "model-b", SettlementEnabled: &enabled}, {Model: "model-a"}}}
		return s
	}
	base := valid()
	require.NoError(t, base.Validate())
	require.Equal(t, []string{"model-a", "model-b"}, base.RequestModels())
	require.Equal(t, 90*time.Second, base.RequestTimeout())
	require.Equal(t, 30*time.Second, testScenario().RequestTimeout())
	require.Equal(t, []string{"test-model"}, testScenario().RequestModels())
	for _, tc := range []struct {
		name   string
		change func(*Scenario)
		want   string
	}{
		{"host outside topology", func(s *Scenario) { s.Topology.Hosts[0].ID = "versiond-4" }, "topology.hosts[0]: id"},
		{"host without participants", func(s *Scenario) { s.Topology.Participants = 0 }, "topology.hosts[0]: id"},
		{"duplicate host", func(s *Scenario) { s.Topology.Hosts = append(s.Topology.Hosts, s.Topology.Hosts[0]) }, "duplicate id"},
		{"env name", func(s *Scenario) { s.Topology.Hosts[0].Env["BAD-NAME"] = "1" }, "invalid variable name"},
		{"node host", func(s *Scenario) { s.Topology.MockML.Nodes[0].Host = "mock-chain" }, "mock_ml.nodes[0].host"},
		{"empty case id", func(s *Scenario) { s.Cases = append(s.Cases, Case{}) }, "cases[1]"},
		{"duplicate case", func(s *Scenario) { s.Cases = append(s.Cases, Case{ID: "host-over-peer-chat-budget"}) }, "cases[1]"},
		{"empty stream quarantine", func(s *Scenario) { s.Gateway.ParticipantThrottle.EmptyStreamQuarantine = "0s" }, "empty_stream_quarantine"},
		{"stalled winner quarantine", func(s *Scenario) { s.Gateway.ParticipantThrottle.StalledWinnerQuarantine = "soon" }, "stalled_winner_quarantine"},
		{"rotation model empty", func(s *Scenario) { s.Gateway.EscrowRotation.Models[0].Model = "" }, "escrow_rotation.models[0]"},
		{"rotation model duplicate", func(s *Scenario) { s.Gateway.EscrowRotation.Models[1].Model = "model-b" }, "escrow_rotation.models[1]"},
		{"rotation models without rotation", func(s *Scenario) { s.Gateway.EscrowRotation.Enabled = false }, "requires gateway.escrow_rotation.enabled"},
		{"timeout", func(s *Scenario) { s.Workload.Request.Timeout = "0s" }, "request.timeout"},
		{"stream ratio", func(s *Scenario) { s.Workload.Request.Mix.StreamRatio = 1.5 }, "stream_ratio"},
		{"logprobs ratio", func(s *Scenario) { s.Workload.Request.Mix.LogprobsRatio = -0.1 }, "logprobs_ratio"},
		{"top logprobs", func(s *Scenario) { s.Workload.Request.Mix.TopLogprobs = 21 }, "top_logprobs"},
		{"max tokens value", func(s *Scenario) { s.Workload.Request.Mix.MaxTokens[0].Value = 0 }, "mix.max_tokens[0]"},
		{"max tokens weight", func(s *Scenario) { s.Workload.Request.Mix.MaxTokens[1].Weight = 0 }, "mix.max_tokens[1]"},
		{"temperature", func(s *Scenario) { s.Workload.Request.Mix.Temperature[0].Value = 3 }, "mix.temperature[0]"},
		{"prompt empty", func(s *Scenario) { s.Workload.Request.Mix.Prompts[0].Value = nil }, "mix.prompts[0]"},
		{"prompt role", func(s *Scenario) { s.Workload.Request.Mix.Prompts[1].Value[0].Role = "" }, "mix.prompts[1]"},
		{"model duplicate", func(s *Scenario) { s.Workload.Request.Mix.Models[1].Value = "model-a" }, "mix.models[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := valid()
			tc.change(&s)
			require.ErrorContains(t, s.Validate(), tc.want)
		})
	}
}

// Test flow:
// 1. Parse a YAML scenario fragment that uses every new chunk A field.
// 2. Check the fields decode into the scenario types, including nested event params and weighted prompts.
func TestScenario_ChunkAFieldsYAML(t *testing.T) {
	var scenario Scenario
	require.NoError(t, yaml.Unmarshal([]byte(`
cases:
  - {id: host-under-poc-challenge, title: PoC challenge, known_bug: "#1823"}
topology:
  hosts:
    - id: versiond-5
      env: {DEVSHARD_RPC_MSGS_PER_MIN: "3000"}
  mock_ml:
    nodes:
      - {name: mock-openai-5, profile: fast, host: versiond-5}
workload:
  request:
    timeout: 2m
    mix:
      stream_ratio: 0.5
      max_tokens: [{value: 32, weight: 40}, {value: 2048, weight: 5}]
      temperature: [{value: 0, weight: 1}]
      prompts:
        - value: [{role: user, content: hi}]
          weight: 1
      models: [{value: model-a, weight: 7}]
  events:
    - {id: c1, case: receipt-then-empty-stream, action: ml_fault, target: mock-openai-5, at: 2m, until: 3m, params: {patch: {empty_stream: true}}}
gateway:
  participant_throttle: {empty_stream_quarantine: 45s, stalled_winner_quarantine: 45s}
  escrow_rotation:
    models: [{model: model-b, settlement_enabled: false}]
`), &scenario))
	require.Equal(t, "#1823", scenario.Cases[0].KnownBug)
	require.Equal(t, "3000", scenario.Topology.Hosts[0].Env["DEVSHARD_RPC_MSGS_PER_MIN"])
	require.Equal(t, "versiond-5", scenario.Topology.MockML.Nodes[0].Host)
	require.Equal(t, 2048, scenario.Workload.Request.Mix.MaxTokens[1].Value)
	require.Equal(t, "hi", scenario.Workload.Request.Mix.Prompts[0].Value[0].Content)
	require.Equal(t, map[string]any{"empty_stream": true}, scenario.Workload.Events[0].Params["patch"])
	require.Equal(t, "45s", scenario.Gateway.ParticipantThrottle.StalledWinnerQuarantine)
	require.False(t, *scenario.Gateway.EscrowRotation.Models[0].SettlementEnabled)
}
