package journal

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/internal/logcapture"
)

// Test flow:
//  1. Table-driven: each case pairs one funding producer call with the exact entry it must render.
//  2. For each case, build a journal with a `logcapture.Recorder`, run the call and flush.
//  3. Assert the recorder captured exactly that entry, and the funding kind rides the money lane.
func TestFundingTransitionsRenderTheLinesTheirProducersNarrate(t *testing.T) {
	storeDown := errors.New("store unavailable")
	testCases := []struct {
		name    string
		produce func(events *Journal)
		want    logcapture.Entry
	}{
		{
			name: "a funding decision",
			produce: func(events *Journal) {
				events.EscrowPlanned("qwen", 2, 1, "guard,idle_starved", true, 131_112, 974_444, 1)
			},
			want: logcapture.Entry{Level: "info", Msg: "escrow funding planned", Fields: []any{
				"model", "qwen", "creates", 2, "retires", 1, "reasons", "guard,idle_starved",
				"money_short", true, "need", uint64(131_112), "liquid", uint64(974_444), "full_count", 1,
			}},
		},
		{
			name:    "an escrow starves",
			produce: func(events *Journal) { events.EscrowStarved("7", 40_000) },
			want:    logcapture.Entry{Level: "info", Msg: "escrow starved", Fields: []any{"escrow", "7", "free", uint64(40_000)}},
		},
		{
			name:    "an escrow is full again",
			produce: func(events *Journal) { events.EscrowFull("7", 900_000) },
			want:    logcapture.Entry{Level: "info", Msg: "escrow full again", Fields: []any{"escrow", "7", "free", uint64(900_000)}},
		},
		{
			name:    "the guarantee breaks",
			produce: func(events *Journal) { events.FundingGuaranteeBroken("qwen", 1, 2) },
			want:    logcapture.Entry{Level: "warn", Msg: "funding guarantee broken", Fields: []any{"model", "qwen", "have", 1, "want", 2}},
		},
		{
			name:    "a model is misconfigured",
			produce: func(events *Journal) { events.FundingMisconfigured("qwen", 69_000, 69_762) },
			want: logcapture.Entry{Level: "warn", Msg: "model amount cannot fund a full escrow", Fields: []any{
				"model", "qwen", "amount", uint64(69_000), "need", uint64(69_762),
			}},
		},
		{
			name:    "the budget is reached",
			produce: func(events *Journal) { events.EscrowBudgetReached("qwen", 6, 6) },
			want:    logcapture.Entry{Level: "warn", Msg: "escrow budget reached", Fields: []any{"model", "qwen", "counted", 6, "max_unsettled", 6}},
		},
		{
			name:    "a funding plan is skipped",
			produce: func(events *Journal) { events.FundingPlanFailed(storeDown) },
			want:    logcapture.Entry{Level: "warn", Msg: "escrow funding plan skipped", Fields: []any{"error", storeDown}},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lines := &logcapture.Recorder{}
			events := newJournal(t, Settings{Lines: lines})

			testCase.produce(events)
			events.Flush()

			require.Equal(t, []logcapture.Entry{testCase.want}, lines.All())
		})
	}
	require.True(t, KindFundingTransition.onMoneyLane(), "the funding kind must ride the money lane: a once-per-episode line must never drop")
	require.Equal(t, "funding_transition", KindFundingTransition.String())
}
