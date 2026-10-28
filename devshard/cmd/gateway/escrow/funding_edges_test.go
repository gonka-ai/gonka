package escrow

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/funding"
	"devshard/cmd/gateway/liquidity"
	"devshard/types"
)

func edgeLines(narrator *recordingLifecycleNarrator) []string {
	return slices.DeleteFunc(narrator.recorded(), func(line string) bool {
		return !strings.HasPrefix(line, "starved ") && !strings.HasPrefix(line, "full ")
	})
}

// Test flow:
//  1. Plan a planning manager whose escrow is starved, then three times with it full, then once starved and once full again.
//  2. Assert the first sighting is narrated at once, the full edge only on the second full read, and the one-read flap not at all.
func TestAnEdgeIsNarratedOnlyAfterItHoldsForTwoReads(t *testing.T) {
	manager, _, _, narrator := planningManager(t)
	sessionConfig := types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}
	plan := func(balance uint64) {
		manager.funds = fakeFunds{"5": {Config: sessionConfig, Balance: balance, TokenPrice: 1, FeePerNonce: 10}}
		_ = manager.planFunding(context.Background(), true)
	}

	plan(40_000)
	plan(1_000_000)
	plan(1_000_000)
	plan(1_000_000)
	plan(40_000)
	plan(1_000_000)

	require.Equal(t, []string{"starved 5 free 40000", "full 5 free 1000000"}, edgeLines(narrator))
}

func guaranteeReading(full, gated bool) modelReading {
	money := liquidity.Escrow{Free: 40_000, Starved: true, Idle: true}
	if full {
		money = liquidity.Escrow{Free: 1_000_000, Full: true, Idle: true}
	}
	return modelReading{state: funding.ModelState{
		Escrows: []funding.EscrowState{{ID: "1", Label: 7, FullCost: 32_778, Money: money}}, Counted: 1,
		TargetCount: 1, MaxUnsettled: 6, FullContextSlots: 1, Amount: 1_000_000, CreateFee: 100,
		FullCost: 32_778, Slot: 65_556, Priced: true, EpochKnown: true, ServedByNetwork: true,
		BreakerGated: gated, CurrentLabel: 7,
	}}
}

// Test flow:
//  1. Decide a model short of its guarantee four times with its breaker gated on every other read, then once with the guarantee met, then once short and gated again.
//  2. Assert the broken guarantee began twice: once per shortage, not once per gated read.
func TestABrokenGuaranteeIsNarratedOncePerShortage(t *testing.T) {
	planner := fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	began := 0

	for _, reading := range []modelReading{
		guaranteeReading(false, true), guaranteeReading(false, false), guaranteeReading(false, true), guaranteeReading(false, false),
		guaranteeReading(true, false), guaranteeReading(false, true),
	} {
		if _, _, transitions := planner.decide("qwen", reading, true, now); transitions.brokenBegan {
			began++
		}
		now = now.Add(TickInterval)
	}

	require.Equal(t, 2, began, "broken-guarantee episodes begun")
}
