package loadtest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/mockchain/ledgerface"

	"github.com/stretchr/testify/require"
)

var fixtureT0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func at(offset time.Duration) time.Time { return fixtureT0.Add(offset) }

func atPtr(offset time.Duration) *time.Time {
	t := at(offset)
	return &t
}

func accountingAt(offset time.Duration, label string, misses map[string]uint64) AccountingSnapshot {
	snapshot := AccountingSnapshot{At: at(offset), Label: label, EpochIndex: 1}
	for _, p := range []string{"p0", "p1", "p2", "p3"} {
		snapshot.Participants = append(snapshot.Participants, accountingParticipant{Participant: p, Model: "m-a", ProtocolMisses: misses[p], CrossChecks: accountingCrossChecks{HostMissed: misses[p]}})
	}
	return snapshot
}

func processSample(target string, offset time.Duration, pid, goroutines, heap float64) MetricsSample {
	return MetricsSample{At: at(offset), Phase: "workload", Target: target, Values: map[string]float64{
		"loadtest_process_pid": pid, "loadtest_process_start_ticks": pid, "go_goroutines": goroutines,
		"go_memstats_heap_inuse_bytes": heap, "process_resident_memory_bytes": heap * 2,
	}}
}

func transitionKey(participant, mode, reason string) string {
	return quarantineFamily + `{mode="` + mode + `",model="m-a",participant_key="` + participant + `",reason="` + reason + `"}`
}

func settleTx(seq uint64, escrow, nonce uint64, root string, fees uint64, stats ...ledgerface.SettleHostStatView) ledgerface.TxView {
	return ledgerface.TxView{Seq: seq, Type: "settle", Settle: &ledgerface.SettleTxView{EscrowID: escrow, Nonce: nonce, StateRootAndProtocolVersion: root, Fees: fees, HostStats: stats}}
}

func shardSample(offset time.Duration, host, participant, escrow string, nonce uint64, root string) HostStatsSample {
	return HostStatsSample{At: at(offset), Host: host, Participant: participant, Kind: "shard", EscrowID: escrow, Nonce: nonce, StateRoot: root,
		Group: []HostSlot{{0, "p0"}, {1, "p1"}, {2, "p2"}, {3, "p3"}}}
}

// passingInputs is a run where every invariant holds: p2 serves an empty
// stream fault (receipt-then-empty-stream), p3 a PoC challenge (host-under-poc-challenge), and the gateway restarts (gateway-killed-mid-race).
func passingInputs() InvariantInputs {
	in := InvariantInputs{
		Started:  fixtureT0,
		Workload: 10 * time.Minute,
		Settings: invariantSettings{
			Grace: 95 * time.Second, MaxPendingAge: 220 * time.Second, SteadyAfter: 2 * time.Minute,
			Baseline: [2]time.Duration{time.Minute, 2 * time.Minute}, TrendEarly: [2]time.Duration{2 * time.Minute, 4 * time.Minute},
			TrendLate: [2]time.Duration{8 * time.Minute, 10 * time.Minute}, Cases: map[string]string{"no-runtime-for-deactivated-escrow": "escrow-rotation-per-model-settlement"},
			Quarantine: map[string]time.Duration{"empty_stream": 45 * time.Second},
		},
		Hosts:     []InvariantHost{{"versiond-0", "p0"}, {"versiond-1", "p0"}, {"versiond-2", "p1"}, {"versiond-3", "p2"}, {"versiond-4", "p3"}},
		NodeHosts: map[string]string{"n2": "versiond-3"},
		Events: []EventRecord{
			{ID: "e1", Case: "receipt-then-empty-stream", Action: "ml_fault", Target: "n2", Params: map[string]any{"patch": map[string]any{"empty_stream": true}}, StartedAt: atPtr(3 * time.Minute), EndedAt: atPtr(4 * time.Minute)},
			{ID: "e2", Case: "gateway-killed-mid-race", Action: "gateway_restart", Target: "devshardctl", StartedAt: atPtr(5 * time.Minute), EndedAt: atPtr(5*time.Minute + 10*time.Second)},
			{ID: "e3", Case: "host-under-poc-challenge", Action: "dapi_challenge", Target: "versiond-4", StartedAt: atPtr(6 * time.Minute), EndedAt: atPtr(7 * time.Minute)},
		},
		Accounting: []AccountingSnapshot{
			accountingAt(0, "baseline", nil),
			accountingAt(3*time.Minute, "e1:start", nil),
			accountingAt(5*time.Minute+35*time.Second, "e1:end+grace", map[string]uint64{"p2": 4}),
			accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 4}),
		},
		SettleTxs: []ledgerface.TxView{settleTx(1, 10, 40, "r40", 10,
			ledgerface.SettleHostStatView{SlotID: 0, Cost: 300}, ledgerface.SettleHostStatView{SlotID: 1, Cost: 200}, ledgerface.SettleHostStatView{SlotID: 2, Cost: 100, Missed: 4})},
		Escrows: []ledgerface.EscrowView{{ID: 10, ModelID: "m-a", Amount: 1000, Settled: true}, {ID: 11, ModelID: "m-a", Amount: 1000}, {ID: 12, ModelID: "m-b", Amount: 1000}},
		Devshards: []GatewayDevshard{
			{ID: "10", Model: "m-a"},
			{ID: "11", Model: "m-a", Active: true, Resident: true, Nonce: 50},
			{ID: "12", Model: "m-b"},
		},
		Inferences: map[string]map[string]DebugInference{
			"10": {"1": {Status: "finished", ActualCost: 400, StartedAt: at(time.Minute).Unix()}, "2": {Status: "validated", ActualCost: 200}, "3": {Status: "timed_out", ActualCost: 99}},
			"11": {"1": {Status: "finished", StartedAt: at(9 * time.Minute).Unix()}},
		},
		SettlementDisabled: map[string]bool{"m-b": true},
		Leases:             []LeaseCount{{"p0", "submitted", 10}, {"p2", "skipped", 1}},
	}
	for offset := time.Duration(0); offset < in.Workload; offset += time.Second {
		in.Requests = append(in.Requests, RequestResult{StartedAt: at(offset), Duration: 100 * time.Millisecond, Status: 200, Outcome: "completed"})
	}
	for offset := 3*time.Minute + 25*time.Second; offset <= 4*time.Minute+15*time.Second; offset += throttlePollEvery {
		quarantined := offset >= 3*time.Minute+30*time.Second && offset <= 4*time.Minute+10*time.Second
		p := ThrottleParticipant{ParticipantKey: "p2", ShadowQuarantined: quarantined, FailureStrikes: 3}
		if quarantined {
			p.QuarantineUntil = at(3*time.Minute + 30*time.Second + 45*time.Second).Format(time.RFC3339Nano)
		}
		in.Throttle = append(in.Throttle, ThrottleSample{At: at(offset), EscrowID: "11", Participants: []ThrottleParticipant{p, {ParticipantKey: "p1"}}})
	}
	for offset := time.Duration(0); offset < in.Workload; offset += 10 * time.Second {
		pid := 1.0
		if offset >= 5*time.Minute+10*time.Second {
			pid = 2
		}
		gateway := processSample("gateway", offset, pid, 100, 50<<20)
		if pid == 1 {
			gateway.Domain = map[string]float64{transitionKey("p2", "shadow", "empty_stream_quarantine"): 0}
			if offset >= 3*time.Minute+30*time.Second {
				gateway.Domain[transitionKey("p2", "shadow", "empty_stream_quarantine")] = 1
			}
		}
		host := processSample("versiond-2/devshardd", offset, 7, 200, 80<<20)
		host.Domain = map[string]float64{`devshard_diff_fork_detected_total{}`: 0}
		in.Metrics = append(in.Metrics, gateway, host)
		in.Pending = append(in.Pending, PendingSample{At: at(offset), EscrowID: "11", Nonce: uint64(offset / time.Second), MaxAgeSeconds: 4, Open: 1})
	}
	for offset := time.Duration(0); offset <= in.Workload; offset += dapiHostsPollEvery {
		n := uint64(offset / dapiHostsPollEvery)
		var rejections uint64
		if offset > 6*time.Minute {
			rejections = uint64((min(offset, 7*time.Minute) - 6*time.Minute) / dapiHostsPollEvery)
		}
		in.DapiHosts = append(in.DapiHosts, DapiHostSample{At: at(offset), Hosts: map[string]DapiHostRow{
			"versiond-3":     {Allocations: n, NodeAllocations: map[string]uint64{"n2": n}},
			"versiond-4":     {ChallengeRejections: rejections},
			dapiUnmappedHost: {},
		}})
	}
	in.Pending = append(in.Pending, PendingSample{At: at(10*time.Minute + 5*time.Second), EscrowID: "11", Nonce: 50})
	in.Pending = append(in.Pending, PendingSample{At: at(2 * time.Minute), EscrowID: "10", Nonce: 40})
	in.HostStats = []HostStatsSample{
		shardSample(6*time.Minute, "versiond-0", "p0", "10", 40, "r40"),
		shardSample(6*time.Minute, "versiond-2", "p1", "10", 40, "r40"),
		shardSample(10*time.Minute, "versiond-0", "p0", "11", 50, "r50"),
		shardSample(10*time.Minute, "versiond-1", "p0", "11", 50, "r50"),
		shardSample(10*time.Minute, "versiond-2", "p1", "11", 50, "r50"),
		shardSample(10*time.Minute, "versiond-3", "p2", "11", 50, "r50"),
		shardSample(4*time.Minute+30*time.Second, "versiond-3", "p2", "11", 50, "lagging"),
	}
	return in
}

func checksNamed(t *testing.T, checks []AssertionResult, name string) []AssertionResult {
	t.Helper()
	var found []AssertionResult
	for _, check := range checks {
		if check.Name == "invariants."+name {
			found = append(found, check)
		}
	}
	require.NotEmpty(t, found, "no check named %s", name)
	return found
}

type invariantCase struct {
	name   string
	check  string
	mutate func(*InvariantInputs)
	passed bool
}

func runInvariantCases(t *testing.T, cases []invariantCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := passingInputs()
			if tc.mutate != nil {
				tc.mutate(&in)
			}
			checks, _ := evaluateInvariants(in)
			for _, check := range checksNamed(t, checks, tc.check) {
				require.Equal(t, tc.passed, check.Passed, "%s: actual=%s details=%s", check.Name, check.Actual, check.Details)
			}
		})
	}
}

// Test flow:
// 1. Evaluate the passing fixture and require every check to pass.
// 2. Check the honest set, the graced fault windows and the written invariants.json.
func TestEvaluateInvariantsPassingRun(t *testing.T) {
	checks, report := evaluateInvariants(passingInputs())
	for _, check := range checks {
		require.True(t, check.Passed, "%s: expected %s; actual %s; %s", check.Name, check.Expected, check.Actual, check.Details)
	}
	require.Equal(t, []string{"p0", "p1"}, report.Honest)
	require.Len(t, report.Faulty, 2)
	require.Equal(t, at(4*time.Minute+95*time.Second), report.Faulty[0].GracedTo)
	dir := t.TempDir()
	require.NoError(t, writeInvariantReport(dir, report))
	body, err := os.ReadFile(filepath.Join(dir, "invariants.json"))
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded))
	require.Contains(t, decoded["tables"], "escrow-accounting-reconciles")
}

// Test flow:
// 1. Charge a miss to an honest host in GW-acct and in a settle tx.
// 2. Charge a faulty host outside its window, and inside a waived challenge window.
// 3. Require each misses-only-in-fault-windows check to fail only for its own fixture.
func TestInvariantMissesOnlyOnFaultyHosts(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass gateway", check: "misses-only-in-fault-windows.honest_misses_gateway", passed: true},
		{name: "miss on an honest host", check: "misses-only-in-fault-windows.honest_misses_gateway", passed: false, mutate: func(in *InvariantInputs) {
			in.Accounting[3] = accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 4, "p1": 1})
		}},
		{name: "pass attribution", check: "misses-only-in-fault-windows.misses_attributed", passed: true},
		{name: "faulty miss outside any window", check: "misses-only-in-fault-windows.misses_attributed", passed: false, mutate: func(in *InvariantInputs) {
			in.Accounting[3] = accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 5})
		}},
		{name: "pass waived", check: "misses-only-in-fault-windows.e3.waived_misses", passed: true},
		{name: "miss charged under challenge", check: "misses-only-in-fault-windows.e3.waived_misses", passed: false, mutate: func(in *InvariantInputs) {
			in.Accounting[3] = accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 4, "p3": 2})
		}},
		{name: "pass chain", check: "misses-only-in-fault-windows.honest_misses_chain", passed: true},
		{name: "settle charges honest slot", check: "misses-only-in-fault-windows.honest_misses_chain", passed: false, mutate: func(in *InvariantInputs) {
			in.SettleTxs[0].Settle.HostStats[1].Missed = 1
		}},
		{name: "too few snapshots", check: "misses-only-in-fault-windows.honest_misses_gateway", passed: false, mutate: func(in *InvariantInputs) {
			in.Accounting = in.Accounting[:1]
		}},
	})
}

// Test flow:
// 1. Settle a deactivated escrow twice, and settle an escrow of a model with settlement off.
// 2. Overspend the escrow, settle behind the gateway nonce, and mismatch inference cost.
// 3. Leave a challenged inference whose cost is still in host_stats.
// 4. Require the matching escrow-accounting-reconciles check to fail, and the passing fixture and the challenged cost to pass.
func TestInvariantEscrowAccounting(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass count", check: "escrow-accounting-reconciles.settle_count", passed: true},
		{name: "settled twice", check: "escrow-accounting-reconciles.settle_count", passed: false, mutate: func(in *InvariantInputs) {
			in.SettleTxs = append(in.SettleTxs, settleTx(2, 10, 40, "r40", 10))
		}},
		{name: "settlement off but settled", check: "escrow-accounting-reconciles.settle_count", passed: false, mutate: func(in *InvariantInputs) {
			in.SettleTxs = append(in.SettleTxs, settleTx(2, 12, 5, "r5", 1))
		}},
		{name: "deactivated never settled", check: "escrow-accounting-reconciles.settle_count", passed: false, mutate: func(in *InvariantInputs) {
			in.SettleTxs = nil
		}},
		{name: "pass balance", check: "escrow-accounting-reconciles.balance", passed: true},
		{name: "cost above amount", check: "escrow-accounting-reconciles.balance", passed: false, mutate: func(in *InvariantInputs) { in.Escrows[0].Amount = 500 }},
		{name: "pass nonce", check: "escrow-accounting-reconciles.settled_nonce", passed: true},
		{name: "settled behind gateway", check: "escrow-accounting-reconciles.settled_nonce", passed: false, mutate: func(in *InvariantInputs) { in.SettleTxs[0].Settle.Nonce = 30 }},
		{name: "pass cost", check: "escrow-accounting-reconciles.cost_matches_inferences", passed: true},
		{name: "cost mismatch", check: "escrow-accounting-reconciles.cost_matches_inferences", passed: false, mutate: func(in *InvariantInputs) {
			in.Inferences["10"]["4"] = DebugInference{Status: "finished", ActualCost: 1}
		}},
		{name: "challenged keeps its cost", check: "escrow-accounting-reconciles.cost_matches_inferences", passed: true, mutate: func(in *InvariantInputs) {
			in.Inferences["10"]["4"] = DebugInference{Status: "challenged", ActualCost: 50}
			in.SettleTxs[0].Settle.HostStats[0].Cost += 50
		}},
		{name: "ledger unavailable", check: "escrow-accounting-reconciles.settle_count", passed: false, mutate: func(in *InvariantInputs) { in.ChainError = "connection refused" }},
	})
}

// Test flow:
// 1. Give an honest host a different root at a shared nonce, and a lagging faulty host one inside its recovery window.
// 2. Mismatch the settle root and increase the fork counter.
// 3. Require only real disagreements to fail.
func TestInvariantStateRoots(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass with lagging faulty host excluded", check: "honest-hosts-agree-on-state.state_roots_agree", passed: true},
		{name: "honest roots differ", check: "honest-hosts-agree-on-state.state_roots_agree", passed: false, mutate: func(in *InvariantInputs) {
			in.HostStats[4].StateRoot = "fork"
		}},
		{name: "recovered faulty host differs", check: "honest-hosts-agree-on-state.state_roots_agree", passed: false, mutate: func(in *InvariantInputs) {
			in.HostStats[5].StateRoot = "stale"
		}},
		{name: "no shared nonce", check: "honest-hosts-agree-on-state.state_roots_agree", passed: false, mutate: func(in *InvariantInputs) { in.HostStats = in.HostStats[:1] }},
		{name: "pass settle root", check: "honest-hosts-agree-on-state.settle_root_matches", passed: true},
		{name: "settle root differs", check: "honest-hosts-agree-on-state.settle_root_matches", passed: false, mutate: func(in *InvariantInputs) {
			in.SettleTxs[0].Settle.StateRootAndProtocolVersion = "other"
		}},
		{name: "pass fork counter", check: "honest-hosts-agree-on-state.no_fork_detected", passed: true},
		{name: "fork detected", check: "honest-hosts-agree-on-state.no_fork_detected", passed: false, mutate: func(in *InvariantInputs) {
			last := len(in.Metrics) - 1
			in.Metrics[last].Domain = map[string]float64{`devshard_diff_fork_detected_total{}`: 1}
		}},
	})
}

// Test flow:
// 1. Raise one pending sample above the age bound.
// 2. Leave an open inference in the final poll.
// 3. Remove all pending samples.
// 4. Leave two final-poll samples of one escrow open; require one dirty escrow, from its latest sample.
func TestInvariantPendingAge(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass age", check: "no-nonce-left-pending.max_pending_age", passed: true},
		{name: "stuck pending", check: "no-nonce-left-pending.max_pending_age", passed: false, mutate: func(in *InvariantInputs) { in.Pending[20].MaxAgeSeconds = 400 }},
		{name: "pass end state", check: "no-nonce-left-pending.clean_end_state", passed: true},
		{name: "open at end", check: "no-nonce-left-pending.clean_end_state", passed: false, mutate: func(in *InvariantInputs) { in.Pending[len(in.Pending)-2].Open = 2 }},
		{name: "no samples", check: "no-nonce-left-pending.max_pending_age", passed: false, mutate: func(in *InvariantInputs) { in.Pending = nil }},
	})

	in := passingInputs()
	last := in.Pending[len(in.Pending)-2]
	last.Open = 3
	in.Pending[len(in.Pending)-2] = last
	last.At, last.Open = last.At.Add(-2*time.Second), 4
	in.Pending = append(in.Pending, last)
	checks, _ := evaluateInvariants(in)
	dirty := checksNamed(t, checks, "no-nonce-left-pending.clean_end_state")[0]
	require.Equal(t, "dirty=1", dirty.Actual)
	require.Equal(t, "escrow 11 open=3 pending_txs=0", dirty.Details)
}

// Test flow:
// 1. Give one pending sample an open nonce 400 s old, above the 220 s bound, and require max_pending_age to fail.
// 2. Record that nonce as burned by the gateway.
// 3. Require max_pending_age to pass and the burned nonce to be reported apart: burned max age 400 s, one sample over the bound.
// 4. Drop the per-nonce start times, as runs before them were written, and require the burned nonce to count again, with a note.
func TestInvariantPendingAgeLeavesOutBurnedNonces(t *testing.T) {
	stuck := func() InvariantInputs {
		in := passingInputs()
		s := &in.Pending[20]
		s.Open, s.MaxAgeSeconds, s.OldestInference, s.OldestStatus = 2, 400, "77", "pending"
		s.OpenStarted = map[string]int64{"20": s.At.Add(-4 * time.Second).Unix(), "77": s.At.Add(-400 * time.Second).Unix()}
		return in
	}
	table := func(t *testing.T, report InvariantReport) map[string]any {
		t.Helper()
		body, err := json.Marshal(report.Tables["no-nonce-left-pending"])
		require.NoError(t, err)
		var decoded map[string]any
		require.NoError(t, json.Unmarshal(body, &decoded))
		return decoded
	}

	checks, _ := evaluateInvariants(stuck())
	require.False(t, checksNamed(t, checks, "no-nonce-left-pending.max_pending_age")[0].Passed)

	in := stuck()
	in.Burns = []gatewayBurn{{At: at(-200 * time.Second), EscrowID: "11", Nonce: "77", Host: "p2", Reason: "participant_ejected_no_send"}, {EscrowID: "10", Nonce: "20"}}
	checks, report := evaluateInvariants(in)
	check := checksNamed(t, checks, "no-nonce-left-pending.max_pending_age")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
	require.Contains(t, check.Actual, "burned_max=400s burned_samples_over=1")
	require.Equal(t, 400.0, table(t, report)["burned_max_age_seconds"])
	require.Equal(t, 1.0, table(t, report)["burned_samples_over_limit"])

	in.Pending[20].OpenStarted = nil
	checks, report = evaluateInvariants(in)
	require.False(t, checksNamed(t, checks, "no-nonce-left-pending.max_pending_age")[0].Passed)
	require.Contains(t, table(t, report)["note"], "per-nonce start times")
}

// Test flow:
// 1. Keep a deactivated escrow resident in the gateway registry.
// 2. Require no-runtime-for-deactivated-escrow to fail, carry the case from the settings, and pass otherwise.
func TestInvariantResidentRuntimes(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "no-runtime-for-deactivated-escrow.no_resident_deactivated", passed: true},
		{name: "deactivated but resident", check: "no-runtime-for-deactivated-escrow.no_resident_deactivated", passed: false, mutate: func(in *InvariantInputs) { in.Devshards[2].Resident = true }},
		{name: "devshards unavailable", check: "no-runtime-for-deactivated-escrow.no_resident_deactivated", passed: false, mutate: func(in *InvariantInputs) { in.Devshards = nil }},
	})
	checks, _ := evaluateInvariants(passingInputs())
	require.Equal(t, "escrow-rotation-per-model-settlement", checksNamed(t, checks, "no-runtime-for-deactivated-escrow.no_resident_deactivated")[0].Case)
}

// Test flow:
// 1. Quarantine an honest host, give the empty-stream fault an http reason, and give the challenged host an empty_stream quarantine.
// 2. Stretch quarantine_until beyond the configured duration.
// 3. Require each quarantines-match-faults check to fail on its fixture.
func TestInvariantQuarantine(t *testing.T) {
	addTransition := func(in *InvariantInputs, offset time.Duration, participant, mode, reason string) {
		for i := range in.Metrics {
			if in.Metrics[i].Target == "gateway" && in.Metrics[i].At.After(at(offset-time.Second)) && in.Metrics[i].At.Before(at(5*time.Minute+10*time.Second)) {
				in.Metrics[i].Domain[transitionKey(participant, mode, reason)] = 1
			} else if in.Metrics[i].Target == "gateway" && in.Metrics[i].Domain != nil {
				in.Metrics[i].Domain[transitionKey(participant, mode, reason)] = 0
			}
		}
	}
	runInvariantCases(t, []invariantCase{
		{name: "pass honest", check: "quarantines-match-faults.honest_not_quarantined", passed: true},
		{name: "honest quarantined", check: "quarantines-match-faults.honest_not_quarantined", passed: false, mutate: func(in *InvariantInputs) {
			in.Throttle[3].Participants[1].Quarantined = true
		}},
		{name: "pass reason", check: "quarantines-match-faults.e1.reason", passed: true},
		{name: "wrong reason", check: "quarantines-match-faults.e1.reason", passed: false, mutate: func(in *InvariantInputs) {
			addTransition(in, 3*time.Minute+40*time.Second, "p2", "probe", "http_throttle_quarantine")
		}},
		{name: "pass challenge", check: "quarantines-match-faults.e3.reason", passed: true},
		{name: "challenge gets empty_stream quarantine", check: "quarantines-match-faults.e3.reason", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.Metrics {
				if in.Metrics[i].Target == "gateway" && in.Metrics[i].At.After(at(5*time.Minute+10*time.Second)) {
					value := 0.0
					if in.Metrics[i].At.After(at(6*time.Minute + 20*time.Second)) {
						value = 1
					}
					in.Metrics[i].Domain = map[string]float64{transitionKey("p3", "shadow", "empty_stream_quarantine"): value}
				}
			}
		}},
		{name: "pass durations", check: "quarantines-match-faults.durations", passed: true},
		{name: "quarantine too long", check: "quarantines-match-faults.durations", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.Throttle {
				if in.Throttle[i].Participants[0].ShadowQuarantined {
					in.Throttle[i].Participants[0].QuarantineUntil = at(6 * time.Minute).Format(time.RFC3339Nano)
				}
			}
		}},
		{name: "unattributed transition", check: "quarantines-match-faults.quarantine_attributed", passed: false, mutate: func(in *InvariantInputs) {
			addTransition(in, 2*time.Minute, "p3", "probe", "transport_failure_quarantine")
		}},
	})
}

func failRequests(from, to time.Duration, every int, status int) func(*InvariantInputs) {
	return func(in *InvariantInputs) {
		for i := range in.Requests {
			offset := in.Requests[i].StartedAt.Sub(fixtureT0)
			if offset >= from && offset < to && i%every == 0 {
				in.Requests[i].Outcome, in.Requests[i].Status = "error", status
			}
		}
	}
}

func addOverlappingFault(in *InvariantInputs) {
	in.Events = append(in.Events, EventRecord{ID: "e5", Case: "host-hangs-after-first-token", Action: "ml_fault", Target: "n2", StartedAt: atPtr(6*time.Minute + 30*time.Second), EndedAt: atPtr(6*time.Minute + 50*time.Second)})
}

func dropRequests(from, to time.Duration, every int) func(*InvariantInputs) {
	return func(in *InvariantInputs) {
		for i := range in.Requests {
			offset := in.Requests[i].StartedAt.Sub(fixtureT0)
			if offset >= from && offset < to && i%every == 0 {
				in.Requests[i] = RequestResult{StartedAt: in.Requests[i].StartedAt, Outcome: "dropped_by_generator", Error: "max_in_flight reached"}
			}
		}
	}
}

// Test flow:
// 1. Add errors in quiet time, slow quiet requests, and errors inside the empty-stream window.
// 2. Add non-503 errors inside an epoch-advance window and remove the baseline.
// 3. Add generator drops in quiet time and errors where two fault windows overlap.
// 4. Require each error-rate-and-latency check to fail on its fixture, and drops and overlaps to stay out of the single-fault bounds.
func TestInvariantLatencyAndErrors(t *testing.T) {
	fail := failRequests
	runInvariantCases(t, []invariantCase{
		{name: "generator drops are not gateway errors", check: "error-rate-and-latency.quiet_error_rate", passed: true, mutate: dropRequests(8*time.Minute+40*time.Second, 10*time.Minute, 20)},
		{name: "errors while two faults are active stay out of the single-fault bound", check: "error-rate-and-latency.e3.error_rate", passed: true, mutate: func(in *InvariantInputs) {
			addOverlappingFault(in)
			fail(6*time.Minute+30*time.Second, 6*time.Minute+50*time.Second, 5, 502)(in)
		}},
		{name: "errors in the fault's own time count", check: "error-rate-and-latency.e3.error_rate", passed: false, mutate: func(in *InvariantInputs) {
			addOverlappingFault(in)
			fail(6*time.Minute, 6*time.Minute+30*time.Second, 5, 502)(in)
		}},
		{name: "requests cut by a gateway restart stay out of the fault they started in", check: "error-rate-and-latency.e3.error_rate", passed: true, mutate: func(in *InvariantInputs) {
			in.Events = append(in.Events, EventRecord{ID: "e6", Case: "gateway-killed-mid-race", Action: "gateway_restart", Target: "devshardctl", StartedAt: atPtr(6*time.Minute + 40*time.Second), EndedAt: atPtr(6*time.Minute + 42*time.Second)})
			for i := range in.Requests {
				offset := in.Requests[i].StartedAt.Sub(fixtureT0)
				if offset >= 6*time.Minute+30*time.Second && offset < 6*time.Minute+40*time.Second {
					in.Requests[i].Duration, in.Requests[i].Outcome, in.Requests[i].Status = 15*time.Second, "error", 0
				}
			}
		}},
		{name: "an earlier fault's grace tail does not shield an active fault", check: "error-rate-and-latency.e3.error_rate", passed: false, mutate: fail(6*time.Minute, 6*time.Minute+45*time.Second, 5, 502)},
		{name: "pass quiet errors", check: "error-rate-and-latency.quiet_error_rate", passed: true},
		{name: "quiet errors", check: "error-rate-and-latency.quiet_error_rate", passed: false, mutate: fail(8*time.Minute+40*time.Second, 10*time.Minute, 20, 500)},
		{name: "pass latency", check: "error-rate-and-latency.quiet_latency", passed: true},
		{name: "slow quiet", check: "error-rate-and-latency.quiet_latency", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.Requests {
				if in.Requests[i].StartedAt.After(at(9 * time.Minute)) {
					in.Requests[i].Duration = time.Second
				}
			}
		}},
		{name: "empty stream in quiet time", check: "error-rate-and-latency.quiet_empty_streams", passed: false, mutate: func(in *InvariantInputs) {
			in.Requests[590].StreamOutcome, in.Requests[590].Outcome = streamEmpty, "error"
		}},
		{name: "pass fault window", check: "error-rate-and-latency.e1.error_rate", passed: true},
		{name: "fault leaks to users", check: "error-rate-and-latency.e1.error_rate", passed: false, mutate: fail(3*time.Minute, 4*time.Minute, 5, 502)},
		{name: "epoch window non-retryable errors", check: "error-rate-and-latency.e4.error_rate", passed: false, mutate: func(in *InvariantInputs) {
			in.Events = append(in.Events, EventRecord{ID: "e4", Action: "epoch_advance", StartedAt: atPtr(8 * time.Minute), EndedAt: atPtr(8*time.Minute + 30*time.Second)})
			fail(8*time.Minute, 8*time.Minute+30*time.Second, 3, 500)(in)
		}},
		{name: "epoch window retryable errors", check: "error-rate-and-latency.e4.error_rate", passed: true, mutate: func(in *InvariantInputs) {
			in.Events = append(in.Events, EventRecord{ID: "e4", Action: "epoch_advance", StartedAt: atPtr(8 * time.Minute), EndedAt: atPtr(8*time.Minute + 30*time.Second)})
			fail(8*time.Minute, 8*time.Minute+30*time.Second, 3, 503)(in)
		}},
		{name: "no baseline", check: "error-rate-and-latency.quiet_latency", passed: false, mutate: func(in *InvariantInputs) {
			in.Requests = in.Requests[150:]
		}},
	})
}

// Test flow:
// 1. Restart the gateway inside the empty-stream window (e1); it is back at 3:25 but fails every request until 3:45.
// 2. Require the restart's active window to end at 3:45, the first request start that succeeded, and at its 90 s recovery budget when nothing succeeds; a non-restart event still ends at its EndedAt.
// 3. Require all 25 outage errors (3:20-3:45) in the e1+e6 overlap row, none in e1's own row, and e1's error check to pass.
func TestInvariantRestartWindowEndsWhenTheGatewayServesAgain(t *testing.T) {
	restart := EventRecord{ID: "e6", Case: "gateway-restart-while-participant-absent", Action: "gateway_restart", Target: "devshardctl", StartedAt: atPtr(3*time.Minute + 20*time.Second), EndedAt: atPtr(3*time.Minute + 25*time.Second)}
	in := passingInputs()
	in.Events = append(in.Events, restart)
	failRequests(3*time.Minute+20*time.Second, 3*time.Minute+45*time.Second, 1, 502)(&in)
	stuck := passingInputs()
	stuck.Events = append(stuck.Events, restart)
	failRequests(3*time.Minute+20*time.Second, 5*time.Minute, 1, 502)(&stuck)

	require.Equal(t, at(3*time.Minute+45*time.Second), newInvariantContext(in).activeEnd(restart))
	require.Equal(t, at(3*time.Minute+25*time.Second+restartRecoveryLim), newInvariantContext(stuck).activeEnd(restart))
	require.Equal(t, at(4*time.Minute), newInvariantContext(in).activeEnd(in.Events[0]))

	checks, report := evaluateInvariants(in)
	rows := map[string]requestWindowStats{}
	for _, row := range report.Tables["error-rate-and-latency"].([]requestWindowStats) {
		rows[row.Window] = row
	}
	require.Equal(t, 25, rows["e1+e6"].Errors)
	require.Zero(t, rows["e1"].Errors)
	require.True(t, checksNamed(t, checks, "error-rate-and-latency.e1.error_rate")[0].Passed)
}

// Test flow:
// 1. Add a fault active inside the challenge window, then evaluate.
// 2. Require the overlap to get its own report row and the challenge row to hold only its own time.
func TestInvariantReportsOverlappingFaultsApart(t *testing.T) {
	in := passingInputs()
	addOverlappingFault(&in)

	_, report := evaluateInvariants(in)

	rows := map[string]requestWindowStats{}
	for _, row := range report.Tables["error-rate-and-latency"].([]requestWindowStats) {
		rows[row.Window] = row
	}
	require.Equal(t, 21+85, rows["e3+e5"].Requests)
	require.Equal(t, 30+10+10, rows["e3"].Requests)
}

// Test flow:
// 1. Add a gateway restart inside the challenge window and a fault that cuts the empty-stream recovery short.
// 2. Require the restart's error check and the cut recovery to be NOT_JUDGED, not PASS.
func TestInvariantsMarkWhatTheRunCouldNotJudge(t *testing.T) {
	in := passingInputs()
	in.Events = append(in.Events,
		EventRecord{ID: "e6", Case: "gateway-killed-mid-race", Action: "gateway_restart", Target: "devshardctl", StartedAt: atPtr(6*time.Minute + 40*time.Second), EndedAt: atPtr(6*time.Minute + 42*time.Second)},
		EventRecord{ID: "e7", Case: "host-hangs-after-first-token", Action: "ml_fault", Target: "n2", StartedAt: atPtr(4*time.Minute + 20*time.Second), EndedAt: atPtr(4*time.Minute + 50*time.Second)},
	)
	failRequests(4*time.Minute, 4*time.Minute+20*time.Second, 10, 502)(&in)

	checks, _ := evaluateInvariants(in)

	restart := checksNamed(t, checks, "error-rate-and-latency.e6.error_rate")[0]
	require.Equal(t, statusNotJudged, restart.Status, "actual=%s details=%s", restart.Actual, restart.Details)
	require.True(t, restart.Passed)
	recovery := checksNamed(t, checks, "recovery-after-fault.e1.recovery")[0]
	require.Equal(t, statusNotJudged, recovery.Status, "actual=%s", recovery.Actual)
	require.True(t, recovery.Passed)
}

// Test flow:
// 1. Keep errors for two minutes after the empty-stream window ends.
// 2. Drop every request in the first bucket after the window, then keep errors.
// 3. Require recovery-after-fault for that event to fail, a bucket of drops not to count as recovered, and a pass when errors stop at once.
func TestInvariantRecovery(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "recovery-after-fault.e1.recovery", passed: true},
		{name: "a bucket of generator drops is not a recovery", check: "recovery-after-fault.e1.recovery", passed: false, mutate: func(in *InvariantInputs) {
			dropRequests(4*time.Minute, 4*time.Minute+30*time.Second, 1)(in)
			failRequests(4*time.Minute+30*time.Second, 6*time.Minute, 10, 502)(in)
		}},
		{name: "slow recovery", check: "recovery-after-fault.e1.recovery", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.Requests {
				offset := in.Requests[i].StartedAt.Sub(fixtureT0)
				if offset >= 4*time.Minute && offset < 6*time.Minute && i%10 == 0 {
					in.Requests[i].Outcome = "error"
				}
			}
		}},
		{name: "a later fault cuts the recovery window short", check: "recovery-after-fault.e1.recovery", passed: true, mutate: func(in *InvariantInputs) {
			in.Events = append(in.Events, EventRecord{ID: "e7", Case: "host-hangs-after-first-token", Action: "ml_fault", Target: "n2", StartedAt: atPtr(4*time.Minute + 20*time.Second), EndedAt: atPtr(4*time.Minute + 50*time.Second)})
			failRequests(4*time.Minute, 6*time.Minute, 10, 502)(in)
		}},
		{name: "restart recovering past its budget", check: "recovery-after-fault.e2.recovery", passed: false, mutate: func(in *InvariantInputs) {
			in.Events[2].StartedAt, in.Events[2].EndedAt = atPtr(8*time.Minute), atPtr(9*time.Minute)
			failRequests(5*time.Minute+10*time.Second, 7*time.Minute, 10, 502)(in)
		}},
		{name: "restart has a longer budget", check: "recovery-after-fault.e2.recovery", passed: true, mutate: func(in *InvariantInputs) {
			in.Events[2].StartedAt, in.Events[2].EndedAt = atPtr(8*time.Minute), atPtr(9*time.Minute)
			for i := range in.Requests {
				offset := in.Requests[i].StartedAt.Sub(fixtureT0)
				if offset >= 5*time.Minute+10*time.Second && offset < 6*time.Minute+10*time.Second && i%10 == 0 {
					in.Requests[i].Outcome = "error"
				}
			}
		}},
	})
}

// Test flow:
// 1. Grow host goroutines, grow host heap linearly, and restart a host that no event targets.
// 2. Require the matching no-restarts-or-resource-growth check to fail and the flat fixture to pass.
func TestInvariantTrends(t *testing.T) {
	hostSeries := func(f func(offset time.Duration, s *MetricsSample)) func(*InvariantInputs) {
		return func(in *InvariantInputs) {
			for i := range in.Metrics {
				if in.Metrics[i].Target == "versiond-2/devshardd" {
					f(in.Metrics[i].At.Sub(fixtureT0), &in.Metrics[i])
				}
			}
		}
	}
	runInvariantCases(t, []invariantCase{
		{name: "pass goroutines", check: "no-restarts-or-resource-growth.goroutines", passed: true},
		{name: "goroutine growth", check: "no-restarts-or-resource-growth.goroutines", passed: false, mutate: hostSeries(func(offset time.Duration, s *MetricsSample) {
			s.Values["go_goroutines"] = 200 + offset.Seconds()
		})},
		{name: "pass heap", check: "no-restarts-or-resource-growth.heap_slope", passed: true},
		{name: "heap grows 5 MiB per minute", check: "no-restarts-or-resource-growth.heap_slope", passed: false, mutate: hostSeries(func(offset time.Duration, s *MetricsSample) {
			s.Values["go_memstats_heap_inuse_bytes"] = float64(80<<20) + offset.Minutes()*float64(5<<20)
		})},
		{name: "heap step stays flat", check: "no-restarts-or-resource-growth.heap_slope", passed: true, mutate: hostSeries(func(offset time.Duration, s *MetricsSample) {
			if offset > 9*time.Minute+30*time.Second {
				s.Values["go_memstats_heap_inuse_bytes"] = float64(400 << 20)
			}
		})},
		{name: "pass restarts", check: "no-restarts-or-resource-growth.restarts", passed: true},
		{name: "unexpected host restart", check: "no-restarts-or-resource-growth.restarts", passed: false, mutate: hostSeries(func(offset time.Duration, s *MetricsSample) {
			if offset > 7*time.Minute {
				s.Values["loadtest_process_pid"], s.Values["loadtest_process_start_ticks"] = 9, 9
			}
		})},
	})
}

// Test flow:
// 1. Fill the gateway chat cache at 30 MiB a minute up to its 256 MiB cap, with heap in use rising by the same bytes.
// 2. Require heap_slope to pass and the gateway row to report the heap slope with the cache subtracted.
// 3. Drop the cache series from every gateway sample.
// 4. Require heap_slope to fail and the gateway row to note that the cache was not subtracted.
func TestInvariantHeapSlopeLeavesOutTheGatewayCache(t *testing.T) {
	const cacheKey = "devshard_gateway_cache_bytes{}"
	filling := func() InvariantInputs {
		in := passingInputs()
		for i := range in.Metrics {
			s := &in.Metrics[i]
			if s.Target != "gateway" {
				continue
			}
			cache := min(s.At.Sub(fixtureT0).Minutes()*float64(30<<20), float64(256<<20))
			s.Values["go_memstats_heap_inuse_bytes"] += cache
			if s.Domain == nil {
				s.Domain = map[string]float64{}
			}
			s.Domain[cacheKey] = cache
		}
		return in
	}
	gatewayRow := func(t *testing.T, report InvariantReport) map[string]any {
		t.Helper()
		body, err := json.Marshal(report.Tables["no-restarts-or-resource-growth"])
		require.NoError(t, err)
		var rows []map[string]any
		require.NoError(t, json.Unmarshal(body, &rows))
		for _, row := range rows {
			if row["target"] == "gateway" {
				return row
			}
		}
		require.FailNow(t, "no gateway row")
		return nil
	}

	checks, report := evaluateInvariants(filling())
	check := checksNamed(t, checks, "no-restarts-or-resource-growth.heap_slope")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
	row := gatewayRow(t, report)
	require.Equal(t, true, row["heap_minus_cache"])
	require.InDelta(t, 0, row["heap_inuse_slope_bytes_per_min"], 1)

	in := filling()
	for i := range in.Metrics {
		delete(in.Metrics[i].Domain, cacheKey)
	}
	checks, report = evaluateInvariants(in)
	check = checksNamed(t, checks, "no-restarts-or-resource-growth.heap_slope")[0]
	require.False(t, check.Passed)
	require.Contains(t, check.Details, "gateway heap in-use")
	row = gatewayRow(t, report)
	require.Nil(t, row["heap_minus_cache"])
	require.Contains(t, row["note"], "devshard_gateway_cache_bytes missing")
}

// Test flow:
// 1. Leave one event unapplied, strip the ML fault's strikes and transitions, and keep the gateway PID through its restart.
// 2. Require the faults-fired-and-ml-nodes-bound check of that event to fail and to carry no case.
func TestInvariantFaultFired(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass ml fault", check: "faults-fired-and-ml-nodes-bound.e1.fired", passed: true},
		{name: "ml fault left no trace", check: "faults-fired-and-ml-nodes-bound.e1.fired", passed: false, mutate: func(in *InvariantInputs) {
			in.Throttle = nil
			for i := range in.Metrics {
				in.Metrics[i].Domain = nil
			}
		}},
		{name: "pass restart", check: "faults-fired-and-ml-nodes-bound.e2.fired", passed: true},
		{name: "restart did not restart", check: "faults-fired-and-ml-nodes-bound.e2.fired", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.Metrics {
				if in.Metrics[i].Target == "gateway" {
					in.Metrics[i].Values["loadtest_process_pid"], in.Metrics[i].Values["loadtest_process_start_ticks"] = 1, 1
				}
			}
		}},
		{name: "ml fault on an unbound node", check: "faults-fired-and-ml-nodes-bound.e1.fired", passed: false, mutate: func(in *InvariantInputs) {
			in.NodeHosts = map[string]string{}
		}},
		{name: "challenge without mock-dapi data", check: "faults-fired-and-ml-nodes-bound.e3.fired", passed: false, mutate: func(in *InvariantInputs) {
			in.DapiHosts = nil
		}},
		{name: "challenge without rejections", check: "faults-fired-and-ml-nodes-bound.e3.fired", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.DapiHosts {
				in.DapiHosts[i].Hosts["versiond-4"] = DapiHostRow{}
			}
		}},
		{name: "faulted node also served another host", check: "faults-fired-and-ml-nodes-bound.e1.fired", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.DapiHosts {
				in.DapiHosts[i].Hosts[dapiUnmappedHost] = DapiHostRow{Allocations: uint64(i), NodeAllocations: map[string]uint64{"n2": uint64(i)}}
			}
		}},
		{name: "faulted node idle in its window", check: "faults-fired-and-ml-nodes-bound.e1.fired", passed: false, mutate: func(in *InvariantInputs) {
			for i := range in.DapiHosts {
				in.DapiHosts[i].Hosts["versiond-3"] = DapiHostRow{Allocations: 1, NodeAllocations: map[string]uint64{"n2": 1}}
			}
		}},
		{name: "never applied", check: "faults-fired-and-ml-nodes-bound.e3.fired", passed: false, mutate: func(in *InvariantInputs) {
			in.Events[2].StartedAt, in.Events[2].EndedAt, in.Events[2].Error = nil, nil, "POST /testenv/challenge returned 404"
		}},
	})
	in := passingInputs()
	in.Events[2].StartedAt = nil
	checks, _ := evaluateInvariants(in)
	check := checksNamed(t, checks, "faults-fired-and-ml-nodes-bound.e3.fired")[0]
	require.Empty(t, check.Case)
	require.True(t, strings.Contains(check.Details, "host-under-poc-challenge"))
}

// Test flow:
// 1. Report a skipped lease for an honest host and one for an unknown instance.
// 2. Fail the lease query.
// 3. Require no-skipped-validation-leases to fail on each and to stay silent when Postgres is not used.
func TestInvariantSkippedLeases(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass faulty host skipped", check: "no-skipped-validation-leases.no_skipped_leases", passed: true},
		{name: "honest host skipped", check: "no-skipped-validation-leases.no_skipped_leases", passed: false, mutate: func(in *InvariantInputs) {
			in.Leases = append(in.Leases, LeaseCount{"p1", "skipped", 2})
		}},
		{name: "unknown instance skipped", check: "no-skipped-validation-leases.no_skipped_leases", passed: false, mutate: func(in *InvariantInputs) {
			in.Leases = append(in.Leases, LeaseCount{"stranger", "skipped", 1})
		}},
		{name: "query failed", check: "no-skipped-validation-leases.no_skipped_leases", passed: false, mutate: func(in *InvariantInputs) {
			in.Leases, in.LeasesError = nil, "psql: connection refused"
		}},
	})
	in := passingInputs()
	in.Leases = nil
	checks, _ := evaluateInvariants(in)
	for _, check := range checks {
		require.NotEqual(t, "invariants.no-skipped-validation-leases.no_skipped_leases", check.Name)
	}
}

// Test flow:
// 1. Add an epoch-advance blocking window and start one inference inside it.
// 2. Require no-admission-during-poc to fail, and to pass for an inference on the window edge or within one phase poll of the start.
func TestInvariantNoAdmissionWhileBlocked(t *testing.T) {
	epoch := func(in *InvariantInputs) {
		in.Events = append(in.Events, EventRecord{ID: "e4", Case: "epoch-change-with-poc", Action: "epoch_advance", StartedAt: atPtr(8 * time.Minute), EndedAt: atPtr(9 * time.Minute)})
	}
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "no-admission-during-poc.e4.no_admission", passed: true, mutate: func(in *InvariantInputs) {
			epoch(in)
			in.Inferences["11"]["2"] = DebugInference{Status: "finished", StartedAt: at(8 * time.Minute).Unix()}
		}},
		{name: "admitted before the gateway polls the phase", check: "no-admission-during-poc.e4.no_admission", passed: true, mutate: func(in *InvariantInputs) {
			epoch(in)
			in.Inferences["11"]["2"] = DebugInference{Status: "finished", StartedAt: at(8*time.Minute + 5*time.Second).Unix()}
		}},
		{name: "admitted while blocked", check: "no-admission-during-poc.e4.no_admission", passed: false, mutate: func(in *InvariantInputs) {
			epoch(in)
			in.Inferences["11"]["2"] = DebugInference{Status: "finished", StartedAt: at(8*time.Minute + 30*time.Second).Unix()}
		}},
		{name: "inferences unavailable", check: "no-admission-during-poc.e4.no_admission", passed: false, mutate: func(in *InvariantInputs) {
			epoch(in)
			in.Inferences = nil
		}},
	})
}

// Test flow:
// 1. Count 'did not compress' lines, and mark compose.log unreadable.
// 2. Require responses-compress to fail on both and pass on zero.
func TestInvariantResponsesCompress(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "responses-compress.responses_compress", passed: true},
		{name: "uncompressed responses", check: "responses-compress.responses_compress", passed: false, mutate: func(in *InvariantInputs) { in.DidNotCompress = 3 }},
		{name: "log unreadable", check: "responses-compress.responses_compress", passed: false, mutate: func(in *InvariantInputs) { in.DidNotCompress = -1 }},
	})
}

// Test flow:
// 1. Feed a no-runtime-for-deactivated-escrow failure tied to a known_bug case through markKnownBugs.
// 2. Require XFAIL, and require a faults-fired-and-ml-nodes-bound failure of the same case to stay failed.
func TestInvariantKnownBugMarking(t *testing.T) {
	in := passingInputs()
	in.Devshards[2].Resident = true
	in.Events[2].StartedAt = nil
	checks, _ := evaluateInvariants(in)
	marked := markKnownBugs(checks, []Case{{ID: "escrow-rotation-per-model-settlement", KnownBug: "#1988"}, {ID: "host-under-poc-challenge", KnownBug: "#1823"}}, false)
	require.Equal(t, "XFAIL", checksNamed(t, marked, "no-runtime-for-deactivated-escrow.no_resident_deactivated")[0].Status)
	require.False(t, checksNamed(t, marked, "faults-fired-and-ml-nodes-bound.e3.fired")[0].Passed)
}

// Test flow:
// 1. Keep the passing fixture, where mock-dapi maps every allocation to a host.
// 2. Add unmapped allocations after warm-up, only before warm-up, and drop every sample.
// 3. Require faults-fired-and-ml-nodes-bound.host_binding to fail on post-warm-up unmapped allocations and missing data, carry no case, and pass otherwise.
// 4. Require bindingProblems from start to flag a run where every allocation is unmapped.
func TestInvariantHostBinding(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "faults-fired-and-ml-nodes-bound.host_binding", passed: true},
		{name: "unmapped after warm-up", check: "faults-fired-and-ml-nodes-bound.host_binding", passed: false, mutate: func(in *InvariantInputs) {
			last := len(in.DapiHosts) - 1
			in.DapiHosts[last].Hosts[dapiUnmappedHost] = DapiHostRow{Allocations: 5}
		}},
		{name: "unmapped only before warm-up", check: "faults-fired-and-ml-nodes-bound.host_binding", passed: true, mutate: func(in *InvariantInputs) {
			for i := range in.DapiHosts {
				in.DapiHosts[i].Hosts[dapiUnmappedHost] = DapiHostRow{Allocations: 3}
			}
		}},
		{name: "no mock-dapi data", check: "faults-fired-and-ml-nodes-bound.host_binding", passed: false, mutate: func(in *InvariantInputs) {
			in.DapiHosts = []DapiHostSample{{At: at(time.Minute), Error: "connection refused"}}
		}},
	})
	in := passingInputs()
	in.DapiHosts[len(in.DapiHosts)-1].Hosts[dapiUnmappedHost] = DapiHostRow{Allocations: 5}
	checks, _ := evaluateInvariants(in)
	check := checksNamed(t, checks, "faults-fired-and-ml-nodes-bound.host_binding")[0]
	require.Empty(t, check.Case)
	require.Contains(t, check.Details, "5 unmapped allocations")

	allUnmapped := DapiHostSample{Hosts: map[string]DapiHostRow{dapiUnmappedHost: {Allocations: 26337}, "versiond-0": {}}}
	require.Equal(t, []string{"26337 unmapped allocations", "no bound host acquired a node"}, bindingProblems(nil, allUnmapped, map[string]bool{"versiond-0": true}))
}

// Test flow:
// 1. Build 5 s tick series: regular, with a 20 s gap, with a gap of exactly 3x, and with a gap inside an injected pause.
// 2. Require a stall only for the 20 s gap outside the pause, with the median period.
// 3. Build 1 s scrape series with a 3.4 s gap and with a 15 s gap; require a stall only for the 15 s gap.
// 4. Require the invalid windows to extend each stall by the grace and merge overlaps, and the invalid fraction to match.
func TestHeartbeatStalls(t *testing.T) {
	series := func(gapAt, gap time.Duration) []time.Time {
		var ticks []time.Time
		for offset := time.Duration(0); offset <= 2*time.Minute; offset += 5 * time.Second {
			if offset > gapAt {
				offset += gap - 5*time.Second
				gapAt = 1 << 62
			}
			ticks = append(ticks, at(offset))
		}
		return ticks
	}
	require.Empty(t, heartbeatStalls("regular", series(1<<62, 0), nil))
	require.Empty(t, heartbeatStalls("exactly 3x", series(time.Minute, 15*time.Second), nil))
	stalls := heartbeatStalls("versiond-3 reconcile tick", series(time.Minute, 20*time.Second), nil)
	require.Len(t, stalls, 1)
	require.Equal(t, at(time.Minute), stalls[0].From)
	require.Equal(t, at(time.Minute+20*time.Second), stalls[0].To)
	require.Equal(t, "5s", stalls[0].Period)
	require.Empty(t, heartbeatStalls("paused", series(time.Minute, 20*time.Second), []timeSpan{{From: at(time.Minute + 2*time.Second), To: at(time.Minute + 12*time.Second)}}))

	scrapes := func(gap time.Duration) []time.Time {
		var ticks []time.Time
		for offset := time.Duration(0); offset <= time.Minute; offset += time.Second {
			ticks = append(ticks, at(offset))
		}
		for i := 31; i < len(ticks); i++ {
			ticks[i] = ticks[i].Add(gap - time.Second)
		}
		return ticks
	}
	require.Empty(t, heartbeatStalls("gateway metrics scrape", scrapes(3400*time.Millisecond), nil))
	require.Len(t, heartbeatStalls("gateway metrics scrape", scrapes(15*time.Second), nil), 1)

	windows := invalidWindows([]harnessStall{
		{From: at(time.Minute), To: at(time.Minute + 20*time.Second)},
		{From: at(time.Minute + 30*time.Second), To: at(time.Minute + 40*time.Second)},
		{From: at(5 * time.Minute), To: at(5*time.Minute + 10*time.Second)},
	}, 30*time.Second)
	require.Equal(t, []timeSpan{
		{From: at(time.Minute), To: at(time.Minute + 70*time.Second)},
		{From: at(5 * time.Minute), To: at(5*time.Minute + 40*time.Second)},
	}, windows)
	require.InDelta(t, 110.0/600.0, invalidFraction(windows, at(0), at(10*time.Minute)), 1e-9)
}

// Test flow:
// 1. Parse a text versiond reconcile line, a JSON one, and an unrelated line.
// 2. Require the service without its replica suffix and the UTC time, and no tick for the unrelated line.
func TestParseReconcileTick(t *testing.T) {
	service, when, ok := parseReconcileTick("versiond-1-1         | 2026/10/13 06:10:29 INFO reconcile desired versions resolved oracle_versions=[v2]")
	require.True(t, ok)
	require.Equal(t, "versiond-1", service)
	require.Equal(t, time.Date(2026, 10, 13, 6, 10, 29, 0, time.UTC), when)
	service, when, ok = parseReconcileTick(`versiond-5-1  | {"time":"2026-10-13T06:21:30.5Z","level":"INFO","msg":"reconcile desired versions resolved"}`)
	require.True(t, ok)
	require.Equal(t, "versiond-5", service)
	require.Equal(t, time.Date(2026, 10, 13, 6, 21, 30, 5e8, time.UTC), when)
	_, _, ok = parseReconcileTick("devshardctl-1 | 2026/10/13 06:10:29 INFO served chat")
	require.False(t, ok)
}

// Test flow:
// 1. Add a 5 s versiond-2 reconcile ticker that stalls 7:00-7:30, and stop the gateway scrapes over the same time.
// 2. Charge honest p1 one miss inside the stall, then also one outside it.
// 3. Require both stalls in harness_health, a graced invalid window, the host-under-poc-challenge window and its checks INVALID, and the receipt-then-empty-stream checks still PASS.
// 4. Require misses-only-in-fault-windows.honest_misses_gateway INVALID for the miss inside the stall, FAIL once a miss lies outside it, and INVALID to survive markKnownBugs.
func TestInvariantHarnessStallMarksInvalid(t *testing.T) {
	stalled := func() InvariantInputs {
		in := passingInputs()
		in.ReconcileTicks = map[string][]time.Time{}
		for offset := time.Duration(0); offset <= in.Workload; offset += 5 * time.Second {
			if offset > 7*time.Minute && offset < 7*time.Minute+30*time.Second {
				continue
			}
			in.ReconcileTicks["versiond-2"] = append(in.ReconcileTicks["versiond-2"], at(offset))
		}
		var metrics []MetricsSample
		for _, sample := range in.Metrics {
			if sample.Target == "gateway" && sample.At.After(at(7*time.Minute)) && sample.At.Before(at(7*time.Minute+40*time.Second)) {
				continue
			}
			metrics = append(metrics, sample)
		}
		in.Metrics = metrics
		in.Accounting[3] = accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 4, "p1": 1})
		return in
	}

	checks, report := evaluateInvariants(stalled())
	require.Len(t, report.Health.Stalls, 2)
	require.Equal(t, "versiond-2 reconcile tick", report.Health.Stalls[0].Source)
	require.Equal(t, "gateway metrics scrape", report.Health.Stalls[1].Source)
	require.Equal(t, []timeSpan{{From: at(7 * time.Minute), To: at(7*time.Minute + 40*time.Second + 95*time.Second)}}, report.Health.InvalidWindows)
	require.InDelta(t, 135.0/600.0, report.Health.InvalidFraction, 1e-9)
	for _, w := range report.Faulty {
		require.Equal(t, w.Event == "e3", w.Invalid, w.Event)
	}
	for _, name := range []string{"faults-fired-and-ml-nodes-bound.e3.fired", "error-rate-and-latency.e3.error_rate", "misses-only-in-fault-windows.honest_misses_gateway"} {
		check := checksNamed(t, checks, name)[0]
		require.Equal(t, statusInvalid, check.Status, name)
		require.False(t, check.Passed, name)
		require.Contains(t, report.Health.InvalidChecks, check.Name)
	}
	for _, name := range []string{"faults-fired-and-ml-nodes-bound.e1.fired", "error-rate-and-latency.e1.error_rate", "faults-fired-and-ml-nodes-bound.host_binding"} {
		check := checksNamed(t, checks, name)[0]
		require.Empty(t, check.Status, name)
		require.True(t, check.Passed, name)
	}
	marked := markKnownBugs(checks, []Case{{ID: "host-under-poc-challenge", KnownBug: "#1823"}}, false)
	require.Equal(t, statusInvalid, checksNamed(t, marked, "error-rate-and-latency.e3.error_rate")[0].Status)
	require.Contains(t, formatAssertionFailure(checksNamed(t, marked, "error-rate-and-latency.e3.error_rate")[0]), "INVALID (harness stall)")

	in := stalled()
	in.Accounting[2] = accountingAt(5*time.Minute+35*time.Second, "e1:end+grace", map[string]uint64{"p2": 4, "p1": 1})
	in.Accounting[3] = accountingAt(10*time.Minute, "final", map[string]uint64{"p2": 4, "p1": 2})
	checks, _ = evaluateInvariants(in)
	check := checksNamed(t, checks, "misses-only-in-fault-windows.honest_misses_gateway")[0]
	require.Empty(t, check.Status)
	require.False(t, check.Passed)
}
