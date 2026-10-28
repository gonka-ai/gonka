package scenarios

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/escrow"
	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/store"
)

func plannerSpec() testSpec {
	spec := defaultSpec()
	spec.model.fullContextSlots = 2
	return spec
}

func repeatSeeded(count int, amount uint64, role string) []seededEscrow {
	seeded := make([]seededEscrow, count)
	for index := range seeded {
		seeded[index] = seededEscrow{amount: amount, role: role}
	}
	return seeded
}

func everyParticipant(count int, behaviour participantBehaviour) map[int]participantBehaviour {
	behaviours := make(map[int]participantBehaviour, count)
	for index := range count {
		behaviours[index] = behaviour
	}
	return behaviours
}

func seededID(index int) func(*gatewayHarness) string {
	return func(harness *gatewayHarness) string { return harness.seededIDs[index] }
}

func rowOf(harness *gatewayHarness, escrowID string) (store.DevshardRecord, bool) {
	for _, row := range harness.rows() {
		if row.EscrowID == escrowID {
			return row, true
		}
	}
	return store.DevshardRecord{}, false
}

// loggedEntries is this run's share of the recorder the test shares across its runs.
func (h *gatewayHarness) loggedEntries() []logcapture.Entry {
	if h.logged == nil {
		return nil
	}
	if running := h.currentGateway(); running != nil {
		running.Journal().Flush()
	}
	entries := h.logged.All()
	return entries[min(h.logStart, len(entries)):]
}

func (h *gatewayHarness) logLines(message string) int {
	lines := 0
	for _, entry := range h.loggedEntries() {
		if entry.Msg == message {
			lines++
		}
	}
	return lines
}

func (h *gatewayHarness) createdWithReason(model, reason string) int {
	lines := createLinesOf(h.loggedEntries())
	count := 0
	for _, created := range h.chain.createsOf(model) {
		if lines[formatEscrowID(created.escrowID)].reason == reason {
			count++
		}
	}
	return count
}

func (h *gatewayHarness) plannerCreateTimes(model string) []time.Time {
	lines := createLinesOf(h.loggedEntries())
	var times []time.Time
	for _, created := range h.chain.createsOf(model) {
		switch funding.Reason(lines[formatEscrowID(created.escrowID)].reason) {
		case funding.ReasonGuard, funding.ReasonCapacity, funding.ReasonSpread, funding.ReasonStandby:
			if !created.at.Before(h.bootedAt) {
				times = append(times, created.at)
			}
		}
	}
	slices.SortFunc(times, func(left, right time.Time) int { return left.Compare(right) })
	return times
}

// bucketViolation replays funding.Bucket at each create's broadcast, the instant its plan decided in the bubble: the same refill, the same re-anchor of a full bucket.
func bucketViolation(times []time.Time, start time.Time) error {
	bucket := funding.NewBucket(start)
	for index, at := range times {
		bucket.Refill(at, escrow.TickInterval)
		if bucket.Available() < 1 {
			return fmt.Errorf("planner create %d at %s exceeds the bucket: two at once, then one per %s", index+1, at.Sub(start), escrow.TickInterval)
		}
		bucket.Take(1)
	}
	return nil
}

func expectPlannerCreatesWithinBucket(label, model string) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		return bucketViolation(harness.plannerCreateTimes(model), harness.bootedAt)
	}}
}

// fullEscrows counts a model's serving escrows whose session, already net of every reservation on it, pays a whole race.
func (h *gatewayHarness) fullEscrows(model string) int {
	running := h.currentGateway()
	snapshot, configuration := running.Observer().Snapshot(), running.Config().Load()
	fullCost, _ := scheduler.RequestCost(configuration.Limits.RetirementReserve(model, snapshot.Models[model].MaxModelLen), snapshot.TokenPrice, snapshot.FeePerNonce)
	slot := fullCost * uint64(scheduler.AttemptsToFund(configuration.Engine.MaxAttemptsPerRequest))
	full := 0
	for _, row := range h.rows() {
		if row.Active && row.Model == model && h.userBalance(row.EscrowID) >= slot {
			full++
		}
	}
	return full
}

func (h *gatewayHarness) creatorWallet() uint64 {
	wallet, _ := h.chain.SpendableBalance(context.Background(), h.fleet.creator.Address(), "")
	return wallet
}

func (h *gatewayHarness) fundingReport(model string) (escrow.FundingReport, bool) {
	for _, report := range h.currentGateway().Manager().FundingReports() {
		if report.Model == model {
			return report, true
		}
	}
	return escrow.FundingReport{}, false
}

// expectFullCountMatchesThePlanner cross-checks at a quiet step the full count the harness rebuilds against the planner's last reading, Guarantee + K.
func expectFullCountMatchesThePlanner(label string, model modelSpec) expectThat {
	return expectThat{label: label, verify: func(harness *gatewayHarness) error {
		report, found := harness.fundingReport(model.id)
		if !found {
			return fmt.Errorf("no funding report for %s", model.id)
		}
		if rebuilt, planned := harness.fullEscrows(model.id), report.Guarantee+model.guaranteeSlots(); rebuilt != planned {
			return fmt.Errorf("the harness counts %d full escrows, the planner's last reading %d", rebuilt, planned)
		}
		return nil
	}}
}

func everyAnswer(count, status int) func(*gatewayHarness) error {
	return func(harness *gatewayHarness) error {
		statuses := harness.answeredStatuses()
		if len(statuses) != count || slices.ContainsFunc(statuses, func(got int) bool { return got != status }) {
			return fmt.Errorf("statuses = %v, want %d answers of %d", statuses, count, status)
		}
		return nil
	}
}

// Test flow:
//  1. Table-driven: planner create times two at boot, three at boot, one per tick, two in one tick after a refill, and a burst from a bucket that sat full followed by a create fourteen or fifteen seconds later.
//  2. Assert the replay refuses three at boot and the create fourteen seconds after the burst, and accepts the rest.
func TestBucketViolationFollowsThePlannersBucket(t *testing.T) {
	start := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	at := start.Add
	for _, testCase := range []struct {
		name      string
		times     []time.Time
		violation bool
	}{
		{name: "two at boot", times: []time.Time{at(time.Second), at(2 * time.Second)}},
		{name: "three at boot", times: []time.Time{at(time.Second), at(2 * time.Second), at(3 * time.Second)}, violation: true},
		{name: "one per tick", times: []time.Time{at(0), at(15 * time.Second), at(30 * time.Second), at(45 * time.Second)}},
		{name: "two in one tick after a full refill", times: []time.Time{at(0), at(time.Second), at(31 * time.Second), at(32 * time.Second)}},
		{name: "a full bucket banks no time while it waits", times: []time.Time{at(30 * time.Second), at(31 * time.Second), at(44 * time.Second)}, violation: true},
		{name: "a full bucket earns its next token one interval after the burst", times: []time.Time{at(30 * time.Second), at(31 * time.Second), at(45 * time.Second)}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := bucketViolation(testCase.times, start); (err != nil) != testCase.violation {
				t.Fatalf("bucketViolation(%s) = %v, want violation %v", testCase.name, err, testCase.violation)
			}
		})
	}
}
