package loadtest

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// renewedQuarantine puts participant in quarantine from from to to, each quarantine lasting each and
// renewed as it ends, then out of it.
func renewedQuarantine(in *InvariantInputs, participant string, from, to, each time.Duration) {
	for offset := from; offset <= to; offset += throttlePollEvery {
		start := from + (offset-from)/each*each
		p := ThrottleParticipant{ParticipantKey: participant, ShadowQuarantined: true, FailureStrikes: 3, QuarantineUntil: at(start + each).Format(time.RFC3339Nano)}
		in.Throttle = append(in.Throttle, ThrottleSample{At: at(offset), EscrowID: "11", Participants: []ThrottleParticipant{p, {ParticipantKey: "p1"}}})
	}
	in.Throttle = append(in.Throttle, ThrottleSample{At: at(to + throttlePollEvery), EscrowID: "11", Participants: []ThrottleParticipant{{ParticipantKey: participant}, {ParticipantKey: "p1"}}})
}

// Test flow:
// 1. Quarantine p2 from 3m30s to 5m in two 45 s quarantines, the second set as the first ends, as the gateway renews one.
// 2. Require durations to pass: each quarantine lasts the configured 45 s.
// 3. Quarantine p2 once from 3m30s with quarantine_until 2 minutes later, and require durations to fail on that single quarantine.
func TestInvariantDurationsJudgeEachQuarantine(t *testing.T) {
	renewed := passingInputs()
	renewed.Throttle = nil
	renewedQuarantine(&renewed, "p2", 3*time.Minute+30*time.Second, 5*time.Minute, 45*time.Second)
	checks, _ := evaluateInvariants(renewed)
	check := checksNamed(t, checks, "quarantines-match-faults.durations")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)

	long := passingInputs()
	long.Throttle = nil
	renewedQuarantine(&long, "p2", 3*time.Minute+30*time.Second, 5*time.Minute, 2*time.Minute)
	checks, _ = evaluateInvariants(long)
	check = checksNamed(t, checks, "quarantines-match-faults.durations")[0]
	require.False(t, check.Passed)
	require.Contains(t, check.Details, "p2 single quarantine 2m0s > 45s")
}

// Test flow:
// 1. Add a second fault on p2 from 6m to 7m, starting inside the grace of the 3m-4m fault.
// 2. Quarantine p2 from 6m05s to 7m, renewed every 45 s, so it outlives the first fault's grace but not the second's.
// 3. Require durations to pass: the quarantine is charged to the fault it started in, not to the earlier one.
func TestInvariantDurationsChargeTheLatestCause(t *testing.T) {
	in := passingInputs()
	in.Events = append(in.Events, EventRecord{ID: "e4", Case: "host-public-port-down", Action: "tripfile", Target: "versiond-3", StartedAt: atPtr(6 * time.Minute), EndedAt: atPtr(7 * time.Minute)})
	in.Throttle = nil
	renewedQuarantine(&in, "p2", 6*time.Minute+5*time.Second, 7*time.Minute, 45*time.Second)

	checks, _ := evaluateInvariants(in)

	check := checksNamed(t, checks, "quarantines-match-faults.durations")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
}

// Test flow:
// 1. Read the expected quarantine families of an ML hang and of a stream held after its first token.
// 2. Require both to allow empty_stream, as the gateway reports an attempt with a receipt and no content, next to stalled_winner and transport.
func TestHangExpectsTheReasonsTheGatewayCanSee(t *testing.T) {
	for _, patch := range []map[string]any{{"hang": true}, {"pause_stream": true}} {
		allowed, forbidden, specified := expectedQuarantine(EventRecord{Action: "ml_fault", Params: map[string]any{"patch": patch}})
		require.True(t, specified)
		require.Nil(t, forbidden)
		require.Equal(t, map[string]bool{"stalled_winner": true, "transport": true, "empty_stream": true}, allowed)
	}
}

// Test flow:
// 1. Put p3 over its peer Chat budget for the whole run, as a background marker observing host_rate_limited.
// 2. Burn p3's slots as throttled and catching-up at 9m, outside its foreground challenge window, and burn one ejected; burn an honest p1 slot as throttled.
// 3. Require the throttled and catching-up burns of p3 to be left out, and its ejected burn and p1's burn to count.
func TestGhostRateLeavesOutBudgetBurnsOfTheHostOverBudget(t *testing.T) {
	in := passingInputs()
	in.Events = append(in.Events, EventRecord{ID: "budget", Case: "host-over-peer-chat-budget", Action: "marker", Target: "versiond-4",
		Params: map[string]any{"background": true, "observe": "host_rate_limited"}, StartedAt: atPtr(0), EndedAt: atPtr(10 * time.Minute)})
	excused := burnExcuse(in)

	require.True(t, excused(gatewayBurn{At: at(9 * time.Minute), Host: "p3", Reason: "participant_throttled_no_send"}))
	require.True(t, excused(gatewayBurn{At: at(9 * time.Minute), Host: "p3", Reason: "participant_catching_up_no_send"}))
	require.False(t, excused(gatewayBurn{At: at(9 * time.Minute), Host: "p3", Reason: "participant_ejected_no_send"}))
	require.False(t, excused(gatewayBurn{At: at(9 * time.Minute), Host: "p1", Reason: "participant_throttled_no_send"}))
}

// Test flow:
// 1. Leave two burned nonces open on escrow 11 at the final poll, and a new escrow 15 at nonce 1 with its warm-up probe open and two pending txs.
// 2. Require clean_end_state to pass and to report both escrows apart.
// 3. Leave one more open nonce on escrow 11 that was never burned, and require clean_end_state to fail on it alone.
func TestCleanEndStateLeavesOutBurnedAndWarmUpOnlyEscrows(t *testing.T) {
	final := func(extra string) InvariantInputs {
		in := passingInputs()
		last := &in.Pending[len(in.Pending)-2]
		last.OpenStarted = map[string]int64{"7": last.At.Unix() - 60, "8": last.At.Unix() - 50}
		if extra != "" {
			last.OpenStarted[extra] = last.At.Unix() - 10
		}
		last.Open = len(last.OpenStarted)
		in.Pending = append(in.Pending, PendingSample{At: last.At, EscrowID: "15", Nonce: 1, Open: 1, PendingTxs: 2, OpenStarted: map[string]int64{"1": last.At.Unix() - 190}})
		in.Burns = []gatewayBurn{{EscrowID: "11", Nonce: "7"}, {EscrowID: "11", Nonce: "8"}}
		return in
	}

	checks, _ := evaluateInvariants(final(""))
	check := checksNamed(t, checks, "no-nonce-left-pending.clean_end_state")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
	require.Equal(t, "dirty=0 apart=2", check.Actual)
	require.Equal(t, "escrow 11 burned_open=2; escrow 15 warm-up only, nonce=1 open=1 pending_txs=2", check.Details)

	checks, _ = evaluateInvariants(final("9"))
	check = checksNamed(t, checks, "no-nonce-left-pending.clean_end_state")[0]
	require.False(t, check.Passed)
	require.Equal(t, "dirty=1 apart=1", check.Actual)
	require.Contains(t, check.Details, "escrow 11 open=1 pending_txs=0")
}

// escrowEveryMinute adds an escrow sample for a new escrow every minute from 30 s on.
func escrowEveryMinute(in *InvariantInputs) {
	for i := 0; i < 10; i++ {
		in.Metrics = append(in.Metrics, MetricsSample{At: at(time.Duration(i)*time.Minute + 30*time.Second), Phase: "workload", Target: "escrow", EscrowID: fmt.Sprint(100 + i)})
	}
}

// createdBy counts the escrows escrowEveryMinute has created by offset.
func createdBy(offset time.Duration) float64 {
	return float64(min(10, max(0, (offset-30*time.Second)/time.Minute+1)))
}

// Test flow:
// 1. Create an escrow every minute, and grow versiond-2 by 20 goroutines and one validation queue for each, as every host keeps per escrow until the epoch ends.
// 2. Require goroutines and heap_slope to pass, and the host row to count the escrows created between the windows.
// 3. Grow it by 60 goroutines and twice the queue bytes per escrow instead, and require both checks to fail.
func TestInvariantTrendsAllowEachEscrowsHostRuntime(t *testing.T) {
	grow := func(goroutines, bytes float64) InvariantInputs {
		in := passingInputs()
		escrowEveryMinute(&in)
		for i := range in.Metrics {
			s := &in.Metrics[i]
			if s.Target == "versiond-2/devshardd" {
				n := createdBy(s.At.Sub(fixtureT0))
				s.Values["go_goroutines"] = 200 + goroutines*n
				s.Values["go_memstats_heap_alloc_bytes"] = float64(80<<20) + bytes*n
			}
		}
		return in
	}

	checks, report := evaluateInvariants(grow(hostValidationWorkers, hostValidationQueueBytes))
	for _, name := range []string{"no-restarts-or-resource-growth.goroutines", "no-restarts-or-resource-growth.heap_slope"} {
		check := checksNamed(t, checks, name)[0]
		require.True(t, check.Passed, "%s: actual=%s details=%s", name, check.Actual, check.Details)
	}
	for _, row := range report.Tables["no-restarts-or-resource-growth"].([]trendRow) {
		if row.Target == "versiond-2/devshardd" {
			require.Equal(t, 8, row.EscrowsCreated)
		}
	}

	checks, _ = evaluateInvariants(grow(3*hostValidationWorkers, 2*hostValidationQueueBytes))
	for _, name := range []string{"no-restarts-or-resource-growth.goroutines", "no-restarts-or-resource-growth.heap_slope"} {
		require.False(t, checksNamed(t, checks, name)[0].Passed, name)
	}
}

// Test flow:
// 1. Keep versiond-2 idle at 40 MiB until 4m, as the second replica of an HA pair before it gets traffic, then ramp it to 120 MiB by 7m and hold it there.
// 2. Require heap_slope to fail while the host finishes requests from the start: the ramp falls in its steady samples.
// 3. Count its finished requests from 4m only, and require heap_slope to pass, with the row's steady samples starting at 6m.
func TestInvariantTrendsStartAHostWhenItServes(t *testing.T) {
	idleThenServing := func(servingFrom time.Duration) InvariantInputs {
		in := passingInputs()
		for i := range in.Metrics {
			s := &in.Metrics[i]
			if s.Target != "versiond-2/devshardd" {
				continue
			}
			offset := s.At.Sub(fixtureT0)
			ramp := min(1, max(0, (offset-4*time.Minute).Minutes()/3))
			s.Values["go_memstats_heap_alloc_bytes"] = float64(40<<20) + ramp*float64(80<<20)
			served := 0.0
			if offset >= servingFrom {
				served = 1 + offset.Seconds()
			}
			s.Domain[hostServingFamily+`{reason="ok",terminal="finished"}`] = served
		}
		return in
	}

	checks, _ := evaluateInvariants(idleThenServing(0))
	require.False(t, checksNamed(t, checks, "no-restarts-or-resource-growth.heap_slope")[0].Passed)

	checks, report := evaluateInvariants(idleThenServing(4 * time.Minute))
	check := checksNamed(t, checks, "no-restarts-or-resource-growth.heap_slope")[0]
	require.True(t, check.Passed, "actual=%s details=%s", check.Actual, check.Details)
	for _, row := range report.Tables["no-restarts-or-resource-growth"].([]trendRow) {
		if row.Target == "versiond-2/devshardd" {
			require.Equal(t, "6m0s", row.SteadyFrom)
		}
	}
}
