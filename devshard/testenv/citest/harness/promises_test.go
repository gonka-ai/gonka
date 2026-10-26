package harness

import (
	"database/sql"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/testenv/mockopenai"
	"devshard/types"
)

func ref(id uint64) InferenceRef { return InferenceRef{Escrow: "2", Inference: id} }

func hostChain(host string, facts map[uint64]*InferenceFacts, roots map[uint64]string) HostChain {
	c := HostChain{Host: host, Inferences: map[InferenceRef]*InferenceFacts{}, Roots: map[string]map[uint64]string{"2": {}}}
	for id, f := range facts {
		c.Inferences[ref(id)] = f
	}
	for n, r := range roots {
		c.Roots["2"][n] = r
	}
	return c
}

func intPtr(n int) *int { return &n }

func served(id uint64, status int) ClientRequest {
	return ClientRequest{RequestID: "r" + string(rune('0'+id)), PromptKey: "k" + string(rune('0'+id)), HTTPStatus: status,
		Executions: []Execution{{InferenceRef: ref(id), Participant: "p1", Instance: "versiond-2", Outcome: "ok"}}}
}

func execution(id uint64, status, tokens int, outcome, rule string) mockopenai.RequestRecord {
	return mockopenai.RequestRecord{Phase: mockopenai.PhaseExecute, Seed: intPtr(int(id)), PromptKey: "k" + string(rune('0'+id)),
		Status: status, TokensOut: tokens, Outcome: outcome, Rule: rule}
}

func violations(rep PromiseReport, promise string) []PromiseViolation {
	var out []PromiseViolation
	for _, v := range rep.Violations {
		if v.Promise == promise {
			out = append(out, v)
		}
	}
	return out
}

func TestPromiseTokens(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{
			1: {Started: true, MaxTokens: 64, Finished: true, OutputTokens: 64},
			2: {Started: true, MaxTokens: 64, Finished: true, OutputTokens: 80},
			3: {Started: true, MaxTokens: 64, Finished: true, OutputTokens: 50},
		}, nil)}},
		Requests: []ClientRequest{served(1, 200), served(2, 200), served(3, 200)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 64, "ok", ""), execution(2, 200, 80, "ok", ""), execution(3, 200, 64, "ok", "")},
	}
	got := violations(CheckPromises(in), PaidOutputWithinProduced)
	require.Len(t, got, 2)
	require.Contains(t, got[0].Detail, "above Start max_tokens", "inference 2 claims more than max_tokens")
	require.Contains(t, got[1].Detail, "the ML produced [64]", "inference 3 claims other than the ML produced")
}

func TestPromiseTokensOneKeyManyExecutions(t *testing.T) {
	in := PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{1: {Started: true, MaxTokens: 64, Finished: true, OutputTokens: 40}}, nil)}},
		Requests: []ClientRequest{served(1, 200)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 64, "ok", ""), execution(1, 200, 40, "ok", "")},
	}
	require.Empty(t, violations(CheckPromises(in), PaidOutputWithinProduced), "a repeated seed matches any of its executions")
}

func TestPromiseAttribution(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{
			1: {Started: true, TimedOut: true},                        // honest executor timed out
			2: {Started: true, TimedOut: true},                        // client spoiled the request
			3: {Started: true, TimedOut: true},                        // the mock aborted: a real fault
			4: {Started: true, Finished: true, Rejected: []uint32{3}}, // honest result voted invalid
			5: {Started: true, Finished: true},                        // not charged
		}, nil)}},
		Requests: []ClientRequest{served(1, 200), served(2, 502), served(3, 502), served(4, 200), served(5, 200)},
		Mock: []mockopenai.RequestRecord{
			execution(1, 200, 64, "ok", ""), execution(2, 400, 0, "rule_status", "bad"),
			execution(3, 0, 0, "rule_abort", "cut"), execution(4, 200, 64, "ok", ""), execution(5, 200, 64, "ok", ""),
		},
	}
	rep := CheckPromises(in)
	got := violations(rep, ChargeOnlyTheGuilty)
	require.Len(t, got, 3)
	require.Equal(t, uint64(1), got[0].Inference.Inference)
	require.Contains(t, got[0].Detail, "answered it in full")
	require.Equal(t, uint64(2), got[1].Inference.Inference)
	require.Contains(t, got[1].Detail, "client error")
	require.Equal(t, uint64(4), got[2].Inference.Inference)
	require.Contains(t, got[2].Detail, "invalid vote")
	require.Equal(t, 4, rep.Checked[ChargeOnlyTheGuilty].Judged)
}

func TestPromiseAttributionDishonestSide(t *testing.T) {
	wrong := func(id uint64) mockopenai.RequestRecord {
		m := execution(id, 200, 64, "ok", "")
		m.Chaos = mockopenai.ChaosWrongAnswer
		return m
	}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{
			1: {Started: true, Finished: true, Rejected: []uint32{1, 2, 3}},                  // forged, caught: no violation
			2: {Started: true, Finished: true, Accepted: []uint32{4}},                        // forged, accepted
			3: {Started: true, Finished: true},                                               // forged, never validated
			4: {Started: true, Finished: true, Rejected: []uint32{1}, Accepted: []uint32{2}}, // honest, one invalid vote
		}, nil)}},
		Requests: []ClientRequest{served(1, 200), served(2, 200), served(3, 200), served(4, 200)},
		Mock:     []mockopenai.RequestRecord{wrong(1), wrong(2), wrong(3), execution(4, 200, 64, "ok", "")},
	}
	rep := CheckPromises(in)
	got := violations(rep, ChargeOnlyTheGuilty)
	require.Len(t, got, 2)
	require.Equal(t, uint64(2), got[0].Inference.Inference)
	require.Contains(t, got[0].Detail, "validator slots [4] accepted an answer the ML sent with forged logprobs")
	require.Equal(t, uint64(4), got[1].Inference.Inference)
	require.Contains(t, got[1].Detail, "answered it in full", "a forged answer is not an honest one, an untouched one is")
	require.Contains(t, rep.Checked[ChargeOnlyTheGuilty].Note, "2 forged answers validated, 1 of them voted invalid")
}

func TestPromiseKnownViolationsAreLabelled(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{hostChain("a", map[uint64]*InferenceFacts{
			1: {Started: true, MaxTokens: 64, Finished: true, OutputTokens: 80},
		}, nil)}},
		Known: []KnownViolation{{Promise: PaidOutputWithinProduced, Detail: regexp.MustCompile(`above Start max_tokens`), Ref: "tracked-defect"}},
	}
	got := violations(CheckPromises(in), PaidOutputWithinProduced)
	require.Len(t, got, 1)
	require.Equal(t, "tracked-defect", got[0].Known)
}

func TestPromiseNothingMeasuredOnEmptyInput(t *testing.T) {
	rep := CheckPromises(PromiseInput{})
	require.Empty(t, rep.Violations)
	for name, m := range rep.Checked {
		require.False(t, m.Measured, name)
		require.True(t, strings.HasPrefix(m.Note, "not measured: "), name)
	}
	names := make([]string, 0, len(rep.Checked))
	for name := range rep.Checked {
		names = append(names, name)
	}
	require.ElementsMatch(t, []string{PaidOutputWithinProduced, ChargeOnlyTheGuilty}, names,
		"every check reports, measured or not")
}

func TestReadExportDiffsKeepsHostsApart(t *testing.T) {
	dir := t.TempDir()
	diff := func(nonce uint64, txs ...*types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "2", Txs: txs})
		require.NoError(t, err)
		return `\x` + hex.EncodeToString(raw)
	}
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1, MaxTokens: 64}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, OutputTokens: 64}}}
	reject := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: 3}}}
	write := func(rel string, rows ...string) {
		path := filepath.Join(dir, rel)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		body := "epoch_id,escrow_id,nonce,txs_proto,user_sig,post_state_root,state_hash,warm_keys_json,created_at\n" + strings.Join(rows, "\n") + "\n"
		require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	}
	write("postgres/public.devshard_diffs.csv", "1,2,1,"+diff(1, start)+",,\\xaa,,,0")
	write("db-devshardd_versiond_2/postgres/public.devshard_diffs.csv",
		"1,2,1,"+diff(1, start)+",,\\xaa,,,0", "1,2,2,"+diff(2, finish, reject)+",,\\xbb,,,0")

	got, err := ReadExportDiffs(dir)
	require.NoError(t, err)
	require.Len(t, got.Hosts, 2)
	byHost := map[string]HostChain{}
	for _, c := range got.Hosts {
		byHost[c.Host] = c
	}
	require.False(t, byHost["primary"].Inferences[ref(1)].Finished)
	f := byHost["devshardd_versiond_2"].Inferences[ref(1)]
	require.True(t, f.Finished)
	require.Equal(t, uint64(64), f.MaxTokens)
	require.Equal(t, []uint32{3}, f.Rejected)
	require.Equal(t, "bb", byHost["devshardd_versiond_2"].Roots["2"][2])
}

// Test flow:
//  1. One Finish above its Start's max_tokens is replayed on three hosts.
//  2. Check the run.
//  3. Check it is one violation naming the three hosts.
func TestPromiseTokensJudgesAFinishAboveMaxTokensOncePerInference(t *testing.T) {
	f := func() *InferenceFacts {
		return &InferenceFacts{Started: true, MaxTokens: 64, Finished: true, OutputTokens: 199}
	}
	in := PromiseInput{Diffs: ExportDiffs{Hosts: []HostChain{
		hostChain("a", map[uint64]*InferenceFacts{1: f()}, nil),
		hostChain("b", map[uint64]*InferenceFacts{1: f()}, nil),
		hostChain("c", map[uint64]*InferenceFacts{1: f()}, nil),
	}}}
	got := violations(CheckPromises(in), PaidOutputWithinProduced)
	require.Len(t, got, 1, "one Finish replayed on three hosts is one overclaim")
	require.Equal(t, "a,b,c", got[0].Host)
}

// challengeGroup is the escrow group of writeChallengedHost: the executor on slot 0, validator v1
// on slots 1 and 3, v2 on slot 2.
var challengeGroup = []types.SlotAssignment{
	{SlotID: 0, ValidatorAddress: "exec"}, {SlotID: 1, ValidatorAddress: "v1"},
	{SlotID: 2, ValidatorAddress: "v2"}, {SlotID: 3, ValidatorAddress: "v1"},
}

// writeChallengedHost writes one host's export on the storage its versiond keeps (postgres or
// sqlite): the escrow's session row, with the vote threshold and group, and the diffs. A host on
// SQLite sits next to a Postgres host with no diffs, as a hybrid host does in a run.
func writeChallengedHost(t *testing.T, storage string, threshold uint32, diffs ...[]*types.DevshardTx) string {
	t.Helper()
	dir := t.TempDir()
	cfg, err := json.Marshal(types.SessionConfig{VoteThreshold: threshold})
	require.NoError(t, err)
	group, err := json.Marshal(challengeGroup)
	require.NoError(t, err)
	blobs := make([][]byte, len(diffs))
	for i, txs := range diffs {
		blobs[i], err = proto.Marshal(&types.DiffContent{Nonce: uint64(i + 1), EscrowId: "2", Txs: txs})
		require.NoError(t, err)
	}
	if storage == "sqlite" {
		path := filepath.Join(dir, "sqlite", "versiond-0", "opt", "versiond", "data", "a.db")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		db, err := sql.Open("sqlite", "file:"+path)
		require.NoError(t, err)
		defer db.Close()
		_, err = db.Exec(`CREATE TABLE sessions (escrow_id TEXT, config_json TEXT, group_json TEXT, status TEXT);
			CREATE TABLE diffs (escrow_id TEXT, nonce INTEGER, txs_proto BLOB, post_state_root BLOB)`)
		require.NoError(t, err)
		_, err = db.Exec(`INSERT INTO sessions VALUES ('2', ?, ?, 'active')`, string(cfg), string(group))
		require.NoError(t, err)
		for i, b := range blobs {
			_, err = db.Exec(`INSERT INTO diffs VALUES ('2', ?, ?, x'aa')`, i+1, b)
			require.NoError(t, err)
		}
		pg := filepath.Join(dir, "db-postgres-versiond-1", "postgres", "public.devshard_diffs.csv")
		require.NoError(t, os.MkdirAll(filepath.Dir(pg), 0o755))
		require.NoError(t, os.WriteFile(pg, []byte("escrow_id,nonce,txs_proto,post_state_root\n"), 0o644))
		return dir
	}
	base := filepath.Join(dir, "db-postgres-versiond-0", "postgres")
	require.NoError(t, os.MkdirAll(base, 0o755))
	var sessions strings.Builder
	require.NoError(t, csv.NewWriter(&sessions).WriteAll([][]string{
		{"epoch_id", "escrow_id", "config_json", "group_json", "status"},
		{"1", "2", string(cfg), string(group), "active"},
	}))
	require.NoError(t, os.WriteFile(filepath.Join(base, "public.devshard_sessions.csv"), []byte(sessions.String()), 0o644))
	rows := []string{"escrow_id,nonce,txs_proto,post_state_root"}
	for i, b := range blobs {
		rows = append(rows, fmt.Sprintf("2,%d,\\x%s,\\xaa", i+1, hex.EncodeToString(b)))
	}
	require.NoError(t, os.WriteFile(filepath.Join(base, "public.devshard_diffs.csv"), []byte(strings.Join(rows, "\n")+"\n"), 0o644))
	return dir
}

// Test flow:
//  1. A host's chain holds an honest execution, its Finish and a MsgChallenge in which v1 (two
//     slots) and v2 (one slot) found a fault: fault weight 3. The session row is on Postgres or on
//     SQLite.
//  2. Check the run with the session's vote threshold at 3, then at 2; then with a first challenge
//     that found no fault ahead of the faulting one.
//  3. Check a weight not above the threshold charges nothing, a weight above it charges the honest
//     executor with the invalidation, and a challenge after the first charges nothing (the state
//     machine applies only the first).
func TestPromiseAttributionJudgesAChallengeByItsThreshold(t *testing.T) {
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1, MaxTokens: 64}}}
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, OutputTokens: 64}}}
	challenge := func(fault types.ChallengeFault) *types.DevshardTx {
		return &types.DevshardTx{Tx: &types.DevshardTx_Challenge{Challenge: &types.MsgChallenge{InferenceId: 1, Votes: []*types.ChallengeVote{
			{VoterSlot: 1, Fault: fault}, {VoterSlot: 2, Fault: fault},
		}}}}
	}
	faulted := challenge(types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE)
	for _, storage := range []string{"postgres", "sqlite"} {
		t.Run(storage, func(t *testing.T) {
			check := func(threshold uint32, challenges ...*types.DevshardTx) []PromiseViolation {
				diffs := [][]*types.DevshardTx{{start}, {finish}}
				for _, c := range challenges {
					diffs = append(diffs, []*types.DevshardTx{c})
				}
				export, err := ReadExportDiffs(writeChallengedHost(t, storage, threshold, diffs...))
				require.NoError(t, err)
				return violations(CheckPromises(PromiseInput{Diffs: export, Requests: []ClientRequest{served(1, 200)},
					Mock: []mockopenai.RequestRecord{execution(1, 200, 64, "ok", "")}}), ChargeOnlyTheGuilty)
			}
			require.Empty(t, check(3, faulted), "fault weight 3 is not above threshold 3: the Finish stays paid")
			got := check(2, faulted)
			require.Len(t, got, 1, "fault weight 3 above threshold 2 invalidates the honest Finish")
			require.Equal(t, uint64(1), got[0].Inference.Inference)
			require.Contains(t, got[0].Detail, "an invalidation by a usage challenge (fault weight 3 above vote threshold 2, slots [1 2])")
			require.Empty(t, check(2, challenge(types.ChallengeFault_CHALLENGE_FAULT_NONE), faulted),
				"a challenge after the first is a no-op in the state machine")
		})
	}
}
