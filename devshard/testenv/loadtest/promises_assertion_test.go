package loadtest

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// writePromisesRun is an exported run of one request the client got a 200 for: one escrow, its
// inference started, confirmed and finished with 64 tokens on one host, the export taken an hour
// later, and the mock records given, the execution of seed 1 tied to the request by its prompt. With
// an execution of 64 tokens every check of the run judges something and nothing is broken.
func writePromisesRun(t *testing.T, records ...mockopenai.RequestRecord) string {
	t.Helper()
	const at = int64(1_792_350_000)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.yaml"), []byte("workload:\n  request:\n    model: test-model\n    messages:\n      - {role: user, content: hello}\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requests.jsonl"), []byte(`{"request_id":"r-1","status":200,"outcome":"completed"}`+"\n"), 0o644))
	db := filepath.Join(dir, "export", "postgres")
	require.NoError(t, os.MkdirAll(db, 0o755))
	diff := func(nonce uint64, txs ...*types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "4", Txs: txs})
		require.NoError(t, err)
		return `\x` + hex.EncodeToString(raw)
	}
	// The block at height at is made at unix second at: the Start stamps it as the floor, a host acks
	// it in the same diff, and the ConfirmStart in the next one is anchored at it.
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, Model: "test-model", MaxTokens: 256, ObservedHeight: uint64(at), ObservedBlockHash: unixBlockHash(uint64(at))}}}
	ack := &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: &types.MsgHeightAck{SlotId: 0, ObservedHeight: uint64(at), ObservedBlockHash: unixBlockHash(uint64(at))}}}
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ObservedHeight: uint64(at), ObservedBlockHash: unixBlockHash(uint64(at))}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, OutputTokens: 64}}}
	require.NoError(t, os.WriteFile(filepath.Join(db, "public.devshard_diffs.csv"), []byte("escrow_id,nonce,txs_proto,post_state_root\n"+
		"4,1,"+diff(1, start, ack)+",\\xaa\n4,2,"+diff(2, confirm)+",\\xbb\n4,3,"+diff(3, finish)+",\\xcc\n"), 0o644))
	headers, err := json.Marshal(map[string]any{"headers": []harness.ChainHeader{{Height: uint64(at), Hash: hex.EncodeToString(unixBlockHash(uint64(at))), TimeMs: at * 1000}}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", harness.ChainHeadersName), headers, 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(db, "public.devshard_sessions.csv"), []byte("epoch_id,escrow_id,config_json,status\n"+
		`1,4,"{""RefusalTimeout"":5,""ExecutionTimeout"":30}",active`+"\n"), 0o644))
	body, err := json.Marshal(struct {
		Model    string                   `json:"model"`
		Messages []mockopenai.ChatMessage `json:"messages"`
	}{"test-model", []mockopenai.ChatMessage{{Role: "user", Content: "hello [load-request:r-1]"}}})
	require.NoError(t, err)
	for i := range records {
		if records[i].Phase == mockopenai.PhaseExecute {
			records[i].PromptKey, records[i].Model, records[i].StartUnixMs = mockopenai.PromptKey(body), "test-model", at*1000+300
		}
	}
	raw, err := json.Marshal(map[string]any{"complete": true, "records": records})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "mock-log.json"), raw, 0o644))
	manifest, err := json.Marshal(harness.ExportManifest{ExportedAt: time.Unix(at, 0).Add(time.Hour), Complete: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "EXPORT-MANIFEST.json"), manifest, 0o644))
	return dir
}

func honestExecution(node string) mockopenai.RequestRecord {
	seed := 1
	return mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Node: node, Seed: &seed, Status: 200, Outcome: "ok",
		TokensOut: 64, UsageCompletion: 64}
}

// Test flow:
//  1. A run whose checks judge every inference, beside the same run exported before any deadline
//     passed.
//  2. Hold each to the promises.
//  3. Check the first passes, and the second is not valid: the deadline checks judged nothing.
func TestPromisesAssertionRefusesARunWhoseDeadlineChecksJudgedNothing(t *testing.T) {
	scenario := testScenario()
	profiles := map[string]Profile{"fast": {}}
	dir := writePromisesRun(t, honestExecution("mock-openai-0"))
	check := promisesAssertion(dir, scenario, profiles)
	require.True(t, check.Passed, "%+v", check)

	dir = writePromisesRun(t, honestExecution("mock-openai-0"))
	manifest, err := json.Marshal(harness.ExportManifest{ExportedAt: time.Unix(1_792_350_000, 0).Add(time.Minute), Complete: true})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "EXPORT-MANIFEST.json"), manifest, 0o644))
	check = promisesAssertion(dir, scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Equal(t, "run not valid", check.Actual)
	require.Contains(t, check.Details, harness.InferenceClosesInTime+" judged nothing")
	require.Contains(t, check.Details, harness.ServedRequestFinished+" judged nothing")
}

// Test flow:
//  1. An exported run whose node with fault rules answered every request without firing one, beside
//     one whose node fired its rule.
//  2. Hold each run to the promises.
//  3. Check the first is not valid and names the rule and the node; the second passes.
func TestPromisesAssertionRefusesARunWhoseFaultRulesNeverFired(t *testing.T) {
	scenario := testScenario()
	scenario.Topology.MockML.Nodes = []MockMLNode{{Name: "mock-openai-0", Profile: "fast"}, {Name: "mock-openai-1", Profile: "replay-faults"}}
	profiles := map[string]Profile{"fast": {}, "replay-faults": {Rules: []mockopenai.FaultRule{
		{Name: "replay-busy", Phase: mockopenai.PhaseReplay, PromptContains: "000013]", HTTPStatus: 503}}}}
	run := func(rule string) string {
		seed := 1
		replay := mockopenai.RequestRecord{Phase: mockopenai.PhaseReplay, Node: "mock-openai-1", Seed: &seed, Status: 200, Outcome: "ok"}
		if rule != "" {
			replay.Rule, replay.Status, replay.Outcome = rule, 503, "rule_status"
		}
		return writePromisesRun(t, honestExecution("mock-openai-0"), replay)
	}

	check := promisesAssertion(run(""), scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Contains(t, check.Details, "fault rule replay-busy (mock-openai-1) never fired")

	check = promisesAssertion(run("replay-busy"), scenario, profiles)
	require.True(t, check.Passed, "%+v", check)

	// A rule the scenario gives every node counts once for the run, wherever it fired.
	scenario.Topology.MockML.Rules = []mockopenai.FaultRule{{Name: "exec-cut", Phase: mockopenai.PhaseExecute, PromptContains: "000007]", Abort: true}}
	check = promisesAssertion(run("replay-busy"), scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Contains(t, check.Details, "fault rule exec-cut (mock-openai-0, mock-openai-1) never fired")
}

// Test flow:
//  1. A scenario that requires an ML node to be full, and a run where none was.
//  2. Check the run is not valid; the same run with one request queued for a worker or one 503 at
//     capacity passes, and one where only a host's acquire was refused does not: a host is refused
//     a node it excluded after a fault too.
func TestPromisesAssertionRefusesASharedEndpointRunThatNeverFilledItsNodes(t *testing.T) {
	scenario := testScenario()
	scenario.Assertions.MockML.RequireFullNode = true
	profiles := map[string]Profile{"fast": {}}
	check := promisesAssertion(writePromisesRun(t, honestExecution("mock-openai-0")), scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Contains(t, check.Details, "no ML node was ever full")

	// A full node queues the request as vLLM does: it waited for a worker and was answered.
	queued := honestExecution("mock-openai-0")
	queued.QueuedMs = 1200
	check = promisesAssertion(writePromisesRun(t, queued), scenario, profiles)
	require.True(t, check.Passed, "%+v", check)

	full := mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Node: "mock-openai-1", Status: 503, Outcome: "capacity_exhausted"}
	check = promisesAssertion(writePromisesRun(t, honestExecution("mock-openai-0"), full), scenario, profiles)
	require.True(t, check.Passed, "%+v", check)

	dir := writePromisesRun(t, honestExecution("mock-openai-0"))
	writeHostStatsJSON(t, dir, `{"versiond-0":{"allocations":1,"acquire_errors":0},"versiond-2":{"allocations":0,"acquire_errors":3}}`)
	check = promisesAssertion(dir, scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Contains(t, check.Details, "no ML node was ever full")
}

func writeHostStatsJSON(t *testing.T, dir, body string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "host-stats.json"), []byte(body+"\n"), 0o644))
}

// Test flow:
//  1. A scenario that binds each node to a host, and runs whose mock-dapi host stats show every
//     allocation going to a mapped host, one going to a caller it maps to no host, or no stats.
//  2. Hold each to the promises.
//  3. Check only the first passes: an unmapped caller drew from every node, so the binding did not
//     hold, and without the stats the binding was not checked. A scenario that binds nothing is not
//     held to it.
func TestPromisesAssertionRefusesARunWhoseNodeBindingDidNotHold(t *testing.T) {
	scenario := testScenario()
	scenario.Topology.Participants = 1
	scenario.Topology.MockML.Nodes[0].Host = "versiond-0"
	scenario.Topology.MockML.Nodes[1].Host = "versiond-1"
	profiles := map[string]Profile{"fast": {}}

	dir := writePromisesRun(t, honestExecution("mock-openai-0"))
	writeHostStatsJSON(t, dir, `{"versiond-0":{"allocations":1},"versiond-1":{"allocations":0},"unmapped":{"allocations":0,"acquire_errors":2}}`)
	check := promisesAssertion(dir, scenario, profiles)
	require.True(t, check.Passed, "%+v", check)

	dir = writePromisesRun(t, honestExecution("mock-openai-0"))
	writeHostStatsJSON(t, dir, `{"versiond-0":{"allocations":1},"versiond-1":{"allocations":0},"unmapped":{"allocations":2}}`)
	check = promisesAssertion(dir, scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Equal(t, "run not valid", check.Actual)
	require.Contains(t, check.Details, "mock-dapi handed 2 ML nodes to callers it maps to no host")

	dir = writePromisesRun(t, honestExecution("mock-openai-0"))
	check = promisesAssertion(dir, scenario, profiles)
	require.False(t, check.Passed, "%+v", check)
	require.Contains(t, check.Details, "host-stats.json")

	check = promisesAssertion(dir, testScenario(), profiles)
	require.True(t, check.Passed, "%+v", check)
}

// Test flow:
//  1. Load each scenario that holds a run to the promises.
//  2. Compute, as the deadline checks do, when an inference confirmed at the end of the load is due.
//  3. Check the export the runner holds for the promises comes after it, so the deadline checks judge
//     every inference of the run, and the hold stays within a few minutes.
func TestPromiseScenariosHoldTheExportUntilTheLastInferenceIsDue(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	held := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		if !strings.Contains(string(raw), "\n  promises: true") {
			continue
		}
		s, err := LoadScenario(path)
		require.NoError(t, err, path)
		require.True(t, s.Assertions.Promises, path)
		held++
		require.NotEmpty(t, s.Protocol.ExecutionTimeout, "%s: the default execution timeout (32 min) leaves the deadline checks nothing to judge", path)
		refusal, err := protocolTimeoutSeconds(s.Protocol.RefusalTimeout)
		require.NoError(t, err)
		execution, err := protocolTimeoutSeconds(s.Protocol.ExecutionTimeout)
		require.NoError(t, err)
		hold := promisesHold(refusal, execution)
		due := time.Duration(max(refusal, execution))*time.Second + harness.TimeoutSkew + harness.TimeoutVoteWindow + harness.TimeoutSweepPeriod + harness.DeadlineSlack
		require.Greater(t, hold, due, path)
		require.Less(t, hold, 10*time.Minute, path)
	}
	require.Greater(t, held, 4)
}

// Test flow:
//  1. Load every scenario held to the promises that also checks for orphaned work.
//  2. Compute when the gateway times out an inference confirmed at the end of the load: its execution
//     timeout, the gateway's grace and one sweep.
//  3. Check the orphan check waits at least that long, so an inference still open after a short
//     drain is not reported as orphaned while the gateway has yet to sweep it.
func TestOrphanCheckWaitsForTheGatewaysTimeoutSweep(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("scenarios", "*.yaml"))
	require.NoError(t, err)
	checked := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		if !strings.Contains(string(raw), "\n  promises: true") {
			continue
		}
		s, err := LoadScenario(path)
		require.NoError(t, err, path)
		if !s.Assertions.Devshard.NoOrphanedWork {
			continue
		}
		checked++
		refusal, err := protocolTimeoutSeconds(s.Protocol.RefusalTimeout)
		require.NoError(t, err, path)
		execution, err := protocolTimeoutSeconds(s.Protocol.ExecutionTimeout)
		require.NoError(t, err, path)
		swept := time.Duration(max(refusal, execution))*time.Second + harness.TimeoutSkew + harness.TimeoutVoteWindow + harness.TimeoutSweepPeriod
		require.GreaterOrEqual(t, s.orphanWait(), swept, "%s: drain_timeout %s", path, s.DrainDuration())
	}
	require.Greater(t, checked, 4)
}

func TestReadmeStatesTheEscrowCountOnce(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(string(raw), "`topology.chain.escrows` is how many escrows"))
}

func TestReadmeNamesEveryPromiseAndOracleKnob(t *testing.T) {
	raw, err := os.ReadFile("README.md")
	require.NoError(t, err)
	readme := string(raw)
	for _, c := range harness.PromiseCatalog {
		require.Contains(t, readme, "`"+c.Name+"`", "property table")
	}
	for _, key := range []string{"oracle", "chunk_tokens", "first_content_ms", "exec_ms", "replay_ms", "rules",
		"mock_ml.require_full_node", "oracle-chunk1", "oracle-chunk4", "oracle-exec-slow", "oracle-replay-slow", "oracle-shared",
		"chunk-shapes", "phase-faults", "drain", "shared-endpoint"} {
		require.Contains(t, readme, "`"+key+"`", key)
	}
}

func TestAProfileRuleWithAStatusOutsideTheErrorRangeIsRefused(t *testing.T) {
	head := "schema_version: v1\nprofile: bad\nttft: 2ms\ntoken_interval: 0s\nworkers: 1\nqueue: 0\n"
	for _, status := range []string{"600", "1000", "5030"} {
		dir := t.TempDir()
		body := head + "rules:\n  - name: x\n    prompt_contains: load-request\n    http_status: " + status + "\n"
		require.NoError(t, os.WriteFile(filepath.Join(dir, "bad.yaml"), []byte(body), 0o644))
		_, err := LoadProfile(dir, "bad")
		require.Error(t, err, status)
	}
}

// Test flow:
//  1. phase-faults gives its fault rules to every node, so a rule fires whichever node the pool hands
//     the request.
//  2. Check every node of the scenario runs every rule, each rule selects requests by a substring
//     several requests of the run carry, and the execution fault has a node that streams past the
//     client's timeout beside it.
func TestPhaseFaultsGivesEveryNodeItsRules(t *testing.T) {
	s, err := LoadScenario(filepath.Join("scenarios", "phase-faults.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, s.Topology.MockML.Rules)
	phases := map[string]bool{}
	for _, r := range s.Topology.MockML.Rules {
		phases[r.Phase] = true
	}
	require.True(t, phases[mockopenai.PhaseExecute] && phases[mockopenai.PhaseReplay], "a fault for each phase")
	for _, node := range s.Topology.MockML.Nodes {
		p, err := LoadProfile("profiles", node.Profile)
		require.NoError(t, err)
		require.Empty(t, p.Rules, "%s: the rules come from the scenario, for every node alike", node.Profile)
	}
	workDir := t.TempDir()
	profiles := map[string]Profile{}
	for _, node := range s.Topology.MockML.Nodes {
		profiles[node.Profile], err = LoadProfile("profiles", node.Profile)
		require.NoError(t, err)
	}
	require.NoError(t, writeRunnerConfig(testenvWithBaseConfig(t), workDir, s, profiles, false, nil))
	cfg, err := config.Load(filepath.Join(workDir, "config.yaml"))
	require.NoError(t, err)
	for _, node := range cfg.MockOpenAI.Nodes {
		var rules []mockopenai.FaultRule
		require.NoError(t, json.Unmarshal([]byte(node.RulesJSON), &rules), node.Name)
		require.Equal(t, s.Topology.MockML.Rules, rules, node.Name)
	}
}
