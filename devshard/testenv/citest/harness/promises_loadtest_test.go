package harness

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/testenv/mockopenai"
	"devshard/types"
)

const loadtestRunYAMLFixture = `scenario: dishonest-executor-overclaim
seed: 1515
workload:
  request:
    model: model-a
    messages:
      - {role: user, content: "base prompt"}
diagnostics:
  export: true
  logs:
    tail: all
`

// loadtestKey is the prompt key the mock logs for a generator request with this prompt.
func loadtestKey(t *testing.T, model, requestID string, msgs ...mockopenai.ChatMessage) string {
	t.Helper()
	msgs[len(msgs)-1].Content += " [load-request:" + requestID + "]"
	body, err := json.Marshal(map[string]any{"model": model, "messages": msgs})
	require.NoError(t, err)
	return mockopenai.PromptKey(body)
}

func writeLoadtestRun(t *testing.T, requests []map[string]any) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "run.yaml"), []byte(loadtestRunYAMLFixture), 0o644))
	var lines []string
	for _, r := range requests {
		raw, err := json.Marshal(r)
		require.NoError(t, err)
		lines = append(lines, string(raw))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requests.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
	return dir
}

// Test flow:
//  1. A run sent the scenario's one request five times and replayed one from a dataset; the mock
//     log holds the executions of four of them and one validation replay, and the diffs hold their
//     inferences beside an older escrow and another model that reuse the same ids.
//  2. Read the requests.
//  3. Check each request is tied to the inferences its executions started, by prompt, model, seed
//     and time; the executor is the host named for the inference; a 200 the generator did not
//     accept is incomplete; a dropped request is left out; the replayed one is counted, not tied.
func TestReadLoadtestRequestsTiesRequestsToInferencesThroughTheMockLog(t *testing.T) {
	const at = int64(1_792_350_000)
	prompt := func(requestID string) string {
		return loadtestKey(t, "model-a", requestID, mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	}
	kA, kB, kC := prompt("r-1"), prompt("r-2"), prompt("r-3")
	rec := func(key, model, node string, seed int, startMs int64, status int, outcome string) mockopenai.RequestRecord {
		return mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, PromptKey: key, Model: model, Node: node,
			Seed: intPtr(seed), StartUnixMs: startMs, Status: status, Outcome: outcome}
	}
	mock := []mockopenai.RequestRecord{
		rec(kA, "model-a", "mock-openai-0", 7, at*1000+300, 200, "ok"),
		rec(kA, "model-a", "mock-openai-2", 8, at*1000+400, 200, "ok"), // the raced second inference
		rec(kB, "model-a", "mock-openai-2", 10, at*1000+500, 200, "ok"),
		rec(kC, "model-a", "mock-openai-3", 9, at*1000+600, 400, "rule_status"),
		// Validation replays name the prompt too; they are not executions.
		{Phase: mockopenai.PhaseReplay, PromptKey: kA, Model: "model-a", Seed: intPtr(7), StartUnixMs: at*1000 + 900, Status: 200, Outcome: "ok"},
	}
	facts := func(model string, startedAt int64) *InferenceFacts {
		return &InferenceFacts{Started: true, Model: model, StartedAt: startedAt}
	}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		{Escrow: "1", Inference: 7}:  facts("model-a", at),
		{Escrow: "1", Inference: 8}:  facts("model-a", at),
		{Escrow: "1", Inference: 10}: facts("model-a", at),
		{Escrow: "1", Inference: 9}:  facts("model-a", at),
		// The same id on an escrow of another model, in the window: not a candidate.
		{Escrow: "2", Inference: 10}: facts("model-b", at),
		// The same id on an older escrow of the model, out of the window: not a candidate.
		{Escrow: "0", Inference: 7}: facts("model-a", at-600),
	}}
	dir := writeLoadtestRun(t, []map[string]any{
		{"request_id": "r-1", "status": 200, "outcome": "completed"},
		{"request_id": "r-2", "status": 200, "outcome": "error", "error": "response has no choices"},
		{"request_id": "r-3", "status": 502, "outcome": "error"},
		{"request_id": "r-4", "status": 503, "outcome": "error"}, // never reached the ML
		{"request_id": "r-5", "outcome": "dropped_by_generator", "error": "max_in_flight reached"},
		{"request_id": "r-6", "sample_index": 0, "status": 200, "outcome": "completed"},
	})
	require.True(t, IsLoadtestRun(dir))

	got, rep, err := ReadLoadtestRequests(dir, mock, ExportDiffs{Hosts: []HostChain{chain}},
		map[InferenceRef]string{{Escrow: "1", Inference: 7}: "versiond-0", {Escrow: "1", Inference: 8}: "versiond-2"})
	require.NoError(t, err)
	require.Equal(t, LoadtestLinkReport{Requests: 5, Keyed: 3, Executions: 4, Linked: 4, Replayed: true, Dropped: 1}, rep)
	require.Len(t, got, 5, "a request the generator dropped is not a client request")

	require.Equal(t, kA, got[0].PromptKey)
	require.Equal(t, []InferenceRef{{Escrow: "1", Inference: 7}, {Escrow: "1", Inference: 8}}, got[0].Inferences())
	require.Equal(t, "versiond-0", got[0].Executions[0].Participant)
	require.Equal(t, "versiond-2", got[0].Executions[1].Participant)
	require.Equal(t, "mock-openai-2", got[0].Executions[1].Instance)
	require.False(t, got[0].Incomplete)

	require.Equal(t, []InferenceRef{{Escrow: "1", Inference: 10}}, got[1].Inferences(), "the model keeps the escrows apart")
	require.True(t, got[1].Incomplete, "a 200 the generator did not accept has no answer")

	require.Equal(t, 502, got[2].HTTPStatus)
	require.Equal(t, kC, got[2].PromptKey)
	require.Empty(t, got[3].PromptKey)
	require.Empty(t, got[3].Executions)
	require.Empty(t, got[4].PromptKey, "a replayed request carries no tag")
}

func TestReadLoadtestRequestsLeavesAnAmbiguousExecutionUntied(t *testing.T) {
	const at = int64(1_792_350_000)
	k := loadtestKey(t, "model-a", "r-1", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	mock := []mockopenai.RequestRecord{{Phase: mockopenai.PhaseExecute, PromptKey: k, Model: "model-a", Seed: intPtr(7),
		StartUnixMs: at*1000 + 300, Status: 200, Outcome: "ok"}}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		{Escrow: "1", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at},
		{Escrow: "3", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at},
	}}
	dir := writeLoadtestRun(t, []map[string]any{{"request_id": "r-1", "status": 200, "outcome": "completed"}})
	got, rep, err := ReadLoadtestRequests(dir, mock, ExportDiffs{Hosts: []HostChain{chain}}, nil)
	require.NoError(t, err)
	require.Equal(t, 1, rep.AmbiguousExec)
	require.Empty(t, got[0].Executions, "two escrows of the model hold that id in the window: not guessed")
}

// Test flow:
//  1. Two escrows of the model hold inference 7 started at the same second; the mock ran one
//     execution with seed 7 for request r-1, and a host stored the prompt of escrow 3's inference 7
//     with r-1's tag (escrow 1's prompt is another request's).
//  2. Read the requests.
//  3. Check the execution is tied to escrow 3 through the stored prompt, where model, seed and time
//     alone leave it ambiguous.
func TestReadLoadtestRequestsTiesAnExecutionThroughTheStoredPrompt(t *testing.T) {
	const at = int64(1_792_350_000)
	k := loadtestKey(t, "model-a", "r-1", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	mock := []mockopenai.RequestRecord{{Phase: mockopenai.PhaseExecute, PromptKey: k, Model: "model-a", Seed: intPtr(7),
		StartUnixMs: at*1000 + 300, Status: 200, Outcome: "ok"}}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		{Escrow: "1", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at},
		{Escrow: "3", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at},
	}}
	dir := writeLoadtestRun(t, []map[string]any{{"request_id": "r-1", "status": 200, "outcome": "completed"}})
	prompt := func(id string) string {
		return `\x` + hex.EncodeToString([]byte(`{"messages":[{"content":"base prompt [load-request:`+id+`]","role":"user"}],"model":"model-a"}`))
	}
	payloads := filepath.Join(dir, "export", "db-postgres-versiond-1", "postgres", "public.payload_storage.csv")
	require.NoError(t, os.MkdirAll(filepath.Dir(payloads), 0o755))
	require.NoError(t, os.WriteFile(payloads, []byte("escrow_id,inference_id,epoch_id,prompt_payload,response_payload\n"+
		"1,7,1,"+prompt("r-2")+",\\x00\n3,7,1,"+prompt("r-1")+",\\x00\n"), 0o644))

	got, rep, err := ReadLoadtestRequests(dir, mock, ExportDiffs{Hosts: []HostChain{chain}}, nil)
	require.NoError(t, err)
	require.Zero(t, rep.AmbiguousExec)
	require.Equal(t, 1, rep.LinkedPayload)
	require.Len(t, got[0].Executions, 1)
	require.Equal(t, InferenceRef{Escrow: "3", Inference: 7}, got[0].Executions[0].InferenceRef)
}

// Test flow:
//  1. Escrow 4 holds inference 150, whose prompt a host stored with r-1's tag; the mock ran r-1's
//     execution with seed 150, and a second earlier, at capacity, refused one of r-2 with seed 150
//     too, for an inference of another escrow that no exported host holds.
//  2. Read the requests.
//  3. Check r-1's execution is tied to escrow 4's inference 150, and r-2's refusal is not tied to it
//     by model, seed and time: the stored prompt says the inference is r-1's.
func TestReadLoadtestRequestsDoesNotTieAnotherRequestToAStoredInference(t *testing.T) {
	const at = int64(1_792_350_000)
	kA := loadtestKey(t, "model-a", "r-1", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	kB := loadtestKey(t, "model-a", "r-2", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	mock := []mockopenai.RequestRecord{
		{Phase: mockopenai.PhaseExecute, PromptKey: kB, Model: "model-a", Seed: intPtr(150), StartUnixMs: at*1000 - 1100, Status: 503, Outcome: "capacity_exhausted"},
		{Phase: mockopenai.PhaseExecute, PromptKey: kA, Model: "model-a", Seed: intPtr(150), StartUnixMs: at*1000 + 25, Status: 200, Outcome: "ok"},
	}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		{Escrow: "4", Inference: 150}: {Started: true, Model: "model-a", StartedAt: at},
	}}
	dir := writeLoadtestRun(t, []map[string]any{
		{"request_id": "r-1", "status": 200, "outcome": "completed"},
		{"request_id": "r-2", "status": 502, "outcome": "error"},
	})
	payloads := filepath.Join(dir, "export", "db-postgres-versiond-1", "postgres", "public.payload_storage.csv")
	require.NoError(t, os.MkdirAll(filepath.Dir(payloads), 0o755))
	prompt := `\x` + hex.EncodeToString([]byte(`{"messages":[{"content":"base prompt [load-request:r-1]","role":"user"}],"model":"model-a"}`))
	require.NoError(t, os.WriteFile(payloads, []byte("escrow_id,inference_id,epoch_id,prompt_payload,response_payload\n4,150,1,"+prompt+",\\x00\n"), 0o644))

	got, rep, err := ReadLoadtestRequests(dir, mock, ExportDiffs{Hosts: []HostChain{chain}}, nil)
	require.NoError(t, err)
	require.Equal(t, []InferenceRef{{Escrow: "4", Inference: 150}}, got[0].Inferences())
	require.Empty(t, got[1].Executions, "the stored prompt names r-1, not r-2")
	require.Equal(t, 1, rep.NoInference)
}

func TestReadLoadtestRequestsTiesAnExecutionToAClearlyCloserStart(t *testing.T) {
	const at = int64(1_792_350_000)
	k := loadtestKey(t, "model-a", "r-1", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
	mock := []mockopenai.RequestRecord{{Phase: mockopenai.PhaseExecute, PromptKey: k, Model: "model-a", Seed: intPtr(7),
		StartUnixMs: at*1000 + 600, Status: 200, Outcome: "ok"}}
	chain := HostChain{Host: "primary", Inferences: map[InferenceRef]*InferenceFacts{
		{Escrow: "1", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at},
		// The older escrow started the same id 51 s before: still inside the 90 s window, far behind.
		{Escrow: "3", Inference: 7}: {Started: true, Model: "model-a", StartedAt: at - 51},
	}}
	dir := writeLoadtestRun(t, []map[string]any{{"request_id": "r-1", "status": 200, "outcome": "completed"}})
	got, rep, err := ReadLoadtestRequests(dir, mock, ExportDiffs{Hosts: []HostChain{chain}}, nil)
	require.NoError(t, err)
	require.Equal(t, 0, rep.AmbiguousExec)
	require.Equal(t, 1, rep.LinkedNearest)
	require.Len(t, got[0].Executions, 1)
	require.Equal(t, InferenceRef{Escrow: "1", Inference: 7}, got[0].Executions[0].InferenceRef)
}

func TestPromiseAttributionJudgesAnInferenceOnce(t *testing.T) {
	timedOut := &InferenceFacts{Started: true, TimedOut: true}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("c", map[uint64]*InferenceFacts{1: timedOut}, nil),
			hostChain("a", map[uint64]*InferenceFacts{1: timedOut}, nil),
			hostChain("b", map[uint64]*InferenceFacts{1: {Started: true, Finished: true}}, nil),
		}},
		Requests: []ClientRequest{served(1, 200)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 64, "ok", "")},
	}
	rep := CheckPromises(in)
	got := violations(rep, ChargeOnlyTheGuilty)
	require.Len(t, got, 1)
	require.Equal(t, "a,c", got[0].Host)
	require.Contains(t, got[0].Detail, "a timeout (miss) on 2 of 3 hosts")
	require.Equal(t, 1, rep.Checked[ChargeOnlyTheGuilty].Judged)
}

func TestPromiseAttributionTakesAClientGoneAnswerAsNotFull(t *testing.T) {
	in := PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, TimedOut: true}}, nil)}},
		Requests: []ClientRequest{served(1, 200)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 10, mockopenai.OutcomeClientGone, "")},
	}
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, ChargeOnlyTheGuilty), "the executor did not get the whole answer from the ML")
	require.Equal(t, 1, rep.Checked[ChargeOnlyTheGuilty].Judged)
}

// writeCheckedRun writes a load-test run as the runner leaves it: run.yaml, requests.jsonl,
// compose.log with the executor's terminal line, and an export holding one host's diffs (a Start
// asking for 256 tokens and a Finish claiming finished) and, when mock is set, the mock log.
func writeCheckedRun(t *testing.T, finished uint64, mock *mockopenai.RequestRecord) string {
	t.Helper()
	const at = int64(1_792_350_000)
	dir := writeLoadtestRun(t, []map[string]any{{"request_id": "r-1", "status": 200, "outcome": "completed"}})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "compose.log"), []byte(
		`versiond-3-1         | {"msg":"devshard request terminal","escrow_id":"4","stage":"terminal","terminal":"finish_published","inference_id":1,"nonce":1}`+"\n"), 0o644))
	diff := func(nonce uint64, txs ...*types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "4", Txs: txs})
		require.NoError(t, err)
		return `\x` + hex.EncodeToString(raw)
	}
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: 1, Model: "model-a", MaxTokens: 256, StartedAt: at}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: 1, OutputTokens: finished}}}
	diffs := filepath.Join(dir, "export", "db-postgres-versiond-3", "postgres", "public.devshard_diffs.csv")
	require.NoError(t, os.MkdirAll(filepath.Dir(diffs), 0o755))
	require.NoError(t, os.WriteFile(diffs, []byte("escrow_id,nonce,txs_proto,post_state_root\n"+
		"4,1,"+diff(1, start)+",\\xaa\n4,2,"+diff(2, finish)+",\\xbb\n"), 0o644))
	if mock != nil {
		rec := *mock
		rec.PromptKey = loadtestKey(t, "model-a", "r-1", mockopenai.ChatMessage{Role: "user", Content: "base prompt"})
		rec.StartUnixMs = at*1000 + 300
		raw, err := json.Marshal(map[string]any{"complete": true, "dropped": 0, "records": []mockopenai.RequestRecord{rec}})
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "mock-log.json"), raw, 0o644))
	}
	return dir
}

// Test flow:
//  1. A run's mock node produced 64 tokens and reported usage 64; the executor's Finish claims 199.
//  2. Check the run.
//  3. Check the claim is a violation naming the host chain and the executor, and the same run with
//     a Finish of 64 holds.
func TestCheckLoadtestRunCatchesAFinishAboveWhatTheNodeProduced(t *testing.T) {
	produced := &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a", Node: "mock-openai-3",
		Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64}

	rep, err := CheckLoadtestRun(writeCheckedRun(t, 199, produced), nil)
	require.NoError(t, err)
	got := violations(rep, PaidOutputWithinProduced)
	require.Len(t, got, 1, "%+v", rep)
	require.Equal(t, "postgres-versiond-3", got[0].Host)
	require.Equal(t, InferenceRef{Escrow: "4", Inference: 1}, *got[0].Inference)
	require.Contains(t, got[0].Detail, "Finish output_tokens 199, the ML produced [64]")
	require.Equal(t, 1, rep.Unknown())
	require.Contains(t, rep.Checked[PaidOutputWithinProduced].Note, "1 matched to an ML execution")

	rep, err = CheckLoadtestRun(writeCheckedRun(t, 64, produced), nil)
	require.NoError(t, err)
	require.Empty(t, rep.Violations)
	require.True(t, rep.Checked[PaidOutputWithinProduced].Measured)
}

// Test flow:
//  1. A run's executor timed out though its mock node answered in full; the known list names it.
//  2. Check the run with and without the known list.
//  3. Check the charge is a violation naming the executor host, and labelled when known.
func TestCheckLoadtestRunCatchesAChargeOfAnHonestExecutor(t *testing.T) {
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	timeout := &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{InferenceId: 1}}}
	raw, err := proto.Marshal(&types.DiffContent{Nonce: 3, EscrowId: "4", Txs: []*types.DevshardTx{timeout}})
	require.NoError(t, err)
	diffs := filepath.Join(dir, "export", "db-postgres-versiond-3", "postgres", "public.devshard_diffs.csv")
	f, err := os.OpenFile(diffs, os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("4,3,\\x" + hex.EncodeToString(raw) + ",\\xcc\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rep, err := CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	got := violations(rep, ChargeOnlyTheGuilty)
	require.Len(t, got, 1, "%+v", rep)
	require.Contains(t, got[0].Detail, "executor versiond-3 (mock-openai-3) charged a timeout (miss)")

	rep, err = CheckLoadtestRun(dir, []KnownViolation{{Promise: ChargeOnlyTheGuilty, Ref: "tracked-defect"}})
	require.NoError(t, err)
	require.Equal(t, "tracked-defect", violations(rep, ChargeOnlyTheGuilty)[0].Known)
	require.Zero(t, rep.Unknown())
}

// Test flow:
//  1. A run's export has no mock log.
//  2. Check the run.
//  3. Check the attribution is not measured and the report says the output tokens were judged
//     against max_tokens alone.
func TestCheckLoadtestRunWithoutTheMockLogSaysWhatItCouldNotJudge(t *testing.T) {
	rep, err := CheckLoadtestRun(writeCheckedRun(t, 199, nil), nil)
	require.NoError(t, err)
	require.Empty(t, rep.Violations)
	require.False(t, rep.Checked[ChargeOnlyTheGuilty].Measured)
	require.Contains(t, rep.Observations, "the export has no mock-log.json: output tokens are judged against max_tokens only")
}

// Test flow:
// 1. A run's generator dropped requests at max_in_flight.
// 2. Check the run.
// 3. Check the report says the run is not valid and why, and the Markdown opens with it.
func TestCheckLoadtestRunDeclaresARunThatDroppedRequestsNotValid(t *testing.T) {
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	f, err := os.OpenFile(filepath.Join(dir, "requests.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(`{"request_id":"r-2","outcome":"dropped_by_generator","error":"max_in_flight reached"}` + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())

	rep, err := CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"the generator dropped 1 of 2 requests at max_in_flight: the load is not the one the scenario asked for"}, rep.Invalid)
	var b strings.Builder
	require.NoError(t, WritePromiseReport(&b, "System promise checks: run", rep))
	lines := strings.Split(b.String(), "\n")
	require.Equal(t, "**Run not valid:** the generator dropped 1 of 2 requests at max_in_flight: the load is not the one the scenario asked for. The checks below do not judge the system.", lines[2])
}

// Test flow:
//  1. A run's export manifest is not whole: once for a table the checks do not read (a
//     payload_storage dump over the file bound), once for a truncated devshard_diffs; then a whole
//     manifest with a mock log that is incomplete and holds no record.
//  2. Check each run.
//  3. Check only a gap in what the checks read makes the run not valid; the other is an observation.
func TestCheckLoadtestRunDeclaresARunNotValidWhenTheExportLostWhatTheChecksRead(t *testing.T) {
	produced := &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64}
	withManifest := func(m ExportManifest) string {
		dir := writeCheckedRun(t, 64, produced)
		raw, err := json.Marshal(m)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "export", exportManifestName), raw, 0o644))
		return dir
	}

	payload := "postgres public.payload_storage: dump is 1300000000 bytes, over the 536870912 byte bound; not written"
	rep, err := CheckLoadtestRun(withManifest(ExportManifest{Problems: []string{payload}}), nil)
	require.NoError(t, err)
	require.Empty(t, rep.Invalid, "the checks do not read payload_storage")
	require.Contains(t, rep.Observations, "the export is not whole outside what the checks read: "+payload)

	diffs := "db-postgres-versiond-3/postgres/public.devshard_diffs.csv"
	rep, err = CheckLoadtestRun(withManifest(ExportManifest{Files: []ExportedFile{{Path: diffs, Truncated: true}}}), nil)
	require.NoError(t, err)
	require.Equal(t, []string{"the export lost what the checks read: " + diffs + " truncated"}, rep.Invalid)

	dir := withManifest(ExportManifest{Complete: true})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "mock-log.json"), []byte(`{"complete":false,"dropped":0,"records":[]}`), 0o644))
	rep, err = CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"the mock log is incomplete and holds no record: no execution can be judged"}, rep.Invalid)
}

func writeMetrics(t *testing.T, dir string, lines ...string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metrics.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644))
}

// Test flow:
//  1. metrics.jsonl holds gateway scrapes of two processes (a restart between them), an escrow line
//     and a host scrape.
//  2. Read the gateway heap.
//  3. Check only the gateway's samples are read, each process kept apart, and the heap check grows
//     within the first process, not peak minus the restarted heap.
func TestReadLoadtestGatewayHeapKeepsProcessesApart(t *testing.T) {
	line := func(at string, heap, start float64) string {
		return fmt.Sprintf(`{"at":%q,"phase":"workload","target":"gateway","values":{"go_memstats_heap_inuse_bytes":%v,"process_start_time_seconds":%v}}`, at, heap, start)
	}
	dir := t.TempDir()
	writeMetrics(t, dir,
		`{"at":"2026-10-18T19:03:43Z","phase":"baseline","target":"escrow","escrow_id":"1","state":{"diffs":0}}`,
		line("2026-10-18T19:03:44Z", 1000, 1792350207.33),
		line("2026-10-18T19:04:44Z", 5000, 1792350207.33),
		line("2026-10-18T19:05:44Z", 800, 1792350400),
		line("2026-10-18T19:06:44Z", 2800, 1792350400),
		`{"at":"2026-10-18T19:06:45Z","phase":"workload","target":"versiond-0","values":{"go_memstats_heap_inuse_bytes":99999999}}`,
	)
	samples, err := ReadLoadtestGatewayHeap(dir)
	require.NoError(t, err)
	require.Len(t, samples, 4)
	require.Equal(t, uint64(5000), samples[1].HeapInuse)
	require.NotEqual(t, samples[0].Process, samples[2].Process)
	require.Equal(t, time.Unix(1792350224, 0).UnixMilli(), samples[0].AtUnixMs)

	in := PromiseInput{Requests: []ClientRequest{served(1, 200), served(2, 200)}, GatewayHeap: samples}
	rep := CheckPromises(in)
	require.False(t, rep.Checked[GatewayHeapBounded].Measured, "no k: no verdict")
	require.Contains(t, rep.Checked[GatewayHeapBounded].Note, "first 1000 B, peak 5000 B, 2000 B per request over 2 requests",
		"growth is 4000 B within the first process, not peak minus the restarted heap")
	require.Contains(t, rep.Checked[GatewayHeapBounded].Note, "one of 2 gateway processes")
}

// Test flow:
//  1. metrics.jsonl holds the gateway's runtime metrics through the runner's phases: baseline, two
//     workload scrapes, drain and the final snapshot.
//  2. Read the gateway heap and check the live heap against an allowance.
//  3. Check the last reading of each phase is named, the live heap is within the allowance, and a
//     restart before the last reading is not judged.
func TestReadLoadtestGatewayHeapNamesTheLiveHeapReadings(t *testing.T) {
	line := func(at, phase string, nextGC, numGC, start float64) string {
		return fmt.Sprintf(`{"at":%q,"phase":%q,"target":"gateway","values":{"go_memstats_heap_inuse_bytes":1,"go_memstats_next_gc_bytes":%v,"go_gc_duration_seconds_count":%v,"go_memstats_stack_inuse_bytes":200000,"process_start_time_seconds":%v}}`, at, phase, nextGC, numGC, start)
	}
	dir := t.TempDir()
	writeMetrics(t, dir,
		line("2026-10-18T19:03:43Z", "baseline", 2_200_000, 4, 1792350207),
		line("2026-10-18T19:04:43Z", "workload", 12_000_000, 9, 1792350207),
		line("2026-10-18T19:05:43Z", "workload", 12_000_000, 10, 1792350207),
		line("2026-10-18T19:06:43Z", "drain", 3_000_000, 11, 1792350207),
		line("2026-10-18T19:07:43Z", "final", 2_600_000, 12, 1792350207),
	)
	samples, err := ReadLoadtestGatewayHeap(dir)
	require.NoError(t, err)
	var phases []string
	for _, s := range samples {
		phases = append(phases, s.Phase)
	}
	require.Equal(t, []string{"before", "", "load_end", "", "after"}, phases, "the last reading of each runner phase; the final snapshot closes the drain")
	require.Equal(t, uint64(1_000_000), samples[0].LiveHeap())
	require.Equal(t, uint64(10), samples[2].NumGC)

	in := PromiseInput{Requests: []ClientRequest{served(1, 200)}, GatewayHeap: samples, LiveHeapAllowance: 200_000}
	require.Empty(t, violations(CheckPromises(in), GatewayLiveHeapReturns), "1200000 B after the drain against 1000000 B before plus 200000 B")
	require.True(t, CheckPromises(in).Checked[GatewayLiveHeapReturns].Measured)

	samples[4].Process = "restarted"
	in.GatewayHeap = samples
	require.Contains(t, CheckPromises(in).Checked[GatewayLiveHeapReturns].Note, "the gateway restarted", "a new process starts a new heap")
}

// Test flow:
//  1. A run as the runner leaves it, plus a mock-dapi lock never released, a gateway quarantine of a
//     host the runner did not pause, and the gateway's heap growing past its allowance after the drain.
//  2. Check the run with the allowances set.
//  3. Check each of those reaches the report from the run directory alone, and the same run checked
//     without allowances judges the live heap without a verdict.
func TestCheckLoadtestRunWithinReadsTheRunLayers(t *testing.T) {
	const participant = "gonka1qzwyhs83g7rwpwhfveajtszgayp0c95sck5r4l"
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	f, err := os.OpenFile(filepath.Join(dir, "compose.log"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString(strings.Join([]string{
		`mock-dapi-1  | {"time":"2026-10-19T07:58:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"a","node_id":"n4","request_id":"req-1"}`,
		`mock-dapi-1  | {"time":"2026-10-19T08:05:00Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"b","node_id":"n4","request_id":"req-2"}`,
		`mock-dapi-1  | {"time":"2026-10-19T08:05:01Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"b","node_id":"n4"}`,
		`versiond-3-1  | {"time":"2026-10-19T07:50:00Z","level":"INFO","msg":"devshardd: validation lease identity","instance_address":"` + participant + `"}`,
		`devshardctl-1 | {"time":"2026-10-19T07:59:18Z","level":"INFO","msg":"participant_limit_stalled_winner_quarantine participant_key=` + participant + `"}`,
	}, "\n") + "\n")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	writeMetrics(t, dir,
		`{"at":"2026-10-19T07:55:00Z","phase":"baseline","target":"gateway","values":{"go_memstats_heap_inuse_bytes":1,"go_memstats_next_gc_bytes":2000000,"go_gc_duration_seconds_count":4}}`,
		`{"at":"2026-10-19T08:00:00Z","phase":"workload","target":"gateway","values":{"go_memstats_heap_inuse_bytes":1,"go_memstats_next_gc_bytes":12000000,"go_gc_duration_seconds_count":10}}`,
		`{"at":"2026-10-19T08:06:00Z","phase":"final","target":"gateway","values":{"go_memstats_heap_inuse_bytes":1,"go_memstats_next_gc_bytes":3000000,"go_gc_duration_seconds_count":12}}`,
	)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "host-faults.json"), []byte(`[]`), 0o644))

	rep, err := CheckLoadtestRunWithin(dir, PromiseInput{LockGrace: time.Minute, LiveHeapAllowance: 100_000})
	require.NoError(t, err)
	require.Len(t, violations(rep, NodeLocksReleased), 1, "%+v", rep)
	require.Len(t, violations(rep, QuarantineOnlyWhenFaulted), 1)
	require.Contains(t, violations(rep, QuarantineOnlyWhenFaulted)[0].Detail, "shadow quarantine (stalled_winner_quarantine)")
	require.Len(t, violations(rep, GatewayLiveHeapReturns), 1, "500000 B kept against 100000 B allowed")
	require.Contains(t, strings.Join(rep.Observations, "\n"), "escrow balances not replayed", "the export holds no session to replay from")

	rep, err = CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.False(t, rep.Checked[GatewayLiveHeapReturns].Measured, "no allowance, no verdict")
	require.Empty(t, violations(rep, GatewayLiveHeapReturns))
}

// Test flow:
//  1. A run whose scenario kept only the last 5000 lines of each container's log, with a lock
//     journal and a gateway line in compose.log; the same run with the scenario's log tail left out
//     (the runner's default is 5000 lines).
//  2. Check each run.
//  3. Check the checks that read the log layers do not judge a log cut to its tail.
func TestCheckLoadtestRunDoesNotJudgeTheLayersOfACutComposeLog(t *testing.T) {
	for _, runYAML := range []string{
		strings.Replace(loadtestRunYAMLFixture, "tail: all", `tail: "5000"`, 1),
		strings.Replace(loadtestRunYAMLFixture, "  logs:\n    tail: all\n", "", 1),
	} {
		dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
			Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
		require.NotEqual(t, loadtestRunYAMLFixture, runYAML)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "run.yaml"), []byte(runYAML), 0o644))
		f, err := os.OpenFile(filepath.Join(dir, "compose.log"), os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.WriteString(strings.Join([]string{
			`mock-dapi-1  | {"time":"2026-10-19T08:00:01Z","msg":"mlnode_acquire","outcome":"acquired","lock_id":"b","node_id":"n4","request_id":"req-2"}`,
			`mock-dapi-1  | {"time":"2026-10-19T08:00:02Z","msg":"mlnode_release","outcome":"SUCCESS","lock_id":"b","node_id":"n4"}`,
			`devshardctl-1 | {"time":"2026-10-19T08:00:03Z","level":"INFO","msg":"gateway_ready","host":"abcdefgh"}`,
		}, "\n") + "\n")
		require.NoError(t, err)
		require.NoError(t, f.Close())
		rep, err := CheckLoadtestRunWithin(dir, PromiseInput{BurnAllowance: 1})
		require.NoError(t, err)
		for _, name := range []string{NodeLocksReleased, QuarantineOnlyWhenFaulted, OneNoncePerRequest} {
			require.False(t, rep.Checked[name].Measured, "%s judged on a compose.log cut to its tail: %s", name, rep.Checked[name].Note)
			require.Contains(t, rep.Checked[name].Note, "compose.log keeps only the last 5000 lines")
		}
	}
}

// Test flow:
//  1. A run whose metrics.jsonl ends in a line the collector was still writing.
//  2. Check the run.
//  3. Check the torn line is left out and the report is written.
func TestCheckLoadtestRunReadsAMetricsFileCutMidLine(t *testing.T) {
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "metrics.jsonl"), []byte(
		`{"at":"2026-10-19T07:55:00Z","phase":"baseline","target":"gateway","values":{"go_memstats_heap_inuse_bytes":1}}`+"\n"+
			`{"at":"2026-10-19T08:06:00Z","phase":"drain","target":"gateway","values":{"go_memstats_heap_in`), 0o644))
	rep, err := CheckLoadtestRun(dir, nil)
	require.NoError(t, err, "a torn last line must not fail the whole report")
	require.Empty(t, violations(rep, PaidOutputWithinProduced))
	samples, err := ReadLoadtestGatewayHeap(dir)
	require.NoError(t, err)
	require.Len(t, samples, 1)
}

// Test flow:
//  1. A run whose client log records the bytes of each answer, and whose gateway heap grew 400000 B
//     over the load.
//  2. Check it with a k of 2 per response byte.
//  3. Check gateway_heap_bounded gives a verdict from the run directory alone.
func TestCheckLoadtestRunWithinJudgesTheHeapCeilingWhenAskedTo(t *testing.T) {
	dir := writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	require.NoError(t, os.WriteFile(filepath.Join(dir, "requests.jsonl"),
		[]byte(`{"request_id":"r-1","status":200,"outcome":"completed","response_bytes":1000}`+"\n"), 0o644))
	writeMetrics(t, dir,
		`{"at":"2026-10-19T07:55:00Z","phase":"workload","target":"gateway","values":{"go_memstats_heap_inuse_bytes":1000}}`,
		`{"at":"2026-10-19T08:00:00Z","phase":"workload","target":"gateway","values":{"go_memstats_heap_inuse_bytes":401000}}`)
	rep, err := CheckLoadtestRunWithin(dir, PromiseInput{HeapPerResponseByte: 2})
	require.NoError(t, err)
	require.True(t, rep.Checked[GatewayHeapBounded].Measured, rep.Checked[GatewayHeapBounded].Note)
	require.Len(t, violations(rep, GatewayHeapBounded), 1, "400000 B per request against 2 x 1000 B of answer")
}

// Test flow:
//  1. metrics.jsonl holds the gateway's scrapes of one process whose start time moved with a clock
//     sync; the pid and the start ticks stay.
//  2. Read the gateway heap and check the live heap.
//  3. Check the readings are one process, so the live heap is judged.
func TestReadLoadtestGatewayHeapKeepsOneProcessAcrossAClockShift(t *testing.T) {
	line := func(at, phase string, nextGC, numGC, start float64) string {
		return fmt.Sprintf(`{"at":%q,"phase":%q,"target":"gateway","values":{"go_memstats_heap_inuse_bytes":1,"go_memstats_next_gc_bytes":%v,"go_gc_duration_seconds_count":%v,"process_start_time_seconds":%v,"loadtest_process_pid":1,"loadtest_process_start_ticks":310605149}}`, at, phase, nextGC, numGC, start)
	}
	dir := t.TempDir()
	writeMetrics(t, dir,
		line("2026-10-18T19:03:43Z", "baseline", 2_000_000, 4, 1792350207),
		line("2026-10-18T19:05:43Z", "workload", 12_000_000, 10, 1792350207),
		line("2026-10-18T19:07:43Z", "final", 2_200_000, 12, 1792350214))
	samples, err := ReadLoadtestGatewayHeap(dir)
	require.NoError(t, err)
	rep := CheckPromises(PromiseInput{Requests: []ClientRequest{served(1, 200)}, GatewayHeap: samples, LiveHeapAllowance: 200_000})
	require.True(t, rep.Checked[GatewayLiveHeapReturns].Measured, rep.Checked[GatewayLiveHeapReturns].Note)
}

// Test flow:
//  1. A run with a gateway quarantine outside any fault window and no mock log in its export; the
//     same run with a mock log the export could not take whole.
//  2. Check each run.
//  3. Check the quarantine check does not judge them: the ML node's faults are not known.
func TestCheckLoadtestRunDoesNotJudgeQuarantinesWithoutTheMockLog(t *testing.T) {
	quarantine := `devshardctl-1 | {"time":"2026-10-19T07:59:18Z","level":"INFO","msg":"participant_limit_activated participant_key=gonka1qzwyhs83g7rwpwhfveajtszgayp0c95sck5r4l status=503 path_kind=chat"}` + "\n"
	appendLog := func(dir string) {
		f, err := os.OpenFile(filepath.Join(dir, "compose.log"), os.O_APPEND|os.O_WRONLY, 0)
		require.NoError(t, err)
		_, err = f.WriteString(quarantine)
		require.NoError(t, err)
		require.NoError(t, f.Close())
	}
	dir := writeCheckedRun(t, 64, nil)
	appendLog(dir)
	rep, err := CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.False(t, rep.Checked[QuarantineOnlyWhenFaulted].Measured, rep.Checked[QuarantineOnlyWhenFaulted].Note)
	require.Empty(t, violations(rep, QuarantineOnlyWhenFaulted))

	dir = writeCheckedRun(t, 64, &mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Model: "model-a",
		Node: "mock-openai-3", Seed: intPtr(1), Status: 200, Outcome: "ok", TokensOut: 64, UsageCompletion: 64})
	appendLog(dir)
	raw, err := os.ReadFile(filepath.Join(dir, "export", "mock-log.json"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "export", "mock-log.json"), []byte(strings.Replace(string(raw), `"complete":true`, `"complete":false`, 1)), 0o644))
	rep, err = CheckLoadtestRun(dir, nil)
	require.NoError(t, err)
	require.False(t, rep.Checked[QuarantineOnlyWhenFaulted].Measured, rep.Checked[QuarantineOnlyWhenFaulted].Note)
}
