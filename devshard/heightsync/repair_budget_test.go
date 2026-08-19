package heightsync

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestRepairProbe_BudgetAndStagger(t *testing.T) {
	const (
		turn uint64 = 1
		hNow uint64 = 502
	)
	cfg := RepairConfig{Stagger: time.Second, MaxProbesPerWindow: 2}
	b := NewRepairBudget(cfg, 3, 0, 1)

	delay, skip := b.Begin(turn, 1, hNow, false)
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, 2*time.Second, delay) // (V=0, j=1, n=3) → 2·δ

	landed := false
	b.SetClock(time.Now, func(time.Duration) { landed = true })
	b.Sleep(delay)
	require.True(t, landed)
	require.Equal(t, RepairSkipAckLanded, b.AfterWait(turn, 1, landed))
	require.Equal(t, 1, b.Count(string(RepairSkipAckLanded)))
	require.Zero(t, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))

	// Same (turn, slot) is not retried.
	_, skip = b.Begin(turn, 1, hNow, false)
	require.Equal(t, RepairSkipProbed, skip)

	// Two unicasts fill R_max=2; a third is budget-exhausted.
	_, skip = b.Begin(turn, 2, hNow, false)
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, RepairSkipNone, b.AfterWait(turn, 2, false))
	b.Record(turn, 2, RepairOutcomeHeight)

	_, skip = b.Begin(2, 1, hNow, false) // different turn, same window
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, RepairSkipNone, b.AfterWait(2, 1, false))
	b.Record(2, 1, RepairOutcomeHeight)

	_, skip = b.Begin(2, 2, hNow, false)
	require.Equal(t, RepairSkipBudget, skip)
	require.Equal(t, 1, b.Count(string(RepairSkipBudget)))
	require.Equal(t, 2, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))
}

func TestRepairProbe_ArmedHostStopsProbing(t *testing.T) {
	b := NewRepairBudget(RepairConfig{Stagger: 0, MaxProbesPerWindow: 8}, 4, 0, 1)
	delay, skip := b.Begin(1, 1, 502, true)
	require.Equal(t, RepairSkipArmed, skip)
	require.Zero(t, delay)
	require.Equal(t, 1, b.Count(string(RepairSkipArmed)))
	require.Zero(t, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))

	_, skip = b.Begin(1, 2, 502, true)
	require.Equal(t, RepairSkipArmed, skip)
}

func TestRepairBudget_BackoffSkipsRetry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewRepairBudget(RepairConfig{Stagger: time.Second}, 2, 0, 1)
	b.SetClock(func() time.Time { return now }, func(time.Duration) {})

	_, skip := b.Begin(1, 1, 502, false)
	require.Equal(t, RepairSkipNone, skip)
	b.Record(1, 1, RepairOutcomeUnreachable)
	require.Equal(t, 1, b.FailCount(1))
	require.True(t, b.InBackoff(1))

	_, skip = b.Begin(2, 1, 503, false)
	require.Equal(t, RepairSkipBackoff, skip)
}

func TestProbeStagger_PositiveMod(t *testing.T) {
	require.Equal(t, time.Duration(0), ProbeStagger(0, 0, 3, time.Second))
	require.Equal(t, 2*time.Second, ProbeStagger(0, 1, 3, time.Second))
	require.Equal(t, time.Second, ProbeStagger(0, 2, 3, time.Second))
	require.Zero(t, ProbeStagger(1, 1, 3, time.Second))
}

func TestMissingAcksDue_RequiresWindowClosed(t *testing.T) {
	tr := NewTurnTracker(4, 3, DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, SlotsNum: 4,
		}},
	}}, 500)
	require.Nil(t, tr.MissingAcksDue(1, 500), "window still open at h_req")
	require.Equal(t, []uint32{0, 1, 2, 3}, tr.MissingAcks(1))
	tr.AdvanceHeight(502)
	require.Equal(t, []uint32{0, 1, 2, 3}, tr.MissingAcksDue(1, 502))
}
