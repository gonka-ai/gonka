package scheduler

import (
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

const plannerTestContext = 8_192

func plannerSnapshot() chain.PhaseSnapshot {
	return chain.PhaseSnapshot{MaxNonce: 1_000, Models: map[string]chain.ModelParams{modelA: {MaxModelLen: plannerTestContext}}}
}

// plannerScheduler prices modelA at a 32 768-token floor and a 4 096-token answer, two attempts a race.
func plannerScheduler(candidates ...candidate) (*Scheduler, *[]exhaustionReport, *moneyShortRecorder) {
	settings := config.Defaults()
	settings.Limits.MaxTokensCap = 4_096
	settings.Engine.MaxAttemptsPerRequest = 2
	scheduler, _, _ := newScheduler(candidates...)
	scheduler.settings = config.NewHolder(&settings)
	var reported []exhaustionReport
	scheduler.onEscrowExhausted = func(escrowID string, reason ExhaustionReason) {
		reported = append(reported, exhaustionReport{escrowID: escrowID, reason: reason})
	}
	recorder := &moneyShortRecorder{}
	scheduler.onMoneyShort = recorder.record
	return scheduler, &reported, recorder
}

// Test flow:
//  1. One escrow holding 20 000, below its model's 32 768 floor, and a 264-token request it can pay.
//  2. Pick an escrow.
//  3. Assert it serves the request and reports nothing.
func TestAnEscrowBelowItsModelsFloorServesWhatItCanPay(t *testing.T) {
	t.Parallel()
	scheduler, reported, _ := plannerScheduler(candidate{id: "starved", weight: 1, latestNonce: 1, balance: 20_000, tokenPrice: 1})

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: 200, OutputTokens: 64}, plannerSnapshot())

	if err != nil || picked.ID != "starved" || len(*reported) != 0 {
		t.Fatalf("pickEscrow() = %q, %v, reported %v, want starved served and nothing reported", picked.ID, err, *reported)
	}
}

// Test flow:
//  1. Build a scheduler with one escrow below its model's floor and one past the hosts' nonce cap.
//  2. Read Exhaustion for each.
//  3. Assert the starved one reads empty and the capped one reads the nonce cap.
func TestExhaustionReportsOnlyTheNonceCap(t *testing.T) {
	t.Parallel()
	scheduler, _, _ := plannerScheduler()
	scheduler.snapshots = &fakeSnapshots{snapshot: plannerSnapshot()}
	starved := Escrow{ID: "starved", Model: modelA, Session: &fakeSession{balance: 20_000, tokenPrice: 1, slots: slotsOf("starved", 4), latestNonce: 1}}
	capped := Escrow{ID: "capped", Model: modelA, Session: &fakeSession{balance: 1 << 30, tokenPrice: 1, slots: slotsOf("capped", 4), latestNonce: 1_000_000}}

	if reason := scheduler.Exhaustion(starved); reason != ExhaustionNone {
		t.Fatalf("Exhaustion(starved) = %q, want ExhaustionNone", reason)
	}
	if reason := scheduler.Exhaustion(capped); reason != ExhaustionNonceCap {
		t.Fatalf("Exhaustion(capped) = %q, want %q", reason, ExhaustionNonceCap)
	}
}

// Test flow:
//  1. Table-driven: a full escrow weighted 100 and a starved one holding 40 000 weighted 1, picked for a 264-token request and for a 30 064-token one.
//  2. Pick an escrow.
//  3. Assert the small request lands on the starved escrow and the large one on the full escrow.
func TestTheStarvedResidueIsSpentFirst(t *testing.T) {
	t.Parallel()
	small := RequestProfile{Model: modelA, InputBytes: 200, OutputTokens: 64}
	large := RequestProfile{Model: modelA, InputBytes: 30_000, OutputTokens: 64}
	for _, testCase := range []struct {
		name    string
		profile RequestProfile
		want    string
	}{
		{name: "small request", profile: small, want: "starved"},
		{name: "full-context request", profile: large, want: "full"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := plannerScheduler(
				candidate{id: "full", weight: 100, latestNonce: 1, balance: 1_000_000, tokenPrice: 1},
				candidate{id: "starved", weight: 1, latestNonce: 1, balance: 40_000, tokenPrice: 1},
			)

			picked, err := pickWith(scheduler, testCase.profile, plannerSnapshot())

			if err != nil || picked.ID != testCase.want {
				t.Fatalf("pickEscrow(%s) = %q, %v, want %q", testCase.name, picked.ID, err, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Table-driven: a pick whose first candidate cannot pay and whose last is past the nonce cap; the same with the first candidate avoided for money; one with the first avoided because its hosts are busy.
//  2. Pick an escrow.
//  3. Assert the money-short hook fired for the first two and not for the busy one.
func TestAnyMoneyDeclineOfAFailedPickReportsTheModelMoneyShort(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		avoided avoidedEscrows
		want    []string
	}{
		{name: "declined for money, then a nonce-capped escrow", want: []string{modelA}},
		{name: "avoided for money, then a nonce-capped escrow", avoided: avoidedEscrows{"poor": avoidedOutOfFunds}, want: []string{modelA}},
		{name: "avoided for busy hosts, then a nonce-capped escrow", avoided: avoidedEscrows{"poor": avoidedHostsBusy}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, recorder := plannerScheduler(
				candidate{id: "poor", weight: 1, latestNonce: 1, balance: 50, tokenPrice: 10},
				candidate{id: "capped", weight: 1, latestNonce: 1_000_000, balance: 1 << 30, tokenPrice: 10},
			)
			profile := RequestProfile{Model: modelA, InputBytes: 20}

			_, _ = scheduler.pickEscrow(profile, plannerSnapshot(), newWaiter(profile, time.Time{}), testCase.avoided)

			if got := recorder.recorded(); !slices.Equal(got, testCase.want) {
				t.Fatalf("money-short reports = %v, want %v", got, testCase.want)
			}
		})
	}
}
