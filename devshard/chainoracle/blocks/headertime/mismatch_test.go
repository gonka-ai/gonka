package headertime

import (
	"bytes"
	"log/slog"
	"testing"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestRecordClockMismatchIncrementsAndWarns(t *testing.T) {
	before := promtestutil.ToFloat64(TimeoutClockMismatchCounter().WithLabelValues("gateway", "refused"))
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	RecordClockMismatch("gateway", "refused", "escrow-b7", 9, DueEval{
		Active:    DueSide{Due: true, Wall: true},
		Shadow:    DueSide{Due: false},
		HasShadow: true,
		Mismatch:  true,
		Cause:     "stamp_late",
	}, 1_700_000_000, 1_800_000_000_000, 42)

	require.Equal(t, before+1, promtestutil.ToFloat64(TimeoutClockMismatchCounter().WithLabelValues("gateway", "refused")))
	text := buf.String()
	require.Contains(t, text, "timeout clock mismatch")
	require.Contains(t, text, `"side":"gateway"`)
	require.Contains(t, text, `"reason":"refused"`)
	require.Contains(t, text, `"escrow":"escrow-b7"`)
	require.Contains(t, text, `"nonce":9`)
	require.Contains(t, text, `"wall_due":true`)
	require.Contains(t, text, `"stamp_due":false`)
	require.Contains(t, text, `"wall_anchor":1700000000`)
	require.Contains(t, text, `"stamp_ms":1800000000000`)
	require.Contains(t, text, `"height":42`)
	require.Contains(t, text, `"mismatch_cause":"stamp_late"`)

	snap := TimeoutClockDebug()
	require.GreaterOrEqual(t, snap.MismatchTotal, before+1)
	require.NotNil(t, snap.LastMismatch)
	require.Equal(t, "escrow-b7", snap.LastMismatch.Escrow)
	require.Equal(t, uint64(9), snap.LastMismatch.Nonce)
	require.Equal(t, "refused", snap.LastMismatch.Reason)
	require.True(t, snap.LastMismatch.WallDue)
	require.False(t, snap.LastMismatch.StampDue)
	require.Equal(t, int64(1_700_000_000), snap.LastMismatch.WallAnchor)
	require.Equal(t, int64(1_800_000_000_000), snap.LastMismatch.StampMs)
	require.Equal(t, "stamp_late", snap.LastMismatch.MismatchCause)

	missingBefore := promtestutil.ToFloat64(TimeoutClockMismatchCounter().WithLabelValues("gateway", "refused"))
	RecordClockMismatch("gateway", "refused", "escrow-b7", 9, DueEval{
		Active:    DueSide{Due: true, Wall: true},
		Shadow:    DueSide{Due: false},
		HasShadow: true,
		Cause:     "stamp_missing",
	}, 1_700_000_000, 0, 42)
	require.Equal(t, missingBefore, promtestutil.ToFloat64(TimeoutClockMismatchCounter().WithLabelValues("gateway", "refused")))
	require.Contains(t, buf.String(), `"mismatch_cause":"stamp_missing"`)
	require.Equal(t, "stamp_missing", TimeoutClockDebug().LastMismatch.MismatchCause)
}
