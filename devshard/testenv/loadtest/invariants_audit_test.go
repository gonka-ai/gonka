package loadtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/testenv/mockchain/ledgerface"

	"github.com/stretchr/testify/require"
)

const hostRateLimitedKey = `devshard_request_terminal_total{reason="rate_limited",terminal="no_receipt_interrupted"}`

func attemptFailureKey(participant, reason string) string {
	return `devshard_gateway_attempt_failures_total{model="m-a",participant_key="` + participant + `",reason="` + reason + `",role="primary",visibility="hidden"}`
}

// budgetInputs adds a whole-run budget marker on p2 (versiond-3) to the
// passing fixture, with host rate-limit terminals rising through the run.
func budgetInputs() InvariantInputs {
	in := passingInputs()
	in.Settings.Quarantine["sender_rate"] = senderRateQuarantine
	in.Events = append(in.Events, EventRecord{ID: "bg", Case: "host-over-peer-chat-budget", Action: "marker", Target: "versiond-3",
		Params:    map[string]any{"background": true, "waive_misses": true, "expect_quarantine": "sender_rate", "observe": "host_rate_limited"},
		StartedAt: atPtr(0), EndedAt: atPtr(10 * time.Minute)})
	for offset := time.Duration(0); offset < in.Workload; offset += 10 * time.Second {
		host := processSample("versiond-3/devshardd", offset, 9, 200, 80<<20)
		host.Domain = map[string]float64{hostRateLimitedKey: float64(offset / time.Minute)}
		in.Metrics = append(in.Metrics, host)
	}
	return in
}

// Test flow:
// 1. Evaluate no-skipped-validation-leases with versiond-4 excluded as a sqlite host and require a pass that names the exclusion.
// 2. Add a skipped lease for honest p0 and require no-skipped-validation-leases to fail despite the exclusion.
func TestInvariantExcludesSQLiteHosts(t *testing.T) {
	in := passingInputs()
	in.LeaseExcluded = map[string]string{"versiond-4": sqliteLeaseExclusion}
	checks, report := evaluateInvariants(in)
	check := checksNamed(t, checks, "no-skipped-validation-leases.no_skipped_leases")[0]
	require.True(t, check.Passed, check.Details)
	require.Contains(t, check.Expected, "versiond-4 excluded")
	require.Contains(t, report.Tables["no-skipped-validation-leases"], "excluded_hosts")

	in.Leases = append(in.Leases, LeaseCount{"p0", "skipped", 2})
	checks, _ = evaluateInvariants(in)
	require.False(t, checksNamed(t, checks, "no-skipped-validation-leases.no_skipped_leases")[0].Passed)
}

// Test flow:
// 1. Run escrow-accounting-reconciles.final_nonce_le_max on the passing fixture with chain max_nonce 1000.
// 2. Push the observed gateway nonce or the resident nonce past max_nonce and require a failure.
// 3. Drop max_nonce and require the check to be skipped; map the check to a known_bug case and require XFAIL.
func TestInvariantFinalNonceLeMax(t *testing.T) {
	withMax := func(in *InvariantInputs) { in.ChainMaxNonce = 1000 }
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "escrow-accounting-reconciles.final_nonce_le_max", passed: true, mutate: withMax},
		{name: "observed nonce past max", check: "escrow-accounting-reconciles.final_nonce_le_max", passed: false, mutate: func(in *InvariantInputs) {
			withMax(in)
			in.Pending = append(in.Pending, PendingSample{At: at(3 * time.Minute), EscrowID: "10", Nonce: 1006})
		}},
		{name: "resident nonce past max", check: "escrow-accounting-reconciles.final_nonce_le_max", passed: false, mutate: func(in *InvariantInputs) {
			withMax(in)
			in.Devshards[1].Nonce = 1001
		}},
	})

	checks, _ := evaluateInvariants(passingInputs())
	for _, check := range checks {
		require.NotEqual(t, "invariants.escrow-accounting-reconciles.final_nonce_le_max", check.Name)
	}

	in := passingInputs()
	withMax(&in)
	in.Settings.Cases["escrow-accounting-reconciles.final_nonce_le_max"] = "session-nonce-within-max-nonce"
	in.Devshards[1].Nonce = 1001
	checks, _ = evaluateInvariants(in)
	marked := markKnownBugs(checksNamed(t, checks, "escrow-accounting-reconciles.final_nonce_le_max"), []Case{{ID: "session-nonce-within-max-nonce", KnownBug: "nonce-overrun"}}, false)
	require.Equal(t, "XFAIL", marked[0].Status)
}

// Test flow:
// 1. Evaluate the budget fixture and require faults-fired-and-ml-nodes-bound to see host rate-limit terminals for the background marker.
// 2. Flatten the terminal counter and require faults-fired-and-ml-nodes-bound to fail.
// 3. Require the background window to leave the quiet error rate, recovery-after-fault and other windows' misses alone.
func TestInvariantBackgroundBudgetMarkerFires(t *testing.T) {
	checks, _ := evaluateInvariants(budgetInputs())
	fired := checksNamed(t, checks, "faults-fired-and-ml-nodes-bound.bg.fired")[0]
	require.True(t, fired.Passed, fired.Actual)
	require.Contains(t, fired.Actual, "rate_limited")
	require.True(t, checksNamed(t, checks, "error-rate-and-latency.quiet_error_rate")[0].Passed)
	require.True(t, checksNamed(t, checks, "misses-only-in-fault-windows.bg.waived_misses")[0].Passed, "receipt-then-empty-stream misses on p2 belong to e1, not the budget window")
	for _, check := range checks {
		require.NotEqual(t, "invariants.recovery-after-fault.bg.recovery", check.Name)
		require.NotEqual(t, "invariants.error-rate-and-latency.bg.error_rate", check.Name)
	}

	in := budgetInputs()
	for i := range in.Metrics {
		if in.Metrics[i].Target == "versiond-3/devshardd" {
			in.Metrics[i].Domain = map[string]float64{hostRateLimitedKey: 3}
		}
	}
	checks, _ = evaluateInvariants(in)
	require.False(t, checksNamed(t, checks, "faults-fired-and-ml-nodes-bound.bg.fired")[0].Passed)
}

// Test flow:
// 1. Add a sender_rate transition on p2 outside e1 and require quarantines-match-faults.bg.reason and attribution to pass.
// 2. Add an http_throttle transition outside e1 and require quarantines-match-faults.bg.reason to fail.
// 3. Charge p2 a miss outside e1 and require misses-only-in-fault-windows.bg.waived_misses to fail.
func TestInvariantBackgroundBudgetMarkerJudges(t *testing.T) {
	addAt := func(in *InvariantInputs, offset time.Duration, reason string) {
		for i := range in.Metrics {
			s := &in.Metrics[i]
			if s.Target != "gateway" || s.Domain == nil {
				continue
			}
			s.Domain[transitionKey("p2", "probe", reason)] = 0
			if !s.At.Before(at(offset)) {
				s.Domain[transitionKey("p2", "probe", reason)] = 1
			}
		}
	}
	in := budgetInputs()
	addAt(&in, 90*time.Second, "sender_rate_quarantine")
	checks, _ := evaluateInvariants(in)
	require.True(t, checksNamed(t, checks, "quarantines-match-faults.bg.reason")[0].Passed, checksNamed(t, checks, "quarantines-match-faults.bg.reason")[0].Details)
	require.True(t, checksNamed(t, checks, "quarantines-match-faults.quarantine_attributed")[0].Passed)

	in = budgetInputs()
	addAt(&in, 90*time.Second, "http_throttle_quarantine")
	checks, _ = evaluateInvariants(in)
	require.False(t, checksNamed(t, checks, "quarantines-match-faults.bg.reason")[0].Passed)

	in = budgetInputs()
	in.Accounting = append(in.Accounting[:1], append([]AccountingSnapshot{accountingAt(2*time.Minute, "mid", map[string]uint64{"p2": 1})}, in.Accounting[1:]...)...)
	for i := 2; i < len(in.Accounting); i++ {
		in.Accounting[i] = accountingAt(in.Accounting[i].At.Sub(fixtureT0), in.Accounting[i].Label, map[string]uint64{"p2": 5})
	}
	checks, _ = evaluateInvariants(in)
	require.False(t, checksNamed(t, checks, "misses-only-in-fault-windows.bg.waived_misses")[0].Passed)
}

// Test flow:
// 1. Evaluate no-http-5xx-on-honest-hosts on the passing fixture and require a pass.
// 2. Raise http_error attempt failures for honest p1 in a quiet minute and require a failure.
// 3. Require http_5xx inside the global restart window, on faulty p2, or http_429 on p1 to pass.
func TestInvariantHonestHTTP5xx(t *testing.T) {
	raise := func(participant, reason string, offset time.Duration) func(*InvariantInputs) {
		return func(in *InvariantInputs) {
			for i := range in.Metrics {
				s := &in.Metrics[i]
				if s.Target != "gateway" {
					continue
				}
				if s.Domain == nil {
					s.Domain = map[string]float64{}
				}
				s.Domain[attemptFailureKey(participant, reason)] = 0
				if !s.At.Before(at(offset)) {
					s.Domain[attemptFailureKey(participant, reason)] = 2
				}
			}
		}
	}
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: true},
		{name: "honest 500 in quiet time", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: false, mutate: raise("p1", "http_error", 2*time.Minute)},
		{name: "honest 503 in quiet time", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: false, mutate: raise("p1", "http_503", 8*time.Minute)},
		{name: "honest 503 in the restart window", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: true, mutate: raise("p1", "http_503", 5*time.Minute+30*time.Second)},
		{name: "faulty participant", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: true, mutate: raise("p2", "http_error", 2*time.Minute)},
		{name: "honest 429", check: "no-http-5xx-on-honest-hosts.honest_no_http_5xx", passed: true, mutate: raise("p1", "http_429", 2*time.Minute)},
	})
}

// Test flow:
// 1. Evaluate no-escrow-falsely-marked-missing with no "escrow missing on chain" lines and require a pass.
// 2. Mark escrow 11, which mock-chain still lists, inactive as missing and require a failure.
// 3. Mark an escrow mock-chain does not list and require a pass; drop the log and require unavailable.
func TestInvariantEscrowMissingOnChain(t *testing.T) {
	runInvariantCases(t, []invariantCase{
		{name: "pass", check: "no-escrow-falsely-marked-missing.no_false_escrow_missing", passed: true, mutate: func(in *InvariantInputs) { in.EscrowMissing = []string{} }},
		{name: "escrow still on chain", check: "no-escrow-falsely-marked-missing.no_false_escrow_missing", passed: false, mutate: func(in *InvariantInputs) { in.EscrowMissing = []string{"11"} }},
		{name: "escrow really missing", check: "no-escrow-falsely-marked-missing.no_false_escrow_missing", passed: true, mutate: func(in *InvariantInputs) { in.EscrowMissing = []string{"99"} }},
		{name: "chain unavailable", check: "no-escrow-falsely-marked-missing.no_false_escrow_missing", passed: false, mutate: func(in *InvariantInputs) {
			in.EscrowMissing = []string{"11"}
			in.ChainError = "connection refused"
		}},
	})
	checks, _ := evaluateInvariants(passingInputs())
	for _, check := range checks {
		require.NotEqual(t, "invariants.no-escrow-falsely-marked-missing.no_false_escrow_missing", check.Name)
	}
}

// Test flow:
// 1. Write a Compose log with two gateway "escrow missing on chain" lines, one other skip reason and noise.
// 2. Require readEscrowMissing to return exactly the two escrow IDs in order.
func TestReadEscrowMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "compose.log")
	body := strings.Join([]string{
		"devshardctl-1  | 2026/10/13 09:21:40 devshard 15 escrow missing on chain, marking inactive and skipping runtime: escrow not found",
		"devshardctl-1  | 2026/10/13 09:21:40 devshard 16 escrow settled on chain, marking inactive and skipping runtime: settled",
		"versiond-3-1   | INFO reconcile desired versions",
		"devshardctl-1  | 2026/10/13 09:21:41 devshard 17 escrow missing on chain, marking inactive and skipping runtime: escrow not found",
	}, "\n")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	ids, err := readEscrowMissing(path)
	require.NoError(t, err)
	require.Equal(t, []string{"15", "17"}, ids)
}

// Test flow:
// 1. Keep a mock-chain escrow table with escrow 11 and chain max_nonce in the fixture.
// 2. Require the escrow-accounting-reconciles table rows to carry the final nonce the check compared.
func TestInvariantFinalNonceTable(t *testing.T) {
	in := passingInputs()
	in.ChainMaxNonce = 1000
	in.Escrows = append(in.Escrows, ledgerface.EscrowView{ID: 13, ModelID: "m-a", Amount: 1000})
	_, report := evaluateInvariants(in)
	rows, ok := report.Tables["escrow-accounting-reconciles"].([]escrowReconciliation)
	require.True(t, ok)
	finals := map[string]uint64{}
	for _, row := range rows {
		finals[row.EscrowID] = row.FinalNonce
	}
	require.Equal(t, uint64(40), finals["10"])
	require.Equal(t, uint64(590), finals["11"], "the highest of resident, settled and observed nonces")
}

// Test flow:
// 1. Run the whole-run budget marker on p2, which expects sender_rate quarantines, and let the gateway quarantine p2 for http at 1 min, outside any other fault, and again at 3m40s inside p2's empty-stream fault.
// 2. Require the empty-stream fault's reason check to pass, since the marker alone produced http before, and the marker's reason check to name both http transitions.
func TestInvariantQuarantineReasonFollowsTheBackgroundWindowThatProducesIt(t *testing.T) {
	in := budgetInputs()
	key := transitionKey("p2", "shadow", "http_throttle_quarantine")
	for i := range in.Metrics {
		s := &in.Metrics[i]
		if s.Target != "gateway" || s.Domain == nil {
			continue
		}
		offset := s.At.Sub(fixtureT0)
		s.Domain[key] = 0
		if offset >= time.Minute {
			s.Domain[key] = 1
		}
		if offset >= 3*time.Minute+40*time.Second {
			s.Domain[key] = 2
		}
	}

	checks, _ := evaluateInvariants(in)

	fault := checksNamed(t, checks, "quarantines-match-faults.e1.reason")[0]
	require.True(t, fault.Passed, "actual=%s details=%s", fault.Actual, fault.Details)
	marker := checksNamed(t, checks, "quarantines-match-faults.bg.reason")[0]
	require.False(t, marker.Passed)
	require.Equal(t, "mismatched=2", marker.Actual)
}

// Test flow:
//  1. For each case, build gateway samples of one participant's attempt-failure counter, one every 10 seconds, with a fault window from 60 s to 120 s.
//  2. Leave the participant's series out of the samples whose case value is nil.
//  3. Assert attemptFailuresIncreased reports an increase exactly when the counter rose inside the window, counting a restart's drop as new failures.
func TestAttemptFailuresIncreasedIsResetAware(t *testing.T) {
	const participant = "gonka1hang"
	start := time.Date(2026, 10, 22, 20, 35, 0, 0, time.UTC)
	span := timeSpan{From: start.Add(60 * time.Second), To: start.Add(120 * time.Second)}
	value := func(v float64) *float64 { return &v }
	cases := []struct {
		name   string
		values []*float64
		want   bool
	}{
		{name: "restart inside the window, then new failures", values: []*float64{value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(0), value(3), value(21), value(21), value(21)}, want: true},
		{name: "flat through the window", values: []*float64{value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61)}, want: false},
		{name: "restart inside the window, no new failures", values: []*float64{value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(61), value(0), value(0), value(0), value(0), value(0)}, want: false},
		{name: "plain rise inside the window", values: []*float64{value(5), value(5), value(5), value(5), value(5), value(5), value(5), value(5), value(9), value(9), value(9), value(9), value(9)}, want: true},
		{name: "series missing from one sample", values: []*float64{value(61), value(61), value(61), value(61), value(61), value(61), value(61), nil, value(61), value(61), value(61), value(61), value(61)}, want: false},
		{name: "first failures inside the window", values: []*float64{nil, nil, nil, nil, nil, nil, nil, value(2), value(2), value(2), value(2), value(2), value(2)}, want: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			var in InvariantInputs
			for index, counter := range testCase.values {
				sample := MetricsSample{At: start.Add(time.Duration(index) * 10 * time.Second), Target: "gateway", Domain: map[string]float64{}}
				if counter != nil {
					sample.Domain[attemptFailureKey(participant, "empty_stream")] = *counter
				}
				in.Metrics = append(in.Metrics, sample)
			}
			require.Equal(t, testCase.want, newInvariantContext(in).attemptFailuresIncreased(participant, span))
		})
	}
}
