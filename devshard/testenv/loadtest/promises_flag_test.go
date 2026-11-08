package loadtest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// Test flow:
//  1. Turn the promise checks on in every scenario file of scenarios/.
//  2. Check each edited file loads and validates, with assertions.promises, diagnostics.export and
//     diagnostics.logs.tail all set.
//  3. Check every other field decodes as in the original file.
func TestWithPromisesTurnsOnTheChecksAndKeepsTheScenario(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	for _, path := range paths {
		want, err := LoadScenario(path)
		require.NoError(t, err, path)
		raw, err := os.ReadFile(path)
		require.NoError(t, err, path)

		edited, err := withPromises(raw)
		require.NoError(t, err, path)
		var got Scenario
		require.NoError(t, yaml.Unmarshal(edited, &got), path)
		require.NoError(t, got.Validate(), path)
		require.True(t, got.Assertions.Promises, path)
		require.True(t, got.Diagnostics.Export, path)
		require.Equal(t, "all", got.Diagnostics.LogTail(), path)

		want.Assertions.Promises, want.Diagnostics.Export, want.Diagnostics.Logs.Tail = true, true, "all"
		require.Equal(t, want, got, path)
	}
}

// Test flow:
//  1. Edit a scenario with no diagnostics, one whose logs are a flow mapping with a line count, and
//     one whose assertions are empty.
//  2. Check each gets the three keys, the line count is replaced and the other keys stay.
func TestWithPromisesFillsMissingAndShortSections(t *testing.T) {
	for name, src := range map[string]string{
		"no diagnostics":   "scenario: a\nassertions:\n  requests: {error_rate: 0.01}\n",
		"flow logs":        "scenario: a\ndiagnostics:\n  logs: {tail: \"500\"}\n  cpu_profiles: []\nassertions: {promises: false}\n",
		"empty assertions": "scenario: a\nassertions:\ndiagnostics: {export: false}\n",
	} {
		edited, err := withPromises([]byte(src))
		require.NoError(t, err, name)
		var got Scenario
		require.NoError(t, yaml.Unmarshal(edited, &got), name)
		require.Equal(t, "a", got.Scenario, name)
		require.True(t, got.Assertions.Promises, name)
		require.True(t, got.Diagnostics.Export, name)
		require.Equal(t, "all", got.Diagnostics.Logs.Tail, name)
	}
	edited, err := withPromises([]byte("scenario: a\nassertions:\n  requests: {error_rate: 0.01}\n"))
	require.NoError(t, err)
	var got Scenario
	require.NoError(t, yaml.Unmarshal(edited, &got))
	require.InDelta(t, 0.01, got.Assertions.Requests.ErrorRate, 1e-12)
	_, err = withPromises([]byte("- a list\n"))
	require.Error(t, err)
}

// Test flow:
//  1. Copy faults-host.yaml, which asks for no export, as a run's run.yaml and rewrite it the way the
//     runner does under -promises.
//  2. Check the loaded scenario and the file on disk both carry the checks, and the hold covers the
//     scenario's protocol timeouts.
func TestPromisesRunFileRewritesTheRunCopy(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("scenarios", "faults-host.yaml"))
	require.NoError(t, err)
	original, err := LoadScenario(filepath.Join("scenarios", "faults-host.yaml"))
	require.NoError(t, err)
	require.False(t, original.Assertions.Promises)
	require.Zero(t, original.PromisesWait())
	path := filepath.Join(t.TempDir(), "run.yaml")
	require.NoError(t, os.WriteFile(path, raw, 0o644))

	scenario, err := promisesRunFile(path)
	require.NoError(t, err)
	require.True(t, scenario.Assertions.Promises)
	reloaded, err := LoadScenario(path)
	require.NoError(t, err)
	require.Equal(t, scenario, reloaded)
	viaPath, err := LoadScenarioWithPromises(filepath.Join("scenarios", "faults-host.yaml"))
	require.NoError(t, err)
	require.Equal(t, scenario, viaPath)
	require.Equal(t, promisesHold(5, 90), scenario.PromisesWait())
}
