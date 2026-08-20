package heightsync

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

func TestRepairProbe_BudgetAndStagger(t *testing.T) {
	const turn uint64 = 1
	now := time.Unix(1_700_000_000, 0)
	cfg := RepairConfig{Stagger: time.Second, MaxProbesPerWindow: 2}
	b := NewRepairBudget(cfg, 3, 0, DefaultHeartbeatInterval)
	landed := false
	b.SetClock(func() time.Time { return now }, func(time.Duration) { landed = true })

	delay, skip := b.Begin(turn, 1, false)
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, 2*time.Second, delay) // (V=0, j=1, n=3) → 2·δ

	b.Sleep(context.Background(), delay)
	require.True(t, landed)
	require.Equal(t, RepairSkipAckLanded, b.AfterWait(turn, 1, landed))
	require.Equal(t, 1, b.Count(string(RepairSkipAckLanded)))
	require.Zero(t, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))

	// Same (turn, slot) is not retried.
	_, skip = b.Begin(turn, 1, false)
	require.Equal(t, RepairSkipProbed, skip)

	// Two unicasts fill R_max=2; a third is budget-exhausted.
	_, skip = b.Begin(turn, 2, false)
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, RepairSkipNone, b.AfterWait(turn, 2, false))
	b.Record(turn, 2, RepairOutcomeHeight)

	_, skip = b.Begin(2, 1, false) // different turn, same window
	require.Equal(t, RepairSkipNone, skip)
	require.Equal(t, RepairSkipNone, b.AfterWait(2, 1, false))
	b.Record(2, 1, RepairOutcomeHeight)

	_, skip = b.Begin(2, 2, false)
	require.Equal(t, RepairSkipBudget, skip)
	require.Equal(t, 1, b.Count(string(RepairSkipBudget)))
	require.Equal(t, 2, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))
}

func TestRepairBudget_WindowRollsOnElapsedTime(t *testing.T) {
	// A prober that is missing acks is learning no heights, so only elapsed
	// time can refill R_max.
	now := time.Unix(1_700_000_000, 0)
	b := NewRepairBudget(RepairConfig{Stagger: 0, MaxProbesPerWindow: 1}, 3, 0, 2*time.Second)
	b.SetClock(func() time.Time { return now }, func(time.Duration) {})

	_, skip := b.Begin(1, 1, false)
	require.Equal(t, RepairSkipNone, skip)
	b.Record(1, 1, RepairOutcomeHeight)

	_, skip = b.Begin(2, 1, false)
	require.Equal(t, RepairSkipBudget, skip)

	now = now.Add(2 * time.Second)
	_, skip = b.Begin(2, 1, false)
	require.Equal(t, RepairSkipNone, skip)
}

func TestRepairProbe_ArmedHostStopsProbing(t *testing.T) {
	b := NewRepairBudget(RepairConfig{Stagger: 0, MaxProbesPerWindow: 8}, 4, 0, DefaultHeartbeatInterval)
	delay, skip := b.Begin(1, 1, true)
	require.Equal(t, RepairSkipArmed, skip)
	require.Zero(t, delay)
	require.Equal(t, 1, b.Count(string(RepairSkipArmed)))
	require.Zero(t, b.Count(RepairOutcomeHeight))
	require.Zero(t, b.Count(RepairOutcomeUnreachable))

	_, skip = b.Begin(1, 2, true)
	require.Equal(t, RepairSkipArmed, skip)
}

func TestRepairBudget_BackoffSkipsRetry(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewRepairBudget(RepairConfig{Stagger: time.Second}, 2, 0, DefaultHeartbeatInterval)
	b.SetClock(func() time.Time { return now }, func(time.Duration) {})

	_, skip := b.Begin(1, 1, false)
	require.Equal(t, RepairSkipNone, skip)
	b.Record(1, 1, RepairOutcomeUnreachable)
	require.Equal(t, 1, b.FailCount(1))
	require.True(t, b.InBackoff(1))

	_, skip = b.Begin(2, 1, false)
	require.Equal(t, RepairSkipBackoff, skip)
}

func TestProbeStagger_PositiveMod(t *testing.T) {
	require.Equal(t, time.Duration(0), ProbeStagger(0, 0, 3, time.Second))
	require.Equal(t, 2*time.Second, ProbeStagger(0, 1, 3, time.Second))
	require.Equal(t, time.Second, ProbeStagger(0, 2, 3, time.Second))
	require.Zero(t, ProbeStagger(1, 1, 3, time.Second))
}

func TestRepairResponderBudget_OnePerTurnSlotAndWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	b := NewRepairResponderBudget(RepairConfig{MaxProbesPerWindow: 2}, 3, 2*time.Second)
	b.SetClock(func() time.Time { return now })

	require.True(t, b.Allow(1, 0))
	require.False(t, b.Allow(1, 0), "one HEIGHT per (turn, requester)")
	require.Equal(t, 1, b.Count(string(RepairSkipProbed)))

	require.True(t, b.Allow(1, 1), "different requester is a new pair")
	require.False(t, b.Allow(2, 0), "R_max=2 fills the window")
	require.Equal(t, 1, b.Count(string(RepairSkipBudget)))
	require.Equal(t, 2, b.Count(RepairOutcomeHeight))

	now = now.Add(2 * time.Second)
	require.True(t, b.Allow(2, 0), "elapsed Interval refills R_max")
	require.False(t, b.Allow(1, 0), "already-served (turn, slot) is not retried")
}

func TestMissingAcksDue_RequiresWindowClosed(t *testing.T) {
	cfg := DefaultHeartbeatConfig()
	tr := NewTurnTracker(4, 3, cfg)
	tr.Observe(10, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, ObservedBlockHash: []byte{0xaa}, SlotsNum: 4,
		}},
	}}, 500)
	require.Nil(t, tr.MissingAcksDue(1, 500), "window still open at h_req")
	// Probing while the producer is still collecting acks is the steady-state
	// waste step 4 removed: the window has to outlast the turnover budget first.
	inside := 500 + cfg.AckDeadlineBlocks
	require.Nil(t, tr.MissingAcksDue(1, inside), "still inside the turnover budget")
	require.Equal(t, []uint32{0, 1, 2, 3}, tr.MissingAcks(1))
	tr.AdvanceHeight(inside + 1)
	require.Equal(t, []uint32{0, 1, 2, 3}, tr.MissingAcksDue(1, inside+1))
	require.Equal(t, []uint32{0, 1, 2, 3}, tr.MissingAcksDue(1, 0),
		"a log-driven degrade is due even when h_last has not moved")
}

func TestRepairBudget_PruneBoundsMap(t *testing.T) {
	b := NewRepairBudget(RepairConfig{Stagger: 0, MaxProbesPerWindow: 1 << 20}, 3, 0, time.Hour)
	b.SetClock(func() time.Time { return time.Unix(1, 0) }, func(time.Duration) {})
	const extra = 20
	maxTurn := DefaultTurnRetain + extra
	for turn := uint64(1); turn <= maxTurn; turn++ {
		for slot := uint32(1); slot < 3; slot++ {
			_, skip := b.Begin(turn, slot, false)
			require.Equal(t, RepairSkipNone, skip, "turn %d slot %d", turn, slot)
			b.Record(turn, slot, RepairOutcomeHeight)
		}
	}
	require.LessOrEqual(t, b.ProbedCount(), int(DefaultTurnRetain)*3+3)

	rb := NewRepairResponderBudget(RepairConfig{MaxProbesPerWindow: 1 << 20}, 3, time.Hour)
	rb.SetClock(func() time.Time { return time.Unix(1, 0) })
	for turn := uint64(1); turn <= maxTurn; turn++ {
		require.True(t, rb.Allow(turn, 1))
	}
	require.LessOrEqual(t, rb.ServedCount(), int(DefaultTurnRetain)+1)
}

func TestRepairDueAll_IncludesDegradedOlderTurn(t *testing.T) {
	cfg := DefaultHeartbeatConfig()
	tr := NewTurnTracker(2, 2, cfg)
	hash := []byte{0xaa}
	tr.Observe(1, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
		}},
	}}, 500)
	tr.Observe(2, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 1, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
		}},
	}}, 500)
	past := 500 + cfg.AckDeadlineBlocks + 1
	tr.Observe(3, []*types.DevshardTx{{
		Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
			InferenceId: 3, ObservedHeight: past, ObservedBlockHash: hash,
		}},
	}}, 0)
	tr.Observe(4, []*types.DevshardTx{{
		Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
			TurnSeq: 2, ObservedHeight: 500, ObservedBlockHash: hash, SlotsNum: 2,
		}},
	}}, 500)
	due := tr.RepairDueAll()
	var turns []uint64
	for _, d := range due {
		turns = append(turns, d.TurnSeq)
	}
	require.Contains(t, turns, uint64(1), "turn 1 must still be probed after turn 2 opened")
}

func TestRepairBudget_SleepRespectsCancel(t *testing.T) {
	b := NewRepairBudget(RepairConfig{Stagger: time.Hour}, 2, 0, DefaultHeartbeatInterval)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := b.Sleep(ctx, time.Hour)
	require.ErrorIs(t, err, context.Canceled)
	require.Less(t, time.Since(start), 200*time.Millisecond)
}
