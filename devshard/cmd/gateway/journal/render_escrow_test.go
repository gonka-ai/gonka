package journal

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/internal/logcapture"
)

// Test flow:
//  1. Table-driven: each case pairs an escrow-transition producer call with the exact log entry it must render (level, message, and every key with its type).
//  2. For each case, build a journal with a `logcapture.Recorder`, run the case's `produce` function, and flush.
//  3. Assert the recorder captured exactly the case's expected entry.
func TestEscrowTransitionsRenderTheLinesTheirProducersNarrate(t *testing.T) {
	closeFailure := errors.New("storage refused to close")
	unverifiable := errors.New("slot 2 signature does not verify")
	missing := errors.New("escrow not found")

	testCases := []struct {
		name    string
		produce func(events *Journal)
		want    logcapture.Entry
	}{
		{
			name:    "an escrow is published",
			produce: func(events *Journal) { events.EscrowServing("7", "qwen") },
			want:    logcapture.Entry{Level: "info", Msg: "escrow serving", Fields: []any{"escrow", "7", "model", "qwen"}},
		},
		{
			name:    "an idle escrow is retired",
			produce: func(events *Journal) { events.EscrowRetired("7") },
			want:    logcapture.Entry{Level: "info", Msg: "escrow retired", Fields: []any{"escrow", "7"}},
		},
		{
			name:    "a busy escrow is retired",
			produce: func(events *Journal) { events.EscrowRetiredDraining("7", 3) },
			want: logcapture.Entry{Level: "info", Msg: "escrow retired, draining", Fields: []any{
				"escrow", "7", "in_flight", int64(3),
			}},
		},
		{
			name:    "a drained escrow closes",
			produce: func(events *Journal) { events.DrainingEscrowClosed("7", nil) },
			want:    logcapture.Entry{Level: "info", Msg: "draining escrow closed", Fields: []any{"escrow", "7"}},
		},
		{
			name:    "a drained escrow fails to close",
			produce: func(events *Journal) { events.DrainingEscrowClosed("7", closeFailure) },
			want: logcapture.Entry{Level: "error", Msg: "draining escrow failed to close, its storage stays held", Fields: []any{
				"escrow", "7", "error", closeFailure,
			}},
		},
		{
			name:    "a settlement would not verify",
			produce: func(events *Journal) { events.SettlementUnverifiable("5", 9, unverifiable) },
			want: logcapture.Entry{Level: "warn", Msg: "settlement signatures did not verify", Fields: []any{
				"escrow", "5", "nonce", uint64(9), "error", unverifiable,
			}},
		},
		{
			name:    "boot finds a devshard it cannot serve",
			produce: func(events *Journal) { events.EscrowUnservable("gone", missing) },
			want: logcapture.Entry{Level: "warn", Msg: "devshard cannot be served, marking inactive", Fields: []any{
				"escrow", "gone", "error", missing,
			}},
		},
		{
			name:    "an escrow fails to open and waits for a retry",
			produce: func(events *Journal) { events.EscrowUnopened("stalled", missing) },
			want: logcapture.Entry{Level: "warn", Msg: "devshard could not be opened, retrying", Fields: []any{
				"escrow", "stalled", "error", missing,
			}},
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
}

// Test flow:
//  1. Table-driven: each case pairs a lifecycle producer call (creation, recovery, hold, rotation, settlement, sweep, and more) with the exact log entry it must render, escrow id included as text.
//  2. For each case, build a journal with a `logcapture.Recorder`, run the case's `produce` function, and flush.
//  3. Assert the recorder captured exactly the case's expected entry.
func TestEscrowLifecycleTransitionsRenderTheLinesTheirProducersNarrate(t *testing.T) {
	tickFailure := errors.New("store unavailable")

	testCases := []struct {
		name    string
		produce func(events *Journal)
		want    logcapture.Entry
	}{
		{
			name:    "an escrow is created",
			produce: func(events *Journal) { events.EscrowCreated("42", "model-a", "temp", "bridge", 7, "TX-HAPPY") },
			want: logcapture.Entry{Level: "info", Msg: "escrow created", Fields: []any{
				"escrow", "42", "model", "model-a", "role", "temp", "reason", "bridge", "epoch", uint64(7), "tx", "TX-HAPPY",
			}},
		},
		{
			name:    "an escrow is recovered from its commitment",
			produce: func(events *Journal) { events.EscrowRecovered("55", "model-a", "temp", 3, "TXA") },
			want: logcapture.Entry{Level: "info", Msg: "escrow recovered from commitment", Fields: []any{
				"escrow", "55", "model", "model-a", "role", "temp", "epoch", uint64(3), "tx", "TXA",
			}},
		},
		{
			name: "a commitment is cleared",
			produce: func(events *Journal) {
				events.CommitmentCleared("TXB", "model-a", "temp", 3, "transaction created no escrow")
			},
			want: logcapture.Entry{Level: "warn", Msg: "commitment cleared", Fields: []any{
				"tx", "TXB", "model", "model-a", "role", "temp", "epoch", uint64(3), "reason", "transaction created no escrow",
			}},
		},
		{
			name:    "an escrow is gone from the chain",
			produce: func(events *Journal) { events.EscrowGoneFromChain("1") },
			want:    logcapture.Entry{Level: "warn", Msg: "escrow gone from chain, taken out of service", Fields: []any{"escrow", "1"}},
		},
		{
			name:    "an escrow is parked at its settlement deadline",
			produce: func(events *Journal) { events.EscrowDeadlineReached("5", 7, 3100) },
			want: logcapture.Entry{Level: "warn", Msg: "escrow parked at its settlement deadline", Fields: []any{
				"escrow", "5", "chain_epoch", uint64(7), "settle_by", int64(3100),
			}},
		},
		{
			name:    "an escrow's deadline passes with nothing allowed to settle it",
			produce: func(events *Journal) { events.EscrowDeadlineUnsettled("5", 3100, "settlement_disabled") },
			want: logcapture.Entry{Level: "error", Msg: "escrow deadline passes unsettled", Fields: []any{
				"escrow", "5", "settle_by", int64(3100), "reason", "settlement_disabled",
			}},
		},
		{
			name:    "a passed deadline carries no settle_by",
			produce: func(events *Journal) { events.EscrowDeadlineUnsettled("5", 0, "deadline_passed") },
			want: logcapture.Entry{Level: "error", Msg: "escrow deadline passes unsettled", Fields: []any{
				"escrow", "5", "reason", "deadline_passed",
			}},
		},
		{
			name:    "the settle margin is shorter than two settle windows",
			produce: func(events *Journal) { events.SettleMarginShort(60, time.Second, 22*time.Minute) },
			want: logcapture.Entry{Level: "warn", Msg: "settle margin shorter than two settle windows", Fields: []any{
				"margin_blocks", int64(60), "block_time_ms", int64(1000), "settle_windows_ms", int64(1_320_000),
			}},
		},
		{
			name:    "deadlines are read past a stale chain height",
			produce: func(events *Journal) { events.EscrowDeadlinesProjected(2_000, 2_100, 10*time.Minute) },
			want: logcapture.Entry{Level: "warn", Msg: "escrow deadlines read past a stale chain height", Fields: []any{
				"height", int64(2_000), "projected_height", int64(2_100), "stale_for_ms", int64(600_000),
			}},
		},
		{
			name:    "an escrow's chain epoch and amount stay unresolved",
			produce: func(events *Journal) { events.EscrowChainFactsUnresolved("1", "lookup_failed") },
			want: logcapture.Entry{Level: "warn", Msg: "escrow chain epoch and amount unresolved", Fields: []any{
				"escrow", "1", "reason", "lookup_failed",
			}},
		},
		{
			name:    "an escrow is marked for replacement",
			produce: func(events *Journal) { events.EscrowMarkedForReplacement("1", "nonce_cap") },
			want: logcapture.Entry{Level: "warn", Msg: "escrow marked for replacement", Fields: []any{
				"escrow", "1", "reason", "nonce_cap",
			}},
		},
		{
			name:    "a create is refused for an underfunded wallet",
			produce: func(events *Journal) { events.EscrowCreateUnderfunded("qwen", "reserve", 5, 101) },
			want: logcapture.Entry{Level: "warn", Msg: "escrow create refused: wallet underfunded", Fields: []any{
				"model", "qwen", "role", "reserve", "have", uint64(5), "need", uint64(101),
			}},
		},
		{
			name:    "a create is refused for an amount below the model's floor",
			produce: func(events *Journal) { events.EscrowCreateBelowFloor("qwen", "regular", 1000, 180010) },
			want: logcapture.Entry{Level: "warn", Msg: "escrow create refused: amount below the model's floor", Fields: []any{
				"model", "qwen", "role", "regular", "have", uint64(1000), "need", uint64(180010),
			}},
		},
		{
			name:    "a reserve escrow is taken",
			produce: func(events *Journal) { events.EscrowReserveTaken("1") },
			want:    logcapture.Entry{Level: "info", Msg: "reserve escrow taken", Fields: []any{"escrow", "1"}},
		},
		{
			name:    "a rotation is skipped",
			produce: func(events *Journal) { events.RotationSkipped("qwen", "regular", 4) },
			want: logcapture.Entry{Level: "warn", Msg: "rotation skipped, the network serves no such model", Fields: []any{
				"model", "qwen", "role", "regular", "epoch", uint64(4),
			}},
		},
		{
			name:    "regulars are promoted to temp",
			produce: func(events *Journal) { events.RegularsPromotedToTemp("model-a", 9, 2) },
			want: logcapture.Entry{Level: "warn", Msg: "regular escrows promoted to temp", Fields: []any{
				"model", "model-a", "epoch", uint64(9), "promoted", 2,
			}},
		},
		{
			name:    "a bridge is prepared",
			produce: func(events *Journal) { events.BridgePrepared("model-a", 9, 1, 2) },
			want: logcapture.Entry{Level: "info", Msg: "bridge prepared", Fields: []any{
				"model", "model-a", "epoch", uint64(9), "created", 1, "retired", 2,
			}},
		},
		{
			name:    "a bridge is finished",
			produce: func(events *Journal) { events.BridgeFinished("model-a", 9, 2, 1) },
			want: logcapture.Entry{Level: "info", Msg: "bridge finished", Fields: []any{
				"model", "model-a", "epoch", uint64(9), "created", 2, "retired", 1,
			}},
		},
		{
			name:    "an escrow is parked",
			produce: func(events *Journal) { events.EscrowParked("5") },
			want:    logcapture.Entry{Level: "info", Msg: "escrow parked for settlement", Fields: []any{"escrow", "5"}},
		},
		{
			name:    "an escrow is settled",
			produce: func(events *Journal) { events.EscrowSettled("5", "model-a", "SETTLE-TX", "gonka1settler") },
			want: logcapture.Entry{Level: "info", Msg: "escrow settled", Fields: []any{
				"escrow", "5", "model", "model-a", "tx", "SETTLE-TX", "settler", "gonka1settler",
			}},
		},
		{
			name:    "a settle already on chain is reconciled",
			produce: func(events *Journal) { events.SettlementReconciled("7", "SETTLE-TX") },
			want: logcapture.Entry{Level: "info", Msg: "settle already on chain, reconciled", Fields: []any{
				"escrow", "7", "tx", "SETTLE-TX",
			}},
		},
		{
			name:    "a settled record is dropped",
			produce: func(events *Journal) { events.SettledRecordDropped("5") },
			want:    logcapture.Entry{Level: "info", Msg: "settled escrow record dropped", Fields: []any{"escrow", "5"}},
		},
		{
			name:    "a tick fails",
			produce: func(events *Journal) { events.EscrowTickFailed(tickFailure) },
			want:    logcapture.Entry{Level: "error", Msg: "escrow tick failed", Fields: []any{"error", tickFailure}},
		},
		{
			name:    "a sweep found votes owed",
			produce: func(events *Journal) { events.TimeoutsSwept(4, 3, 1) },
			want: logcapture.Entry{Level: "info", Msg: "execution timeouts swept", Fields: []any{
				"swept_due", 4, "swept_applied", 3, "swept_failed", 1,
			}},
		},
		{
			name:    "a settle transaction is broadcast",
			produce: func(events *Journal) { events.SettleBroadcast("123", "ABCDEF", "gonka1settler") },
			want: logcapture.Entry{Level: "info", Msg: "settle tx broadcast", Fields: []any{
				"escrow", "123", "tx", "ABCDEF", "settler", "gonka1settler",
			}},
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
}
