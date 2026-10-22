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
	"time"

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
	chain := hostChain("a", map[uint64]*InferenceFacts{
		1: {Started: true, TimedOut: true}, // honest executor timed out
		2: {Started: true, TimedOut: true}, // client spoiled the request
		3: {Started: true, TimedOut: true}, // the mock aborted: a real fault
		4: {Started: true, Finished: true, Rejected: []uint32{3, 1}, // honest result invalidated
			Votes: []ValidationVote{{Slot: 3}, {Slot: 1, PhaseB: true}}},
		5: {Started: true, Finished: true}, // not charged
	}, nil)
	chain.Voting = map[string]SessionVoting{"2": fiveVoters(t, 1)}
	in := PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{chain}},
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
	require.Contains(t, got[2].Detail, "an invalidation by the validators")
	require.Equal(t, 4, rep.Checked[ChargeOnlyTheGuilty].Judged)
}

func TestPromiseAttributionDishonestSide(t *testing.T) {
	wrong := func(id uint64) mockopenai.RequestRecord {
		m := execution(id, 200, 64, "ok", "")
		m.Chaos = mockopenai.ChaosWrongAnswer
		return m
	}
	chain := hostChain("a", map[uint64]*InferenceFacts{
		1: {Started: true, Finished: true, Rejected: []uint32{1, 2, 3}}, // forged, caught: no violation
		2: {Started: true, Finished: true, Accepted: []uint32{4}},       // forged, accepted
		3: {Started: true, Finished: true},                              // forged, never validated
		4: {Started: true, Finished: true, Rejected: []uint32{1, 2}, // honest, invalidated
			Votes: []ValidationVote{{Slot: 1}, {Slot: 2, PhaseB: true}}},
	}, nil)
	chain.Voting = map[string]SessionVoting{"2": fiveVoters(t, 1)}
	in := PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{chain}},
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

func TestPromiseClientErrorGets4xx(t *testing.T) {
	in := PromiseInput{
		Requests: []ClientRequest{served(1, 502), served(2, 400), served(3, 502)},
		Mock: []mockopenai.RequestRecord{
			execution(1, 400, 0, "rule_status", "bad"), execution(1, 400, 0, "rule_status", "bad"),
			execution(2, 400, 0, "rule_status", "bad"),
			execution(3, 400, 0, "rule_status", "bad"), execution(3, 503, 0, "rule_status", "busy"),
		},
		MockErrorsVLLMShaped: true,
	}
	rep := CheckPromises(in)
	got := violations(rep, ClientErrorAnswered4xx)
	require.Len(t, got, 1, "only request 1: request 2 got its 4xx, request 3 was not all client errors")
	require.Equal(t, served(1, 0).RequestID, got[0].RequestID)

	in.MockErrorsVLLMShaped = false
	rep = CheckPromises(in)
	require.Empty(t, violations(rep, ClientErrorAnswered4xx), "a mock error body not shaped like vLLM may itself cause the 5xx")
	require.Len(t, rep.Observations, 1)
}

func TestPromiseConvergenceWaitsForTheDeadline(t *testing.T) {
	recent := &InferenceFacts{Started: true, StartedAt: t0.Unix(), ConfirmedAt: t0.Unix(), Finished: true}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: recent}, map[uint64]string{1: "r1", 2: "r2"}),
			hostChain("b", map[uint64]*InferenceFacts{1: recent}, map[uint64]string{1: "r1"}),
		}},
		Timeouts:      map[string]EscrowTimeouts{"2": {Refusal: 60, Execution: 60}},
		ObservedUntil: t0.Add(time.Minute),
	}
	require.Empty(t, violations(CheckPromises(in), HostsConverge), "right after the load the hosts lag by turn")
	in.ObservedUntil = t0.Add(time.Hour)
	require.Len(t, violations(CheckPromises(in), HostsConverge), 1, "an hour later the lag is a defect")
}

func TestPromiseConvergence(t *testing.T) {
	in := PromiseInput{Diffs: ExportDiffs{Hosts: []HostChain{
		hostChain("a", nil, map[uint64]string{1: "r1", 2: "r2", 3: "r3"}),
		hostChain("b", nil, map[uint64]string{1: "r1", 2: "r2", 3: "r3"}),
	}}, ObservedUntil: t0}
	require.Empty(t, violations(CheckPromises(in), HostsConverge))

	in.Diffs.Hosts = append(in.Diffs.Hosts,
		hostChain("c", nil, map[uint64]string{1: "r1"}),
		hostChain("d", nil, map[uint64]string{1: "r1", 2: "x2", 3: "x3"}))
	got := violations(CheckPromises(in), HostsConverge)
	require.Len(t, got, 2)
	require.Contains(t, got[0].Detail, "c at 1")
	require.Equal(t, "d", got[1].Host)
	require.Contains(t, got[1].Detail, "from nonce 2 on", "a fork is reported once, at its first nonce")
}

func TestPromiseHeapCeiling(t *testing.T) {
	in := PromiseInput{
		Requests:    []ClientRequest{served(1, 200), served(2, 200)},
		GatewayHeap: []HeapSample{{HeapInuse: 1_000}, {HeapInuse: 401_000}},
	}
	rep := CheckPromises(in)
	require.False(t, rep.Checked[GatewayHeapBounded].Measured, "without k there is no verdict")
	in.ResponseBytes, in.HeapPerResponseByte = 2_000, 2
	require.Len(t, violations(CheckPromises(in), GatewayHeapBounded), 1, "200000 B per request against 2 x 1000 B")
	in.HeapPerResponseByte = 300
	require.Empty(t, violations(CheckPromises(in), GatewayHeapBounded))
}

func TestPromiseLiveHeapReturnsAfterTheDrain(t *testing.T) {
	// Live heap: before (2_000_000 - 0) / 2 = 1_000_000; after (2_600_000 - 200_000) / 2 = 1_200_000.
	heap := []HeapSample{
		{Phase: "before", HeapInuse: 1_500_000, NextGC: 2_000_000, NumGC: 4},
		{HeapInuse: 9_000_000, NextGC: 12_000_000, NumGC: 9},
		{Phase: "load_end", HeapInuse: 8_000_000, NextGC: 12_000_000, NumGC: 10},
		{Phase: "after", HeapInuse: 3_000_000, NextGC: 2_600_000, NumGC: 12, StackInuse: 200_000},
	}
	in := PromiseInput{Requests: []ClientRequest{served(1, 200)}, GatewayHeap: heap}
	rep := CheckPromises(in)
	require.False(t, rep.Checked[GatewayLiveHeapReturns].Measured, "without an allowance there is no verdict")
	require.Contains(t, rep.Checked[GatewayLiveHeapReturns].Note, "1000000 B before the load, 1200000 B after the drain (+200000 B; 2 collections")

	in.LiveHeapAllowance = 199_999
	got := violations(CheckPromises(in), GatewayLiveHeapReturns)
	require.Len(t, got, 1, "200000 B kept against 199999 B allowed")
	require.Contains(t, got[0].Detail, "1200000 B after the drain against 1000000 B before the load plus 199999 B allowed")
	in.LiveHeapAllowance = 200_000
	require.Empty(t, violations(CheckPromises(in), GatewayLiveHeapReturns))
	require.True(t, CheckPromises(in).Checked[GatewayLiveHeapReturns].Measured, "a heap_inuse peak of 9 MB in the load is no verdict")
}

func TestPromiseLiveHeapAllowsForTheInferencesOfUnsettledEscrows(t *testing.T) {
	chain := func(status map[string]string, refs ...InferenceRef) HostChain {
		inf := map[InferenceRef]*InferenceFacts{}
		for _, r := range refs {
			inf[r] = &InferenceFacts{}
		}
		return HostChain{Inferences: inf, Status: status}
	}
	in := PromiseInput{Requests: []ClientRequest{served(1, 200)}, LiveHeapAllowance: 100_000, LiveHeapPerInference: 1_000,
		GatewayHeap: []HeapSample{
			{Phase: "before", NextGC: 2_000_000, NumGC: 4},
			{Phase: "load_end", NextGC: 12_000_000, NumGC: 10},
			{Phase: "after", NextGC: 2_400_000, NumGC: 12}, // +200_000 B live
		},
		// Escrow 1 is settled everywhere; escrow 2 is active on one host up to inference 60, the other
		// host holds only up to 40: the highest nonce counts.
		Diffs: ExportDiffs{Hosts: []HostChain{
			chain(map[string]string{"1": "settled", "2": "active"}, InferenceRef{"1", 500}, InferenceRef{"2", 60}),
			chain(map[string]string{"1": "settled", "2": "settled"}, InferenceRef{"2", 40}),
		}},
	}
	rep := CheckPromises(in)
	require.Contains(t, rep.Checked[GatewayLiveHeapReturns].Note, "60 inferences on escrows not settled by the export")
	got := violations(rep, GatewayLiveHeapReturns)
	require.Len(t, got, 1, "200000 B kept against 100000 + 60 x 1000 B allowed")
	require.Contains(t, got[0].Detail, "plus 160000 B allowed (100000 B + 1000 B for each of 60 inferences on unsettled escrows)")

	in.LiveHeapPerInference = 1_667
	require.Empty(t, violations(CheckPromises(in), GatewayLiveHeapReturns), "100000 + 60 x 1667 = 200020 B")
}

func TestPromiseLiveHeapNeedsACollectionAfterTheLoad(t *testing.T) {
	in := PromiseInput{Requests: []ClientRequest{served(1, 200)}, LiveHeapAllowance: 1, GatewayHeap: []HeapSample{
		{Phase: "before", NextGC: 2_000_000, NumGC: 4},
		{Phase: "load_end", NextGC: 12_000_000, NumGC: 10},
		{Phase: "after", NextGC: 12_000_000, NumGC: 10},
	}}
	rep := CheckPromises(in)
	require.False(t, rep.Checked[GatewayLiveHeapReturns].Measured, "the goal still comes from a collection inside the load")
	require.Contains(t, rep.Checked[GatewayLiveHeapReturns].Note, "no collection between the load end and the last reading")
	require.Empty(t, rep.Violations)

	in.GatewayHeap = []HeapSample{{HeapInuse: 1}, {HeapInuse: 2}}
	require.Contains(t, CheckPromises(in).Checked[GatewayLiveHeapReturns].Note, "no gateway heap readings before the load, at its end and after the drain",
		"a run written before the phases were recorded is not judged")
}

func TestPromiseKnownClientErrorIsLabelled(t *testing.T) {
	in := PromiseInput{
		Requests:             []ClientRequest{served(1, 502)},
		Mock:                 []mockopenai.RequestRecord{execution(1, 400, 0, "rule_status", "bad")},
		MockErrorsVLLMShaped: true,
		Known:                []KnownViolation{{Promise: ClientErrorAnswered4xx, Detail: regexp.MustCompile(`client got 502`), Ref: "#1903"}},
	}
	got := violations(CheckPromises(in), ClientErrorAnswered4xx)
	require.Len(t, got, 1)
	require.Equal(t, "#1903", got[0].Known)
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
	require.ElementsMatch(t, []string{InferenceClosesInTime, ServedRequestFinished, PaidOutputWithinProduced, ChargeOnlyTheGuilty,
		EscrowMoneyConserved, GatewayHeapBounded, GatewayLiveHeapReturns, ClientErrorAnswered4xx, HostsConverge,
		NodeLocksReleased, OneNoncePerRequest, QuarantineOnlyWhenFaulted}, names,
		"every check reports, measured or not")
}

func TestReadExportDiffsKeepsHostsApart(t *testing.T) {
	dir := t.TempDir()
	diff := func(nonce uint64, txs ...*types.DevshardTx) string {
		raw, err := proto.Marshal(&types.DiffContent{Nonce: nonce, EscrowId: "2", Txs: txs})
		require.NoError(t, err)
		return `\x` + hex.EncodeToString(raw)
	}
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1, MaxTokens: 64, StartedAt: 7}}}
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

// claimedChain is host's chain of escrow 2 on the fiveVoters session at vote threshold 1, with roots
// at nonces 1 to diffs: each inference is started with max_tokens 64, finished with the output
// tokens it claims, then voted on by its txs.
func claimedChain(t *testing.T, host string, diffs uint64, claims map[uint64]uint64, votes map[uint64][]*types.DevshardTx) HostChain {
	c := hostChain(host, nil, nil)
	c.Voting = map[string]SessionVoting{"2": fiveVoters(t, 1)}
	for n := uint64(1); n <= diffs; n++ {
		c.Roots["2"][n] = fmt.Sprintf("%02x", n)
	}
	for id, out := range claims {
		c.apply("2", &types.DiffContent{Txs: append([]*types.DevshardTx{
			{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: id, InputLength: 10, MaxTokens: 64}}},
			{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: id, InputTokens: 10, OutputTokens: out}}},
		}, votes[id]...)})
	}
	return c
}

// Test flow:
//  1. Six inflated Finishes, each Start allowing 64 tokens, vote threshold 1. Inferences 1, 2, 3 and
//     6 claim 64 where the ML produced 40; 4 and 5 claim 80, above max_tokens, as the ML produced.
//     The validators invalidate 1 and 4 (two invalid votes), validate 2 and 5 after a challenge, and
//     leave 3 Finished; a usage challenge faulted by slots 1 and 2 invalidates 6. Host a holds the
//     chain to nonce 3; host b holds a prefix of it to nonce 2, where no vote and no challenge has
//     reached 1, 4 and 6 yet.
//  2. Check the run and write its report.
//  3. Check the three paid inflated Finishes (2, 3, 5) are violations naming both hosts; the three
//     invalidated ones (1, 4, 6), judged on the longest chain for the host behind it, are not
//     violations but counted and listed apart as caught, in the note, the Markdown and the JSON.
func TestPromiseTokensJudgesOnlyPaidFinishes(t *testing.T) {
	claims := map[uint64]uint64{1: 64, 2: 64, 3: 64, 4: 80, 5: 80, 6: 64}
	invalidated := func(id uint64) []*types.DevshardTx {
		return []*types.DevshardTx{validation(id, 1, false), validationVote(id, 2, false)}
	}
	validated := func(id uint64) []*types.DevshardTx {
		return []*types.DevshardTx{validation(id, 1, false), validationVote(id, 2, true), validationVote(id, 3, true)}
	}
	faulted := &types.DevshardTx{Tx: &types.DevshardTx_Challenge{Challenge: &types.MsgChallenge{InferenceId: 6, Votes: []*types.ChallengeVote{
		{VoterSlot: 1, Fault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE}, {VoterSlot: 2, Fault: types.ChallengeFault_CHALLENGE_FAULT_PAYLOAD_UNAVAILABLE},
	}}}}
	tip := claimedChain(t, "a", 3, claims, map[uint64][]*types.DevshardTx{
		1: invalidated(1), 2: validated(2), 4: invalidated(4), 5: validated(5), 6: {faulted},
	})
	behind := claimedChain(t, "b", 2, claims, map[uint64][]*types.DevshardTx{2: validated(2), 5: validated(5)})
	var requests []ClientRequest
	var mock []mockopenai.RequestRecord
	for id := uint64(1); id <= 6; id++ {
		requests = append(requests, served(id, 200))
		produced := 40
		if claims[id] == 80 {
			produced = 80
		}
		mock = append(mock, execution(id, 200, produced, "ok", ""))
	}
	rep := CheckPromises(PromiseInput{Diffs: ExportDiffs{Hosts: []HostChain{tip, behind}}, Requests: requests, Mock: mock})

	got := violations(rep, PaidOutputWithinProduced)
	require.Len(t, got, 3, "%+v", got)
	byID := map[uint64]PromiseViolation{}
	for _, v := range got {
		byID[v.Inference.Inference] = v
		require.Equal(t, "a,b", v.Host, "%+v", v)
	}
	require.Contains(t, byID[2].Detail, "Finish output_tokens 64, the ML produced [40]", "validated after a challenge: paid")
	require.Contains(t, byID[3].Detail, "Finish output_tokens 64, the ML produced [40]", "never voted on: paid")
	require.Contains(t, byID[5].Detail, "Finish output_tokens 80 above Start max_tokens 64", "validated after a challenge: paid")

	require.Len(t, rep.CaughtOverclaims, 3, "%+v", rep.CaughtOverclaims)
	caught := map[uint64]PromiseViolation{}
	for _, v := range rep.CaughtOverclaims {
		caught[v.Inference.Inference] = v
		require.Equal(t, PaidOutputWithinProduced, v.Promise)
		require.Equal(t, "a,b", v.Host, "host b is behind the longest chain and judged on it: %+v", v)
	}
	require.Equal(t, "Finish output_tokens 64, the ML produced [40]; invalidated by the validators (invalid weight 2 above vote threshold 1), "+
		"its pay went back to the escrow", caught[1].Detail)
	require.Equal(t, "Finish output_tokens 80 above Start max_tokens 64; invalidated by the validators (invalid weight 2 above vote threshold 1), "+
		"its pay went back to the escrow", caught[4].Detail)
	require.Equal(t, "Finish output_tokens 64, the ML produced [40]; invalidated by a usage challenge (fault weight 2 above vote threshold 1), "+
		"its pay went back to the escrow", caught[6].Detail)

	m := rep.Checked[PaidOutputWithinProduced]
	require.True(t, m.Measured)
	require.Equal(t, 12, m.Judged)
	require.Equal(t, "12 finishes judged across hosts, 12 matched to an ML execution; 6 inferences whose Finish claimed other than the ML produced "+
		"or above max_tokens, 3 of them caught: invalidated, so not paid, and counted apart, not as violations", m.Note)

	var md strings.Builder
	require.NoError(t, WritePromiseReport(&md, "run", rep))
	require.Contains(t, md.String(), "\n## Overclaims caught: invalidated, not paid\n")
	require.Contains(t, md.String(), "\n- `host a,b, escrow 2 inference 6`: "+caught[6].Detail+"\n")
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"caught_overclaims":[{"promise":"paid_output_within_produced","host":"a,b","inference":{"escrow_id":"2","inference_id":1}`)
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

// fiveVoters is a session that gives each of slots 0-4 its own address, at a token price of 100.
func fiveVoters(t *testing.T, threshold uint32) SessionVoting {
	v, ok := sessionVoting(fmt.Sprintf(`{"VoteThreshold":%d,"TokenPrice":100}`, threshold),
		`[{"SlotID":0,"ValidatorAddress":"a0"},{"SlotID":1,"ValidatorAddress":"a1"},{"SlotID":2,"ValidatorAddress":"a2"},`+
			`{"SlotID":3,"ValidatorAddress":"a3"},{"SlotID":4,"ValidatorAddress":"a4"}]`)
	require.True(t, ok)
	return v
}

// votedChain is a host chain of escrow 2 on the fiveVoters session; each inference is started at
// startedAt, finished, then voted on by its txs.
func votedChain(t *testing.T, threshold uint32, startedAt int64, votes map[uint64][]*types.DevshardTx) HostChain {
	c := hostChain("a", nil, nil)
	c.Voting = map[string]SessionVoting{"2": fiveVoters(t, threshold)}
	for id, txs := range votes {
		c.apply("2", &types.DiffContent{Txs: append([]*types.DevshardTx{
			{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: id, InputLength: 10, MaxTokens: 64, StartedAt: startedAt}}},
			{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: id, InputTokens: 10, OutputTokens: 64}}},
		}, txs...)})
	}
	return c
}

func validation(id uint64, slot uint32, valid bool) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: id, ValidatorSlot: slot, Valid: valid}}}
}

func validationVote(id uint64, slot uint32, valid bool) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: &types.MsgValidationVote{InferenceId: id, VoterSlot: slot, VoteValid: valid}}}
}

// Test flow:
//  1. Three honest executions, vote threshold 1. Inference 1: an invalid MsgValidation, then two
//     invalid MsgValidationVote, the first of which invalidates it. Inference 2: an invalid
//     MsgValidation, then two valid votes that validate it. Inference 3: one invalid MsgValidation,
//     and it stays Challenged.
//  2. Check the run.
//  3. Check only the invalidated inference is charged, naming every slot that voted invalid in
//     either phase, and the two the state machine did not invalidate are observations.
func TestPromiseAttributionChargesOnlyAnInvalidation(t *testing.T) {
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{votedChain(t, 1, 0, map[uint64][]*types.DevshardTx{
			1: {validation(1, 4, false), validationVote(1, 1, false), validationVote(1, 3, false)},
			2: {validation(2, 0, false), validationVote(2, 1, true), validationVote(2, 3, true)},
			3: {validation(3, 0, false)},
		})}},
		Requests: []ClientRequest{served(1, 200), served(2, 200), served(3, 200)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 64, "ok", ""), execution(2, 200, 64, "ok", ""), execution(3, 200, 64, "ok", "")},
	}
	rep := CheckPromises(in)
	got := violations(rep, ChargeOnlyTheGuilty)
	require.Len(t, got, 1, "%+v", got)
	require.Equal(t, uint64(1), got[0].Inference.Inference)
	require.Contains(t, got[0].Detail, "an invalidation by the validators (invalid weight 2 above vote threshold 1, slots [4 1 3] voted invalid)")
	require.Equal(t, 1, rep.Checked[ChargeOnlyTheGuilty].Judged)
	var seen []string
	for _, o := range rep.Observations {
		if strings.Contains(o, "voted invalid though the ML answered it in full") {
			seen = append(seen, o)
		}
	}
	require.Len(t, seen, 2, "%v", rep.Observations)
	require.Contains(t, seen[0], "escrow 2 inference 2")
	require.Contains(t, seen[0], "not invalidated: Validated, valid weight 2, invalid 1, vote threshold 1; the executor kept its pay")
	require.Contains(t, seen[1], "escrow 2 inference 3")
	require.Contains(t, seen[1], "not invalidated: Challenged, valid weight 0, invalid 1, vote threshold 1")
}

// stoppedValidation is one honest execution by host, answered at answered, that validator slots 0 and
// 1 (addresses a0 and a1), or the given slots in their order, voted invalid on a vote threshold of 1,
// with the host's fault windows and the validators' payload misses of the run.
func stoppedValidation(t *testing.T, host string, answered time.Time, windows []FaultWindow, misses []PayloadMiss, slots ...uint32) PromiseInput {
	t.Helper()
	r := served(1, 200)
	r.Executions[0].Participant, r.Executions[0].Instance = host, "mock-openai-"+host[len(host)-1:]
	e := execution(1, 200, 64, "ok", "")
	e.StartUnixMs, e.DurationMs = answered.Add(-2*time.Second).UnixMilli(), 2000
	if len(slots) == 0 {
		slots = []uint32{0, 1}
	}
	txs := []*types.DevshardTx{validation(1, slots[0], false)}
	for _, slot := range slots[1:] {
		txs = append(txs, validationVote(1, slot, false))
	}
	return PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{votedChain(t, 1, answered.Add(-2*time.Second).Unix(), map[uint64][]*types.DevshardTx{1: txs})}},
		Requests: []ClientRequest{r},
		Mock:     []mockopenai.RequestRecord{e},
		Layers:   &LoadtestLayers{FaultWindows: windows, PayloadMisses: misses},
	}
}

// payloadMiss is validator address's payload miss of escrow 2 inference 1 at the time.
func payloadMiss(address, at, cause string) PayloadMiss {
	when, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		panic(err)
	}
	return PayloadMiss{At: when, Escrow: "2", Inference: 1, ValidatorHost: "versiond-" + address[1:], Validator: address, Cause: cause}
}

func window(event, host, from, to string, stops bool) FaultWindow {
	f, err := time.Parse(time.RFC3339Nano, from)
	if err != nil {
		panic(err)
	}
	w := FaultWindow{Event: event, Host: host, From: f, StopsPayload: stops}
	if to != "" {
		if w.To, err = time.Parse(time.RFC3339Nano, to); err != nil {
			panic(err)
		}
	}
	return w
}

const portDown503 = "executor payload fault: get payload: unavailable: HTTP status 503 Service Unavailable"

// Test flow:
//  1. The faults-host shape of escrow 12 inference 417: the runner took versiond-3's public port
//     down at 12:33:59.483 until 12:34:44.606; versiond-3 executed honestly, the ML answering in full
//     at 12:34:08, 8.5 s after the port went down; validators a0 and a1 got 503 for its payload at
//     12:34:24.858 and 12:34:31.351 and voted it invalid.
//  2. Check the run and write its report.
//  3. Check the invalidation is not a violation but is counted and listed apart, naming the fault,
//     the misses and the loss, in the note, the Markdown and the JSON.
func TestPromiseAttributionCountsApartAnExecutorWhosePortWasDownWhenValidated(t *testing.T) {
	answered := time.Date(2026, 10, 23, 12, 34, 8, 0, time.UTC)
	in := stoppedValidation(t, "versiond-3", answered,
		[]FaultWindow{window("host-public-port-down tripfile", "versiond-3", "2026-10-23T12:33:59.483369195Z", "2026-10-23T12:34:44.606270148Z", true)},
		[]PayloadMiss{payloadMiss("a0", "2026-10-23T12:34:24.858755553Z", portDown503), payloadMiss("a1", "2026-10-23T12:34:31.351035866Z", portDown503)})
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, ChargeOnlyTheGuilty))
	require.True(t, rep.Checked[ChargeOnlyTheGuilty].Measured)
	require.Equal(t, 1, rep.Checked[ChargeOnlyTheGuilty].Judged)
	require.Contains(t, rep.Checked[ChargeOnlyTheGuilty].Note, "1 invalidated after an honest execution because the executor, stopped before validation, did not serve its payload with the logprobs (by design: #1647, #2002), not violations")

	stopped := "`host a, escrow 2 inference 1`: executor versiond-3 (mock-openai-3) charged an invalidation by the validators " +
		"(invalid weight 2 above vote threshold 1, slots [0 1] voted invalid) on 1 of 1 hosts; the ML answered it in full at 2026-10-23T12:34:08Z, then " +
		"validator versiond-0 (a0) could not fetch its payload at 2026-10-23T12:34:24.858Z (" + portDown503 + ") while the runner's fault \"host-public-port-down tripfile\" " +
		"stopped its host serving it, from 2026-10-23T12:33:59.483Z to 2026-10-23T12:34:44.606Z; " +
		"validator versiond-1 (a1) could not fetch its payload at 2026-10-23T12:34:31.351Z (" + portDown503 + ") while the runner's fault \"host-public-port-down tripfile\" " +
		"stopped its host serving it, from 2026-10-23T12:33:59.483Z to 2026-10-23T12:34:44.606Z, so validators voted it invalid; " +
		"without their votes the invalid weight is 0, not above the vote threshold 1; the executor lost 7400"
	var md strings.Builder
	require.NoError(t, WritePromiseReport(&md, "run", rep))
	require.Contains(t, md.String(), "\n## Invalidated after an honest execution: the executor was stopped before validation\n")
	require.Contains(t, md.String(), "\n- "+stopped+"\n")
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"charged_while_executor_stopped":[{"promise":"charge_only_the_guilty","host":"a","inference":{"escrow_id":"2","inference_id":1}`)
}

// Test flow:
//  1. The prod-like-15m shape of escrow 4 inference 69: versiond-5 executed honestly, the ML answering
//     in full at 12:53:50; the runner paused versiond-5 27 s later, 12:54:17.529 to 12:55:17.839;
//     validators a0 and a1 found no peer session for its payload at 12:54:49.402 and 12:54:53.451.
//  2. Check the run.
//  3. Check the invalidation is counted apart: what counts is when the validators came for the
//     payload, not how soon after the answer the pause began.
func TestPromiseAttributionCountsApartAnExecutorPausedLongAfterItAnswered(t *testing.T) {
	const noSession = "executor payload fault: peer rpc session not ready: context deadline exceeded"
	answered := time.Date(2026, 10, 23, 12, 53, 50, 0, time.UTC)
	rep := CheckPromises(stoppedValidation(t, "versiond-5", answered,
		[]FaultWindow{window("host-paused-then-catches-up pause", "versiond-5", "2026-10-23T12:54:17.529048786Z", "2026-10-23T12:55:17.8388911Z", true)},
		[]PayloadMiss{payloadMiss("a0", "2026-10-23T12:54:49.402853067Z", noSession), payloadMiss("a1", "2026-10-23T12:54:53.451835939Z", noSession)}))
	require.Empty(t, violations(rep, ChargeOnlyTheGuilty))
	require.Len(t, rep.ChargedWhileStopped, 1)
	require.Contains(t, rep.ChargedWhileStopped[0].Detail, `the runner's fault "host-paused-then-catches-up pause" stopped its host serving it`)
}

// Test flow:
//  1. The faults-host shape of inference 417, with the fault on versiond-3 replaced by one that does
//     not stop a host serving its payload (a throttle, a PoC challenge, a marker, a chain participant
//     mode, the payload dropped by trip file); then the public port down again, but with no payload
//     miss of the validators, and with their misses only after the port came back.
//  2. Check each run.
//  3. Check each is a violation: only a stop the validators ran into while fetching the payload
//     excuses the invalidation.
func TestPromiseAttributionExcusesOnlyAStopTheValidatorsRanInto(t *testing.T) {
	answered := time.Date(2026, 10, 23, 12, 34, 8, 0, time.UTC)
	misses := []PayloadMiss{payloadMiss("a0", "2026-10-23T12:34:24.858755553Z", portDown503), payloadMiss("a1", "2026-10-23T12:34:31.351035866Z", portDown503)}
	for _, event := range []string{"slow throttle", "poc dapi_challenge", "budget marker", "absent chain_participant", "withheld tripfile"} {
		rep := CheckPromises(stoppedValidation(t, "versiond-3", answered,
			[]FaultWindow{window(event, "versiond-3", "2026-10-23T12:33:59.483369195Z", "2026-10-23T12:34:44.606270148Z", false)}, misses))
		require.Len(t, violations(rep, ChargeOnlyTheGuilty), 1, event)
		require.Empty(t, rep.ChargedWhileStopped, event)
	}
	down := []FaultWindow{window("host-public-port-down tripfile", "versiond-3", "2026-10-23T12:33:59.483369195Z", "2026-10-23T12:34:20Z", true)}
	for name, m := range map[string][]PayloadMiss{"no miss": nil, "misses after the port came back": misses} {
		rep := CheckPromises(stoppedValidation(t, "versiond-3", answered, down, m))
		require.Len(t, violations(rep, ChargeOnlyTheGuilty), 1, name)
		require.Empty(t, rep.ChargedWhileStopped, name)
	}
}

// Test flow:
//  1. The faults-host shape of inference 417, voted invalid by a3 and a4, which missed no payload,
//     before a0 and a1, which got 503 while the port was down; then by a0 and a3. Vote threshold 1.
//  2. Check each run.
//  3. Check the first is a violation, as a3 and a4 alone invalidate it (weight 2 above 1), and the
//     second is counted apart, as a3 alone (weight 1) would not have.
func TestPromiseAttributionDoesNotExcuseInvalidVotesTheStopDoesNotExplain(t *testing.T) {
	answered := time.Date(2026, 10, 23, 12, 34, 8, 0, time.UTC)
	down := []FaultWindow{window("host-public-port-down tripfile", "versiond-3", "2026-10-23T12:33:59.483369195Z", "2026-10-23T12:34:44.606270148Z", true)}
	misses := []PayloadMiss{payloadMiss("a0", "2026-10-23T12:34:24.858755553Z", portDown503), payloadMiss("a1", "2026-10-23T12:34:31.351035866Z", portDown503)}
	rep := CheckPromises(stoppedValidation(t, "versiond-3", answered, down, misses, 3, 4, 0, 1))
	require.Len(t, violations(rep, ChargeOnlyTheGuilty), 1)
	require.Empty(t, rep.ChargedWhileStopped)

	rep = CheckPromises(stoppedValidation(t, "versiond-3", answered, down, misses, 0, 3))
	require.Empty(t, violations(rep, ChargeOnlyTheGuilty))
	require.Contains(t, rep.ChargedWhileStopped[0].Detail, "without their votes the invalid weight is 1, not above the vote threshold 1")
}

// Test flow:
//  1. One honest inference with an invalid vote on three hosts, none of which holds the escrow's
//     session row.
//  2. Check the run.
//  3. Check the note counts one inference not weighed, not three, and the check gives no verdict
//     instead of holding: whether the honest executor was invalidated cannot be told.
func TestPromiseAttributionWithoutASessionRowIsNotHeld(t *testing.T) {
	f := func() *InferenceFacts {
		return &InferenceFacts{Started: true, Finished: true, Rejected: []uint32{1}, Votes: []ValidationVote{{Slot: 1}}}
	}
	in := PromiseInput{
		Diffs: ExportDiffs{Hosts: []HostChain{
			hostChain("a", map[uint64]*InferenceFacts{1: f(), 2: {Started: true, TimedOut: true}}, nil),
			hostChain("b", map[uint64]*InferenceFacts{1: f()}, nil),
			hostChain("c", map[uint64]*InferenceFacts{1: f()}, nil),
		}},
		Requests: []ClientRequest{served(1, 200), served(2, 502)},
		Mock:     []mockopenai.RequestRecord{execution(1, 200, 64, "ok", ""), execution(2, 502, 0, "rule_status", "boom")},
	}
	rep := CheckPromises(in)
	m := rep.Checked[ChargeOnlyTheGuilty]
	require.False(t, m.Measured, "%+v", m)
	require.Contains(t, m.Note, "not measured: 1 inferences with an invalid vote or a challenge not weighed")
	require.Equal(t, "not measured", PromiseResult(rep, ChargeOnlyTheGuilty))
}

// pausedStream is the c8 faults-host shape of escrow 1 inferences 1034 and 1104: versiond-5 took
// each with a receipt at its start and streamed the ML's 2048 tokens for about a minute; the runner
// paused versiond-5 at 18:44:48.866 until 18:45:49.115, the gateway lost the stream at 18:45:09, and
// the timeout votes (execution 90 s) charged a miss on every host. The ML answered in full at
// 18:45:09.170 and 18:45:15.635. windows are the executor host's fault windows; timeouts the escrow's.
func pausedStream(t *testing.T, windows []FaultWindow, timeouts map[string]EscrowTimeouts) PromiseInput {
	t.Helper()
	at := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339Nano, s)
		require.NoError(t, err)
		return v
	}
	started := map[uint64]time.Time{1: at("2026-10-23T18:44:09.585Z"), 2: at("2026-10-23T18:44:15.645Z")}
	answered := map[uint64]time.Time{1: at("2026-10-23T18:45:09.170Z"), 2: at("2026-10-23T18:45:15.635Z")}
	facts := map[uint64]*InferenceFacts{}
	var requests []ClientRequest
	var ml []mockopenai.RequestRecord
	for id := uint64(1); id <= 2; id++ {
		facts[id] = &InferenceFacts{Started: true, StartedAt: started[id].Unix(), ConfirmedAt: started[id].Unix(), TimedOut: true}
		r := served(id, 200)
		r.Executions[0].Participant, r.Executions[0].Instance = "versiond-5", "mock-openai-5"
		requests = append(requests, r)
		e := execution(id, 200, 2048, "ok", "")
		e.StartUnixMs, e.DurationMs = started[id].UnixMilli(), answered[id].Sub(started[id]).Milliseconds()
		ml = append(ml, e)
	}
	return PromiseInput{
		Diffs:    ExportDiffs{Hosts: []HostChain{hostChain("a", facts, nil)}},
		Requests: requests,
		Mock:     ml,
		Timeouts: timeouts,
		Layers:   &LoadtestLayers{FaultWindows: windows},
	}
}

// Test flow:
//  1. The c8 faults-host shape: two honest executions on versiond-5 timed out while the runner had
//     paused versiond-5 between their start and their timeout deadline.
//  2. Check the run and write its report.
//  3. Check neither miss is a violation: both are counted and listed apart, naming the fault, in the
//     note, the Markdown and the JSON.
func TestPromiseAttributionCountsApartAMissWhileTheExecutorsHostWasStopped(t *testing.T) {
	in := pausedStream(t, []FaultWindow{window("host-paused-then-catches-up pause", "versiond-5", "2026-10-23T18:44:48.866848387Z", "2026-10-23T18:45:49.115022119Z", true)},
		map[string]EscrowTimeouts{"2": {Refusal: 5, Execution: 90}})
	rep := CheckPromises(in)
	require.Empty(t, violations(rep, ChargeOnlyTheGuilty))
	require.Len(t, rep.MissedWhileStopped, 2)
	require.Equal(t, 2, rep.Checked[ChargeOnlyTheGuilty].Judged)
	require.Contains(t, rep.Checked[ChargeOnlyTheGuilty].Note, "2 missed because a fault of the runner stopped the executor's host before it could finish, not violations")

	first := "`host a, escrow 2 inference 1`: executor versiond-5 (mock-openai-5) charged a timeout (miss) on 1 of 1 hosts; " +
		"missed because a fault of the runner stopped the executor's host before it could finish: the runner's fault \"host-paused-then-catches-up pause\" " +
		"stopped versiond-5 from 2026-10-23T18:44:48.866Z to 2026-10-23T18:45:49.115Z, between the inference's start at 2026-10-23T18:44:09Z " +
		"and its timeout deadline at 2026-10-23T18:45:44Z; the ML answered it in full at 2026-10-23T18:45:09.17Z"
	var md strings.Builder
	require.NoError(t, WritePromiseReport(&md, "run", rep))
	require.Contains(t, md.String(), "\n## Missed because a fault of the runner stopped the executor's host before it could finish\n")
	require.Contains(t, md.String(), "\n- "+first+"\n")
	raw, err := json.Marshal(rep)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"missed_while_executor_stopped":[{"promise":"charge_only_the_guilty","host":"a","inference":{"escrow_id":"2","inference_id":1}`)
}

// Test flow:
//  1. The c8 faults-host shape with the pause moved after both timeout deadlines (18:46:00 to
//     18:47:00) or before both starts (18:40:00 to 18:41:00); with the pause in place but a fault
//     that does not stop the host (a throttle); and with the escrow's timeouts unknown.
//  2. Check each run.
//  3. Check both misses are violations each time: only a stop of the executor's host between the
//     start and the timeout deadline of that inference explains its miss.
func TestPromiseAttributionChargesAMissWithoutAStopInItsWindow(t *testing.T) {
	timeouts := map[string]EscrowTimeouts{"2": {Refusal: 5, Execution: 90}}
	for name, in := range map[string]PromiseInput{
		"pause after the deadlines": pausedStream(t, []FaultWindow{window("late pause", "versiond-5", "2026-10-23T18:46:00Z", "2026-10-23T18:47:00Z", true)}, timeouts),
		"pause before the starts":   pausedStream(t, []FaultWindow{window("early pause", "versiond-5", "2026-10-23T18:40:00Z", "2026-10-23T18:41:00Z", true)}, timeouts),
		"pause of another host":     pausedStream(t, []FaultWindow{window("other pause", "versiond-4", "2026-10-23T18:44:48.866848387Z", "2026-10-23T18:45:49.115022119Z", true)}, timeouts),
		"a throttle":                pausedStream(t, []FaultWindow{window("slow throttle", "versiond-5", "2026-10-23T18:44:48.866848387Z", "2026-10-23T18:45:49.115022119Z", false)}, timeouts),
		"timeouts unknown":          pausedStream(t, []FaultWindow{window("host-paused-then-catches-up pause", "versiond-5", "2026-10-23T18:44:48.866848387Z", "2026-10-23T18:45:49.115022119Z", true)}, nil),
	} {
		rep := CheckPromises(in)
		require.Len(t, violations(rep, ChargeOnlyTheGuilty), 2, name)
		require.Empty(t, rep.MissedWhileStopped, name)
	}
}

// Test flow:
//  1. Every ML execution of a request was a client error, the client got a 502, and the mock's
//     error bodies are not declared vLLM-shaped.
//  2. Check the run.
//  3. Check the mismatch is an observation and the check gives no verdict instead of "held".
func TestPromiseClientErrorWithoutVLLMShapeIsNotHeld(t *testing.T) {
	rep := CheckPromises(PromiseInput{
		Requests: []ClientRequest{served(1, 502)},
		Mock:     []mockopenai.RequestRecord{execution(1, 400, 0, "rule_status", "bad")},
	})
	require.Empty(t, rep.Violations)
	require.NotEqual(t, "held", PromiseResult(rep, ClientErrorAnswered4xx))
	require.Contains(t, rep.Checked[ClientErrorAnswered4xx].Note, "1 mismatches reported as observations")
}

// Test flow:
//  1. Three host chains: the first holds no chain of escrow 2, the other two fork at nonce 2.
//  2. Check convergence.
//  3. Check the fork is found although the first host cannot be the reference.
func TestConvergenceComparesRootsWhenTheFirstHostLacksTheEscrow(t *testing.T) {
	none := hostChain("a", nil, nil)
	none.Roots = map[string]map[uint64]string{"9": {1: "zz"}}
	in := PromiseInput{Diffs: ExportDiffs{Hosts: []HostChain{
		none,
		hostChain("b", nil, map[uint64]string{1: "aa", 2: "bb"}),
		hostChain("c", nil, map[uint64]string{1: "aa", 2: "cc"}),
	}}}
	got := violations(CheckPromises(in), HostsConverge)
	require.Len(t, got, 1)
	require.Equal(t, "c", got[0].Host)
	require.Contains(t, got[0].Detail, "escrow 2: post_state_root differs from b from nonce 2 on")
}
