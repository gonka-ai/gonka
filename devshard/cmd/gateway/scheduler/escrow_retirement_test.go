package scheduler

import (
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/types"
)

// retirementHarness builds a scheduler with one candidate escrow funded to the given balance, with a max-tokens cap of 4096.
func retirementHarness(t *testing.T, balance uint64) (*Scheduler, *[]exhaustionReport) {
	t.Helper()
	settings := config.Defaults()
	settings.Limits.MaxTokensCap = 4_096
	scheduler, _, _ := newScheduler(
		candidate{id: "only", weight: 100, latestNonce: 1, balance: balance, tokenPrice: 1},
	)
	scheduler.settings = config.NewHolder(&settings)
	var reported []exhaustionReport
	scheduler.onEscrowExhausted = func(escrowID string, reason ExhaustionReason) {
		reported = append(reported, exhaustionReport{escrowID: escrowID, reason: reason})
	}
	return scheduler, &reported
}

// Test flow:
//  1. Build a `retirementHarness` funded well enough for a capped request.
//  2. Pick an escrow for a request whose output tokens are far larger than the cap allows.
//  3. Assert the pick fails with `types.ErrInsufficientBalance`.
//  4. Assert no exhaustion was reported, since a solvent escrow is not retired over one oversized request.
func TestARequestTooDearForAnEscrowDoesNotRetireIt(t *testing.T) {
	t.Parallel()
	scheduler, reported := retirementHarness(t, 1<<20)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, InputBytes: 8_192, OutputTokens: 10_000_000}, chain.PhaseSnapshot{})

	if !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("pickEscrow = %v, want the request refused as unaffordable", err)
	}
	if len(*reported) != 0 {
		t.Fatalf("one request retired %v; an escrow that still affords the retirement floor is not finished", *reported)
	}
}

// Test flow:
//  1. Build a `retirementHarness` funded well enough for a capped request.
//  2. Pick the escrow by name for a request whose output tokens are far larger than the cap allows.
//  3. Assert the pick fails with `types.ErrInsufficientBalance`.
//  4. Assert no exhaustion was reported for the pinned escrow.
func TestAPinnedEscrowIsRefusedWithoutBeingRetired(t *testing.T) {
	t.Parallel()
	scheduler, reported := retirementHarness(t, 1<<20)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "only", InputBytes: 8_192, OutputTokens: 10_000_000}, chain.PhaseSnapshot{})

	if !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("pickEscrow = %v, want the pinned request refused as unaffordable", err)
	}
	if len(*reported) != 0 {
		t.Fatalf("a pinned request retired %v", *reported)
	}
}

const (
	fullContextTokens  = 180_000
	fullContextReserve = 4 * fullContextTokens
)

// pinContextLength makes the operator pin modelA's context length, the way model_limits does.
func pinContextLength(scheduler *Scheduler) {
	settings := *scheduler.settings.Load()
	contextLength := int64(fullContextTokens)
	settings.Limits.ModelLimits = map[string]config.ModelLimits{modelA: {MaxModelLen: &contextLength}}
	scheduler.settings.Swap(&settings)
}

// Test flow:
//  1. Build a funded escrow whose cursor sits at the fallback nonce ceiling.
//  2. Ask `Exhaustion` before governance `max_nonce` is known, then with a known cap below the cursor.
//  3. Assert the fallback ceiling reads nothing, since it is not the hosts' cap, and the known cap reads `ExhaustionNonceCap`.
func TestExhaustionReportsTheHostsNonceCapButNotTheFallbackCeiling(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		maxNonce uint64
		want     ExhaustionReason
	}{
		{name: "max_nonce_unknown", want: ""},
		{name: "max_nonce_known", maxNonce: 1_000, want: ExhaustionNonceCap},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			scheduler, _, _ := newScheduler(candidate{id: "worn", weight: 100, latestNonce: fallbackNonceCeiling, balance: 1 << 30, tokenPrice: 1})
			settings := config.Defaults()
			settings.Limits.MaxTokensCap = 16
			scheduler.settings = config.NewHolder(&settings)
			scheduler.snapshots = &fakeSnapshots{snapshot: chain.PhaseSnapshot{MaxNonce: testCase.maxNonce}}

			if reason := scheduler.Exhaustion(scheduler.escrows.Candidates(modelA)[0]); reason != testCase.want {
				t.Fatalf("Exhaustion = %q, want %q", reason, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a `retirementHarness` holding 200 000, its model pinned at 180 000 tokens, so the escrow is below the model's floor.
//  2. Pick the escrow by name for a small request, the way a hedge does.
//  3. Assert the pinned escrow is served and nothing is reported: it can pay this attempt.
func TestAPinnedHedgeIsServedByAnEscrowBelowItsModelsFloor(t *testing.T) {
	t.Parallel()
	scheduler, reported := retirementHarness(t, 200_000)
	pinContextLength(scheduler)

	picked, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "only", InputBytes: 10, OutputTokens: 16}, chain.PhaseSnapshot{})

	if err != nil || picked.ID != "only" {
		t.Fatalf("pickEscrow = %q, %v; want the pinned escrow served", picked.ID, err)
	}
	if len(*reported) != 0 {
		t.Fatalf("reported = %v, want nothing: a hedge never retires the escrow its race runs on", *reported)
	}
}

// Test flow:
//  1. Build a `retirementHarness` holding 200 000, its model pinned at 180 000 tokens.
//  2. Pick the escrow by name for a request far dearer than its balance.
//  3. Assert the pick fails with `ErrPinnedEscrowShort`, still reads as `ErrNoEscrowCapacity` and `types.ErrInsufficientBalance`, and nothing is reported.
func TestAPinnedHedgeTheEscrowCannotPayIsDeclinedUnreported(t *testing.T) {
	t.Parallel()
	scheduler, reported := retirementHarness(t, 200_000)
	pinContextLength(scheduler)

	_, err := pickWith(scheduler, RequestProfile{Model: modelA, Escrow: "only", InputBytes: 8_192, OutputTokens: 10_000_000}, chain.PhaseSnapshot{})

	if !errors.Is(err, ErrPinnedEscrowShort) || !errors.Is(err, ErrNoEscrowCapacity) || !errors.Is(err, types.ErrInsufficientBalance) {
		t.Fatalf("pickEscrow = %v, want ErrPinnedEscrowShort wrapping ErrNoEscrowCapacity and ErrInsufficientBalance", err)
	}
	if len(*reported) != 0 {
		t.Fatalf("reported = %v, want nothing: a declined hedge marks no escrow", *reported)
	}
}
