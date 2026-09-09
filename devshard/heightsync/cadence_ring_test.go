package heightsync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
)

func TestAnchorTally_SealsAfterDAckAndCountsEmpty(t *testing.T) {
	tal := heightsync.NewAnchorTally(2, 32)
	tal.Record(10, heightsync.AnchorKindResponse)
	tal.Record(10, heightsync.AnchorKindResponse)
	tal.ObserveTip(11)
	last, debug, without, _, _, _, _ := tal.Snapshot()
	require.Nil(t, last, "height 10 must stay open while tip is only 11")
	require.Empty(t, debug)
	require.Zero(t, without)

	tal.ObserveTip(12)
	last, debug, without, _, _, anchors, _ := tal.Snapshot()
	require.NotNil(t, last)
	require.Equal(t, uint64(10), last.Height)
	require.Equal(t, 2, last.ByKind[heightsync.AnchorKindResponse])
	require.Equal(t, uint64(1), anchors.Count)
	require.Zero(t, without)

	tal.ObserveTip(13)
	_, _, without, _, _, _, _ = tal.Snapshot()
	require.Equal(t, uint64(1), without, "height 11 had no anchors and seals at tip 13")
}

func TestCadenceRing_RecordsDischargedOncePerInterval(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(3, 2)
	t0 := time.Unix(1_700_000_000, 0)

	require.False(t, hb.NoteStamp(0, t0))
	require.True(t, hb.NoteStamp(1, t0))
	require.True(t, hb.LastTurnoverFromStamp())

	due, _ := hb.Due(t0.Add(time.Second), 50)
	require.False(t, due)
	require.True(t, hb.MaybeRecordDischarged(t0.Add(time.Second), 50))

	events, counts := hb.CadenceSnapshot()
	require.Equal(t, uint64(1), counts[string(heightsync.CadenceDischargedByInference)])
	require.Equal(t, heightsync.CadenceDischargedByInference, events[0].Event)
	require.Equal(t, 1, hb.SkippedRealTraffic())
	require.False(t, hb.MaybeRecordDischarged(t0.Add(2*time.Second), 50), "rate-limited inside Interval")
	require.Equal(t, 2, hb.SkippedRealTraffic(), "rate-limit does not drop the skip counter")
}

func TestCadenceRing_InFlightTurnIsNotDischarge(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(3, 2)
	t0 := time.Unix(1_700_000_000, 0)

	require.False(t, hb.OpenTurn(t0))
	require.False(t, hb.NoteStamp(0, t0), "below Q: turn stays open")
	due, _ := hb.Due(t0.Add(time.Second), 50)
	require.False(t, due, "open turn suppresses a new span")
	require.False(t, hb.MaybeRecordDischarged(t0.Add(time.Second), 50),
		"an in-flight turn is not discharged_by_inference")
	require.Zero(t, hb.SkippedRealTraffic())

	_, counts := hb.CadenceSnapshot()
	require.Zero(t, counts[string(heightsync.CadenceDischargedByInference)])
}

func TestCadenceRing_SkipNotSuppressedByHeartbeatOpened(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	t0 := time.Unix(1_700_000_000, 0)

	hb.RecordCadence(heightsync.CadenceEvent{
		At:    t0,
		Event: heightsync.CadenceHeartbeatOpened,
	})
	due, reason := hb.Due(t0, 0)
	require.False(t, due)
	require.Equal(t, heightsync.ReasonNoHeight, reason)

	events, counts := hb.CadenceSnapshot()
	require.Equal(t, uint64(1), counts[string(heightsync.CadenceHeartbeatOpened)])
	require.Equal(t, uint64(1), counts[string(heightsync.CadenceSkippedNoHeight)],
		"heartbeat_opened must not consume the skip limiter")
	require.Equal(t, heightsync.CadenceSkippedNoHeight, events[1].Event)

	due, _ = hb.Due(t0.Add(time.Second), 0)
	require.False(t, due)
	events, counts = hb.CadenceSnapshot()
	require.Equal(t, uint64(2), counts[string(heightsync.CadenceSkippedNoHeight)],
		"the counter tracks every skip; only the ring is rate-limited")
	require.Len(t, events, 2, "a same-kind skip inside Interval stays out of the ring")
	require.Equal(t, 2, hb.SkippedNoHeight())
}

func TestCadenceRing_RealTrafficCounterEveryDueCheck(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	hb := heightsync.NewHeartbeat(cfg)
	hb.SetRoster(3, 2)
	t0 := time.Unix(1_700_000_000, 0)
	require.False(t, hb.NoteStamp(0, t0))
	require.True(t, hb.NoteStamp(1, t0))

	var recorded int
	for i := 0; i < 3; i++ {
		at := t0.Add(time.Second + time.Duration(i)*time.Millisecond)
		due, _ := hb.Due(at, 50)
		require.False(t, due)
		if hb.MaybeRecordDischarged(at, 50) {
			recorded++
		}
	}
	require.Equal(t, 1, recorded, "ring and log line stay at one sample per Interval")
	require.Equal(t, 3, hb.SkippedRealTraffic(), "Prometheus denominator matches every due-check")
	events, counts := hb.CadenceSnapshot()
	require.Equal(t, uint64(3), counts[string(heightsync.CadenceDischargedByInference)],
		"cadence_events_total counts every discharge, or the savings ratio reads as one per Interval")
	require.Len(t, events, 1, "the debug ring is still sampled")
}

func TestCadenceRing_OpenTurnRecordsAbandoned(t *testing.T) {
	hb := heightsync.NewHeartbeat(heightsync.DefaultHeartbeatConfig())
	t0 := time.Unix(1_700_000_000, 0)
	require.False(t, hb.OpenTurn(t0))
	require.True(t, hb.OpenTurn(t0.Add(time.Second)))
	require.Equal(t, 1, hb.AbandonedTurns())
}
