package heightsync_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestAnchorTally_LateAckDoesNotLeakOpen(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.ObserveTip(100)
	tal.Record(10, heightsync.AnchorKindResponse)
	tal.RecordTurnover(10)
	require.Zero(t, tal.OpenLen(), "late Record below next must not grow open")
	require.Equal(t, uint64(2), tal.Late())

	_, _, _, late, future, anchorsBefore, _ := tal.Snapshot()
	require.Equal(t, uint64(2), late)
	require.Zero(t, future)

	tal.Record(10, heightsync.AnchorKindResponse)
	_, _, _, _, _, anchorsAfter, _ := tal.Snapshot()
	require.Equal(t, anchorsBefore.Count, anchorsAfter.Count,
		"late claims are not this block's anchors")
}

func TestAnchorTally_OpenBoundedBeforeFirstTip(t *testing.T) {
	// A courier gateway with no oracle of its own never calls ObserveTip, so
	// nothing seals and nothing refuses a claim on height grounds. Hosts pick
	// the height in every ack, so only the bucket budget stands between them
	// and unbounded growth.
	tal := heightsync.NewAnchorTally(2, 32)
	for h := uint64(1); h <= 5000; h++ {
		tal.Record(h, heightsync.AnchorKindResponse)
	}
	open := tal.OpenLen()
	require.LessOrEqual(t, open, 128, "provisional buckets must stay bounded with no tip")
	require.Positive(t, open)
	require.Equal(t, uint64(5000-open), tal.Future(), "refused claims stay on the record")

	// Heights already open keep counting: the budget refuses new buckets, it
	// does not stop the tally.
	tal.Record(1, heightsync.AnchorKindResponse)
	require.Equal(t, open, tal.OpenLen())

	tal.ObserveTip(4000)
	require.LessOrEqual(t, tal.OpenLen(), open, "a tip must not widen the window")
}

func TestAnchorTally_LateAfterTipFirst(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.ObserveTip(100)
	tal.Record(10, heightsync.AnchorKindHeartbeat)
	require.Zero(t, tal.OpenLen())
	require.Equal(t, uint64(1), tal.Late())
}

func TestAnchorTally_LargeTipJumpIsBounded(t *testing.T) {
	// Tip must initialize next before Record so a large jump still
	// fast-forwards from a low cursor (startLocked is tip-only).
	tal := heightsync.NewAnchorTally(2, 32)
	tal.ObserveTip(1)
	tal.Record(1, heightsync.AnchorKindResponse)
	tal.ObserveTip(1_000_000)

	require.Zero(t, tal.OpenLen(), "open stays O(D_ack), not O(Δheight)")
	last, debug, without, _, _, anchors, _ := tal.Snapshot()
	require.NotNil(t, last)
	require.LessOrEqual(t, len(debug), 32, "debug ring bounded by retain")
	require.Equal(t, uint64(999_997), without,
		"empty heights from 2..999998 are without; height 1 had an anchor")
	require.Equal(t, uint64(999_998), last.Height)
	// Fast-forward folds open buckets into the hist and seals at most retain
	// heights with full samples; skipped empties are only on `without`.
	require.Equal(t, uint64(33), anchors.Count, "1 folded open + retain sealed empties")
}

func TestAnchorTally_HostClaimDoesNotPinNext(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.Record(1_000_000, heightsync.AnchorKindResponse)
	tal.ObserveTip(100)

	require.Zero(t, tal.OpenLen(), "absurd pre-tip claim must not remain open")
	require.Equal(t, uint64(1), tal.Future())
	require.Zero(t, tal.Late())

	// Legitimate near-tip claim still buckets.
	tal.Record(100, heightsync.AnchorKindResponse)
	require.Equal(t, 1, tal.OpenLen())
	require.Equal(t, uint64(1), tal.Future())
}

func TestAnchorTally_FutureAboveTipRefused(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.ObserveTip(100)
	tal.Record(1000, heightsync.AnchorKindResponse)
	tal.RecordTurnover(1000)
	require.Zero(t, tal.OpenLen())
	require.Equal(t, uint64(2), tal.Future())
	require.Zero(t, tal.Late())

	// tip+D_ack is still accepted.
	tal.Record(102, heightsync.AnchorKindHeartbeat)
	require.Equal(t, 1, tal.OpenLen())
}

func TestAnchorTally_LateAndFutureOnOperatorViewJSON(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.ObserveTip(100)
	tal.Record(10, heightsync.AnchorKindResponse)
	tal.Record(1000, heightsync.AnchorKindResponse)

	_, _, _, late, future, _, _ := tal.Snapshot()
	view := heightsync.OperatorView{
		DevshardID:    "escrow-1",
		AnchorsLate:   late,
		AnchorsFuture: future,
	}
	raw, err := json.Marshal(view)
	require.NoError(t, err)
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Equal(t, float64(1), decoded["anchors_late"])
	require.Equal(t, float64(1), decoded["anchors_future"])
}
