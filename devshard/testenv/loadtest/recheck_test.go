package loadtest

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const recheckScenario = `schema_version: v1
scenario: recheck
seed: 1
topology:
  versiond_mode: multi
  storage: per_participant
  participants: 4
  chain:
    escrow_amount: 1000000
    max_nonce: 1000
  mock_ml:
    allocator: round_robin
    nodes:
      - {name: n2, profile: fast, host: versiond-3}
      - {name: n3, profile: fast, host: versiond-4}
workload:
  max_in_flight: 4
  duration: 10m
  traffic: {type: constant, rps: 1}
  request:
    model: m-a
    max_tokens: 8
    messages: [{role: user, content: hi}]
cases:
  - {id: escrow-rotation-per-model-settlement, title: rotation}
  - {id: host-under-poc-challenge, title: challenge, known_bug: "#1823"}
assertions:
  requests: {http_status: 200, terminal_outcome: completed, error_rate: 0.01}
  devshard: {no_orphaned_work: true, max_ghost_rate: 0.05}
  mock_ml: {require_each_node_used: true}
  invariants:
    enabled: true
    grace: 95s
    max_pending_age: 220s
    baseline: {from: 1m, to: 2m}
    trend_early: {from: 2m, to: 4m}
    trend_late: {from: 8m, to: 10m}
    cases: {no-runtime-for-deactivated-escrow: escrow-rotation-per-model-settlement}
drain_timeout: 1m
`

func asValues[T any](samples []T) []any {
	values := make([]any, 0, len(samples))
	for _, sample := range samples {
		values = append(values, sample)
	}
	return values
}

func writeSavedRun(t *testing.T, dir string, in InvariantInputs, inputs RunInputs) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.yaml"), []byte(recheckScenario), 0o644))
	summary := Summary{Scenario: "recheck", StartedAt: in.Started, Requests: len(in.Requests), Completed: len(in.Requests)}
	body, err := json.Marshal(summary)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "summary.json"), body, 0o644))
	require.NoError(t, writeEventRecords(dir, in.Events))
	writeJSONLines(t, filepath.Join(dir, "requests.jsonl"), asValues(in.Requests)...)
	writeJSONLines(t, filepath.Join(dir, "metrics.jsonl"), asValues(in.Metrics)...)
	writeJSONLines(t, filepath.Join(dir, "accounting-snapshots.jsonl"), asValues(in.Accounting)...)
	writeJSONLines(t, filepath.Join(dir, "throttle.jsonl"), asValues(in.Throttle)...)
	writeJSONLines(t, filepath.Join(dir, "pending.jsonl"), asValues(in.Pending)...)
	writeJSONLines(t, filepath.Join(dir, "host-stats.jsonl"), asValues(in.HostStats)...)
	writeJSONLines(t, filepath.Join(dir, "dapi-host-stats.jsonl"), asValues(in.DapiHosts)...)
	if inputs.Hosts != nil {
		require.NoError(t, writeRunInputs(dir, inputs))
	}
}

func savedRunFixture(t *testing.T) (InvariantInputs, RunInputs) {
	t.Helper()
	scenario, err := loadScenarioBytes(t, recheckScenario)
	require.NoError(t, err)
	in := passingInputs()
	in.Settings = resolveInvariantSettings(scenario)
	in.NodeHosts = map[string]string{"n2": "versiond-3", "n3": "versiond-4"}
	in.ChainMaxNonce = 1000
	in.DidNotCompress = 0
	in.EscrowMissing = []string{}
	in.Burns = []gatewayBurn{{At: at(3*time.Minute + 30*time.Second), EscrowID: "11", Nonce: "2", Host: "p2", Reason: "participant_ejected_no_send"}}
	inferences := map[string]debugInference{"1": {Status: "finished"}, "2": {Status: "pending", StartedAt: at(3 * time.Minute).Unix()}}
	inputs := RunInputs{
		Started: in.Started, Hosts: in.Hosts, Allocations: map[string]uint64{"n2": 3, "n3": 5}, Burns: in.Burns,
		Orphans: &orphanPoll{Last: &orphanSnapshot{At: at(11 * time.Minute), Runtimes: []orphanRuntime{{EscrowID: "11", Active: true, Inferences: inferences}}}},
		Invariants: &invariantEndState{
			SettlementDisabled: in.SettlementDisabled, Devshards: in.Devshards, Inferences: in.Inferences, SettleTxs: in.SettleTxs, Escrows: in.Escrows,
			Leases: in.Leases, EscrowMissing: in.EscrowMissing, DidNotCompress: in.DidNotCompress,
		},
	}
	return in, inputs
}

func loadScenarioBytes(t *testing.T, body string) (Scenario, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "scenario.yaml")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return LoadScenario(path)
}

// Test flow:
// 1. Save the passing invariant fixture as a run directory: run.yaml, summary.json, events.json, the JSONL artifacts and run-inputs.json, with one ejected burn inside p2's fault window.
// 2. Judge the saved run with EvaluateSavedRun, as -recheck does.
// 3. Require every invariant check to equal the in-memory evaluation of the same inputs, and invariants.json to be rewritten.
// 4. Require the drain check to leave the ejected burn out of the ghost rate, the known_bug case to be marked, and assertions.json to record a pass.
func TestRecheckJudgesASavedRunLikeTheLiveEvaluation(t *testing.T) {
	in, inputs := savedRunFixture(t)
	dir := t.TempDir()
	writeSavedRun(t, dir, in, inputs)
	expected, _ := evaluateInvariants(in)

	passed, notes, err := EvaluateSavedRun(dir, false)

	require.NoError(t, err)
	require.Empty(t, notes)
	var saved struct {
		Passed bool              `json:"passed"`
		Checks []AssertionResult `json:"checks"`
	}
	require.NoError(t, readReportArtifact(dir, "assertions.json", true, &saved))
	byName := map[string]AssertionResult{}
	for _, check := range saved.Checks {
		byName[check.Name] = check
	}
	for _, check := range expected {
		got, ok := byName[check.Name]
		require.True(t, ok, check.Name)
		if check.Case == "host-under-poc-challenge" {
			require.Equal(t, "XPASS", got.Status, check.Name)
			got.Status = check.Status
		}
		check.spans = nil
		require.Equal(t, check, got)
	}
	require.FileExists(t, filepath.Join(dir, "invariants.json"))
	orphans := byName["devshard.no_orphaned_work"]
	require.True(t, orphans.Passed, orphans.Details)
	require.Contains(t, orphans.Actual, "ghost=0 ghost_excluded=1 total=1")
	require.True(t, byName["mock_ml.node_used.n3"].Passed)
	require.Equal(t, passed, saved.Passed)
	require.True(t, passed, strings.Join(failedNames(saved.Checks), ", "))
}

// Test flow:
// 1. Save the same run without run-inputs.json, as runs before this file were written.
// 2. Judge it with EvaluateSavedRun.
// 3. Require notes naming what was not saved, the drain check and the chain-ledger check to fail as unavailable, and the request, trend and pending checks to still be judged.
func TestRecheckJudgesARunSavedWithoutItsInputs(t *testing.T) {
	in, _ := savedRunFixture(t)
	dir := t.TempDir()
	writeSavedRun(t, dir, in, RunInputs{})

	passed, notes, err := EvaluateSavedRun(dir, false)

	require.NoError(t, err)
	require.False(t, passed)
	require.Contains(t, strings.Join(notes, "\n"), "devshard.no_orphaned_work: the drain check's last read "+notSaved)
	require.Contains(t, strings.Join(notes, "\n"), "compose-pre-drain.log not readable")
	var saved struct {
		Checks []AssertionResult `json:"checks"`
	}
	require.NoError(t, readReportArtifact(dir, "assertions.json", true, &saved))
	byName := map[string]AssertionResult{}
	for _, check := range saved.Checks {
		byName[check.Name] = check
	}
	require.False(t, byName["devshard.no_orphaned_work"].Passed)
	require.Equal(t, "unavailable", byName["devshard.no_orphaned_work"].Actual)
	require.False(t, byName["invariants.no-runtime-for-deactivated-escrow.no_resident_deactivated"].Passed)
	for _, name := range []string{"requests.error_rate", "invariants.no-restarts-or-resource-growth.heap_slope", "invariants.no-nonce-left-pending.max_pending_age", "invariants.misses-only-in-fault-windows.honest_misses_gateway"} {
		require.True(t, byName[name].Passed, "%s: %s %s", name, byName[name].Actual, byName[name].Details)
	}
}

func failedNames(checks []AssertionResult) []string {
	var names []string
	for _, check := range failedChecks(checks) {
		names = append(names, check.Name+" "+check.Actual+" "+check.Details)
	}
	return names
}

// Test flow:
// 1. Wrap a failed check's assertionFailures in errors.Join, as the run's deferred event cleanup does.
// 2. Write assertions.json with it.
// 3. Require failed_assertions to name the failed check.
func TestWriteAssertionsNamesFailuresInsideAJoinedError(t *testing.T) {
	dir := t.TempDir()
	check := AssertionResult{Name: "requests.error_rate"}

	require.NoError(t, writeAssertions(dir, []AssertionResult{check}, errors.Join(&assertionFailures{Checks: []AssertionResult{check}}, errors.New("event e1: never applied"))))

	var saved struct {
		FailedAssertions []string `json:"failed_assertions"`
	}
	require.NoError(t, readReportArtifact(dir, "assertions.json", true, &saved))
	require.Equal(t, []string{"requests.error_rate"}, saved.FailedAssertions)
}
