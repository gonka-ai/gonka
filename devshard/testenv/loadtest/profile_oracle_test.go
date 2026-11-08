package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

const oracleProfile = `schema_version: v1
profile: oracle-x
ttft: 2ms
token_interval: 0s
workers: 4
queue: 0
oracle: true
chunk_tokens: 4
first_content_ms: 50
exec_ms: 400
replay_ms: 300
rules:
  - name: replay-busy
    phase: replay
    prompt_contains: "load-request"
    times: 3
    http_status: 503
`

// Test flow:
//  1. A profile turns on the oracle, groups four tokens per chunk, sets the execution schedule and the
//     replay delay, and names a rule for replays.
//  2. Write the runner config for a scenario whose first node runs that profile.
//  3. Check the node carries every knob and the rules as the mock reads them, and the other nodes none.
func TestOracleProfileKnobsReachTheirMockNode(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "oracle-x.yaml"), []byte(oracleProfile), 0o644))
	oracle, err := LoadProfile(dir, "oracle-x")
	require.NoError(t, err)

	scenario, err := LoadScenario(filepath.Join("scenarios", "catch-up.yaml"))
	require.NoError(t, err)
	require.Greater(t, len(scenario.Topology.MockML.Nodes), 1)
	profiles := map[string]Profile{"oracle-x": oracle}
	for _, node := range scenario.Topology.MockML.Nodes {
		profile, err := LoadProfile("profiles", node.Profile)
		require.NoError(t, err)
		profiles[node.Profile] = profile
	}
	scenario.Topology.MockML.Nodes[0].Profile = "oracle-x"

	workDir := t.TempDir()
	require.NoError(t, writeRunnerConfig(testenvWithBaseConfig(t), workDir, scenario, profiles, false, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)

	node := cfg.MockOpenAI.Nodes[0]
	require.True(t, node.Oracle)
	require.Equal(t, 4, node.ChunkTokens)
	require.Equal(t, 50, node.FirstContentMs)
	require.Equal(t, 400, node.ExecMs)
	require.Equal(t, 300, node.ReplayMs)
	var rules []mockopenai.FaultRule
	require.NoError(t, json.Unmarshal([]byte(node.RulesJSON), &rules))
	require.Equal(t, []mockopenai.FaultRule{{Name: "replay-busy", Phase: mockopenai.PhaseReplay,
		PromptContains: "load-request", Times: 3, HTTPStatus: 503}}, rules)
	for _, other := range cfg.MockOpenAI.Nodes[1:] {
		require.False(t, other.Oracle, other.Name)
		require.Zero(t, other.ChunkTokens, other.Name)
		require.Zero(t, other.FirstContentMs, other.Name)
		require.Zero(t, other.ExecMs, other.Name)
		require.Zero(t, other.ReplayMs, other.Name)
		require.Empty(t, other.RulesJSON, other.Name)
	}
}

// Test flow:
// 1. Write profiles with a rule that selects nothing, a rule without an action and negative knobs.
// 2. Load each.
// 3. Check each is refused when the profile is loaded, not when the mock container starts.
func TestAProfileWithABadRuleOrANegativeKnobIsRefused(t *testing.T) {
	head := "schema_version: v1\nprofile: bad\nttft: 2ms\ntoken_interval: 0s\nworkers: 1\nqueue: 0\n"
	for name, body := range map[string]string{
		"rule without a selector":        "rules:\n  - name: x\n    http_status: 503\n",
		"rule without an action":         "rules:\n  - name: x\n    prompt_contains: load-request\n",
		"negative chunk_tokens":          "chunk_tokens: -1\n",
		"negative first_content_ms":      "first_content_ms: -1\n",
		"negative exec_ms":               "exec_ms: -1\n",
		"negative replay_ms":             "replay_ms: -1\n",
		"exec_ms below first_content_ms": "first_content_ms: 500\nexec_ms: 400\n",
		"two rules with one name":        "rules:\n  - {name: x, prompt_contains: a, http_status: 503}\n  - {name: x, prompt_contains: b, http_status: 500}\n",
	} {
		dir := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(head+body), 0o644))
		_, err := LoadProfile(dir, "bad")
		require.Error(t, err, name)
	}
}

// Test flow:
//  1. Load the five oracle profiles the stream-shape scenarios run.
//  2. Check each turns the oracle on with the schedule its shape needs, and differs from oracle-chunk1
//     only in the knob that tells it apart: the grouping, the capacity, the replay delay or the
//     execution time. The fault rules live in the scenario, for every node alike.
func TestOracleProfilesDifferOnlyInTheShapeTheyStand(t *testing.T) {
	load := func(name string) Profile {
		p, err := LoadProfile("profiles", name)
		require.NoError(t, err, name)
		require.True(t, p.Oracle, name)
		require.Empty(t, p.Rules, name)
		return p
	}
	one := load("oracle-chunk1")
	require.Equal(t, 1, one.ChunkTokens)
	require.Equal(t, 50, one.FirstContentMs)
	require.Equal(t, 400, one.ExecMs)
	require.Equal(t, 1000, one.ReplayMs)
	require.Zero(t, one.Queue, "no hidden queue: over capacity the node answers 503 at once")
	asOne := func(p Profile) Profile {
		p.Profile = one.Profile
		return p
	}

	four := load("oracle-chunk4")
	require.Equal(t, 4, four.ChunkTokens)
	four.ChunkTokens = 1
	require.Equal(t, one, asOne(four), "the two groupings run one schedule")

	shared := load("oracle-shared")
	s, err := LoadScenario(filepath.Join("scenarios", "shared-endpoint.yaml"))
	require.NoError(t, err)
	// A request holds a node for its executions and replays: drain, on the same oracle nodes with
	// room to spare, took 1.19 executions and 0.67 replays a request, about 1.15 node-seconds, and the
	// node of a solo host (versiond-2, versiond-3) carried about a third of them. The four nodes
	// together keep up with every escrow; the node of a solo host does not.
	perRequest := (1.19*float64(shared.ExecMs) + 0.67*float64(shared.ReplayMs)) / 1000
	demand := s.Workload.Traffic.RPS * perRequest
	require.Greater(t, float64(shared.Workers*len(s.Topology.MockML.Nodes)), demand, "the shared endpoint keeps up with every escrow together")
	require.Less(t, float64(shared.Workers), demand/3, "a solo host's node is smaller than its share of the load")
	// A full node queues a request as vLLM does: the queue holds what a solo host's node falls behind
	// over the whole load, so the node never answers 503, and the dapi caps no node's leases.
	behind := (demand/3 - float64(shared.Workers)) * s.Duration().Seconds() / (perRequest / (1.19 + 0.67))
	require.Greater(t, float64(shared.Queue), behind, "the queue holds what a solo host's node falls behind")
	for _, node := range s.Topology.MockML.Nodes {
		require.Zero(t, node.MaxConcurrent, node.Name)
	}
	shared.Workers, shared.Queue = one.Workers, one.Queue
	require.Equal(t, one, asOne(shared))

	replay := load("oracle-replay-slow")
	require.Equal(t, 1500, replay.ReplayMs)
	replay.ReplayMs = one.ReplayMs
	require.Equal(t, one, asOne(replay))

	exec := load("oracle-exec-slow")
	phase, err := LoadScenario(filepath.Join("scenarios", "phase-faults.yaml"))
	require.NoError(t, err)
	timeout, err := protocolTimeoutSeconds(phase.Protocol.ExecutionTimeout)
	require.NoError(t, err)
	require.Greater(t, exec.ExecMs, int(clientTimeout.Milliseconds()), "the client gives up while the node is still sending")
	require.Less(t, exec.ExecMs, int(timeout*1000), "the execution still ends within the escrow's execution timeout")
	exec.ExecMs = one.ExecMs
	require.Equal(t, one, asOne(exec))
}

// The runner reads scenarios leniently, so a misspelt key would silently drop a shape, the export or
// the checks.
func TestStreamShapeScenariosRunTheirOracleNodesUnderThePromises(t *testing.T) {
	for name, want := range map[string][]string{
		"chunk-shapes":    {"oracle-chunk1", "oracle-chunk4"},
		"phase-faults":    {"oracle-replay-slow", "oracle-exec-slow", "oracle-chunk1"},
		"drain":           {"oracle-chunk1"},
		"shared-endpoint": {"oracle-shared"},
	} {
		s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err, name)
		require.True(t, s.Assertions.Promises, name)
		require.True(t, s.Diagnostics.Export, name)
		require.Equal(t, "all", s.Diagnostics.LogTail(), name)
		seen := map[string]bool{}
		for _, node := range s.Topology.MockML.Nodes {
			p, err := LoadProfile("profiles", node.Profile)
			require.NoError(t, err, node.Profile)
			require.True(t, p.Oracle, "%s: %s answers replays from the enforced tokens", name, node.Name)
			seen[node.Profile] = true
		}
		for _, profile := range want {
			require.True(t, seen[profile], "%s runs %s", name, profile)
		}
	}
	s, err := LoadScenario(filepath.Join("scenarios", "shared-endpoint.yaml"))
	require.NoError(t, err)
	require.Equal(t, 4, s.Topology.Chain.Escrows, "several escrows share the endpoint")
	require.Len(t, s.Topology.MockML.Nodes, 4, "one node for each versiond container")
}
