package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

func TestDishonestProfilesHandTheMockTheirChaos(t *testing.T) {
	for name, action := range map[string]string{"wrong-answer": mockopenai.ChaosWrongAnswer, "overclaim": mockopenai.ChaosOddUsage} {
		p, err := LoadProfile("profiles", name)
		require.NoError(t, err, name)
		raw, err := p.chaosJSON()
		require.NoError(t, err)
		var c mockopenai.ChaosConfig
		require.NoError(t, json.Unmarshal([]byte(raw), &c))
		require.True(t, c.Enabled, "on from the start")
		require.Equal(t, map[string]int{mockopenai.ChaosOK: 4, action: 1}, c.Weights, name)
	}
	empty, err := LoadProfile("profiles", "empty-stream")
	require.NoError(t, err)
	raw, err := empty.chaosJSON()
	require.NoError(t, err)
	var c mockopenai.ChaosConfig
	require.NoError(t, json.Unmarshal([]byte(raw), &c))
	require.Equal(t, map[string]int{mockopenai.ChaosEmpty: 1}, c.Weights, "every execution is an empty stream")
	honest, err := LoadProfile("profiles", "fast")
	require.NoError(t, err)
	raw, err = honest.chaosJSON()
	require.NoError(t, err)
	require.Empty(t, raw, "a profile without chaos answers honestly")
}

func TestAProfileWithAnUnknownChaosActionIsRefused(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(
		"schema_version: v1\nprofile: bad\nttft: 2ms\ntoken_interval: 0s\nworkers: 1\nqueue: 0\nchaos:\n  weights:\n    lie_politely: 1\n"), 0o644))
	_, err := LoadProfile(dir, "bad")
	require.ErrorContains(t, err, "chaos")

	require.NoError(t, os.WriteFile(filepath.Join(dir, "empty.yaml"), []byte(
		"schema_version: v1\nprofile: empty\nttft: 2ms\ntoken_interval: 0s\nworkers: 1\nqueue: 0\nchaos:\n  seed: 1\n"), 0o644))
	_, err = LoadProfile(dir, "empty")
	require.ErrorContains(t, err, "chaos needs weights", "no weights would fall back to every shape, not the one the profile names")
}

// The runner reads scenarios leniently, so a misspelt key would silently drop the export the
// dishonest-executor checks read.
func TestDishonestScenariosExportTheRunWithTheWholeLog(t *testing.T) {
	for _, name := range []string{"dishonest-executor-wrong-answer", "dishonest-executor-overclaim"} {
		s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err, name)
		require.True(t, s.Diagnostics.Export, name)
		require.Equal(t, "all", s.Diagnostics.LogTail(), name)
	}
}

func TestDishonestScenariosHoldTheRunToThePromises(t *testing.T) {
	for _, name := range []string{"dishonest-executor-wrong-answer", "dishonest-executor-overclaim"} {
		s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err, name)
		require.True(t, s.Assertions.Promises, name)
	}
}

func TestPromisesNeedTheExport(t *testing.T) {
	scenario := testScenario()
	scenario.Assertions.Promises = true
	require.ErrorContains(t, scenario.Validate(), "assertions.promises requires diagnostics.export")
	scenario.Diagnostics.Export = true
	scenario.Diagnostics.Logs.Tail = "all"
	require.NoError(t, scenario.Validate())
}

// The checks name the executor of every inference from compose.log; a log cut to its last lines
// would hide the early ones.
func TestPromisesNeedTheWholeComposeLog(t *testing.T) {
	scenario := testScenario()
	scenario.Assertions.Promises = true
	scenario.Diagnostics.Export = true
	require.ErrorContains(t, scenario.Validate(), "assertions.promises requires diagnostics.logs.tail: all")
	scenario.Diagnostics.Logs.Tail = "all"
	require.NoError(t, scenario.Validate())
}

// Test flow:
//  1. A finished run directory holds the runner's request log and the scenario but no export; then a
//     run whose export every check can judge.
//  2. Hold each run to the promises.
//  3. Check the first fails and says why, and the second passes with the report written as JSON and
//     Markdown.
func TestPromisesAssertionWritesTheReport(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.yaml"), []byte("workload:\n  request:\n    model: test-model\n    messages:\n      - {role: user, content: hello}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requests.jsonl"), []byte(`{"request_id":"r-1","status":200,"outcome":"completed"}`+"\n"), 0o644))

	check := promisesAssertion(dir, testScenario(), nil)
	require.False(t, check.Passed)
	require.Contains(t, check.Details, "devshard_diffs.csv")

	dir = writePromisesRun(t, honestExecution("mock-openai-0"))
	check = promisesAssertion(dir, testScenario(), nil)
	require.True(t, check.Passed, "%+v", check)
	require.Equal(t, "promises", check.Name)
	for _, name := range []string{"report.json", "report.md"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.NoError(t, err, name)
	}
}

// The honest twin is the control of the dishonest-executor scenarios: the same stack, load and
// checks with every ML node honest, so a violation the dishonest run reports is the dishonest node's.
func TestTheHonestTwinDiffersOnlyInTheDishonestNode(t *testing.T) {
	twin, err := LoadScenario(filepath.Join("scenarios", "dishonest-executor-honest.yaml"))
	require.NoError(t, err)
	require.True(t, twin.Assertions.Promises)
	for _, n := range twin.Topology.MockML.Nodes {
		p, err := LoadProfile("profiles", n.Profile)
		require.NoError(t, err)
		require.Nil(t, p.Chaos, "node %s answers honestly", n.Name)
	}
	for _, name := range []string{"dishonest-executor-wrong-answer", "dishonest-executor-overclaim"} {
		s, err := LoadScenario(filepath.Join("scenarios", name+".yaml"))
		require.NoError(t, err)
		s.Scenario = twin.Scenario
		for i := range s.Topology.MockML.Nodes {
			s.Topology.MockML.Nodes[i].Profile = twin.Topology.MockML.Nodes[i].Profile
		}
		require.Equal(t, s, twin, name)
	}
}

// The wave keeps one node on empty streams for the whole run and the empty-stream quarantine short,
// so the hosts keep winning with it; its twin differs only in that node.
func TestTheChallengeWaveAndItsTwin(t *testing.T) {
	wave, err := LoadScenario(filepath.Join("scenarios", "usage-challenge-wave.yaml"))
	require.NoError(t, err)
	require.True(t, wave.Assertions.Promises)
	require.Equal(t, "1s", wave.Gateway.ParticipantThrottle.EmptyStreamQuarantine)
	var empty []string
	for _, n := range wave.Topology.MockML.Nodes {
		if n.Profile == "empty-stream" {
			empty = append(empty, n.Name)
		}
	}
	require.Equal(t, []string{"mock-openai-3"}, empty)

	twin, err := LoadScenario(filepath.Join("scenarios", "usage-challenge-wave-honest.yaml"))
	require.NoError(t, err)
	for _, n := range twin.Topology.MockML.Nodes {
		require.Equal(t, "fast", n.Profile, n.Name)
	}
	wave.Scenario = twin.Scenario
	for i := range wave.Topology.MockML.Nodes {
		wave.Topology.MockML.Nodes[i].Profile = "fast"
	}
	require.Equal(t, wave, twin)
}

// Test flow:
//  1. An exported run whose dishonest node answered only honestly, beside one whose dishonest node
//     gave one dishonest answer.
//  2. Hold each run to the promises.
//  3. Check the first is not valid, naming the node, and fails; the second passes.
func TestPromisesAssertionRefusesARunWhoseDishonestNodeNeverLied(t *testing.T) {
	scenario := testScenario()
	scenario.Topology.MockML.Nodes = []MockMLNode{{Name: "mock-openai-0", Profile: "fast"}, {Name: "mock-openai-1", Profile: "overclaim"}}
	profiles := map[string]Profile{"fast": {}, "overclaim": {Chaos: &ProfileChaos{Seed: 7, Weights: map[string]int{"ok": 4, "odd_usage": 1}}}}
	run := func(chaos string) string {
		lie := honestExecution("mock-openai-1")
		lie.Chaos = chaos
		lie.Seed = nil
		return writePromisesRun(t, honestExecution("mock-openai-0"), lie)
	}

	dir := run(mockopenai.ChaosOK)
	check := promisesAssertion(dir, scenario, profiles)
	require.False(t, check.Passed)
	require.Contains(t, check.Details, "mock-openai-1 (profile overclaim) gave no dishonest answer")
	raw, err := os.ReadFile(filepath.Join(dir, "report.md"))
	require.NoError(t, err)
	require.Contains(t, string(raw), "**Run not valid:** mock-openai-1 (profile overclaim) gave no dishonest answer")

	check = promisesAssertion(run(mockopenai.ChaosOddUsage), scenario, profiles)
	require.True(t, check.Passed, "%+v", check)
}
