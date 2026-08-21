package heightsync_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	commrc "common/runtimeconfig"

	"devshard/heightsync"
	"devshard/types"
)

func TestHeartbeatConfig_Defaults(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	require.Equal(t, 3*time.Second, cfg.Interval)
	require.Equal(t, 2*cfg.Interval, cfg.TurnTimeout,
		"a turn needs patience beyond the interval that opened it")
	require.Equal(t, heightsync.DefaultIdleMultiple*cfg.Interval, cfg.IdleTimeout,
		"a host tolerates four missed turnovers before arming")
	require.Equal(t, cfg.Interval+cfg.TurnTimeout, cfg.TurnoverBudget())
	require.Greater(t, cfg.IdleTimeout, cfg.TurnoverBudget(),
		"one lost turnover must never arm a host")
}

// TestHeartbeatConfig_AckWindowFollowsTheSchedule: D_ack is not a shipped
// constant but the millisecond schedule expressed in the only unit the log can
// check (proposal §20). The old constant of one block was shorter than the
// turnover it was meant to cover at every block time we ship.
func TestHeartbeatConfig_AckWindowFollowsTheSchedule(t *testing.T) {
	cfg := heightsync.DefaultHeartbeatConfig()
	require.Equal(t, time.Second, cfg.BlockTime, "the assumption is explicit, not implicit")
	require.Equal(t, uint64(10), cfg.AckDeadlineBlocks,
		"9s of turnover budget at 1s blocks, plus the boundary block")
	require.GreaterOrEqual(t, cfg.AckWindow(), cfg.TurnoverBudget(),
		"the log must not disown a turn its producer is still working on")
	require.NoError(t, cfg.Validate(heightsync.DefaultOriginatorFreshness))

	// Slower blocks buy the same wall clock with fewer of them.
	require.Equal(t, uint64(3), heightsync.AckDeadlineBlocksFor(9*time.Second, 5*time.Second))
	slow := heightsync.HeartbeatConfig{BlockTime: 5 * time.Second}
	require.NoError(t, slow.Validate(heightsync.DefaultOriginatorFreshness))
	require.Equal(t, 15*time.Second, slow.AckWindow())

	// A longer interval carries the window with it: nothing is left behind at an
	// absolute default that the schedule has outgrown.
	slower := heightsync.HeartbeatConfig{Interval: 10 * time.Second}
	require.Equal(t, 30*time.Second, slower.TurnoverBudget())
	require.Equal(t, 31*time.Second, slower.AckWindow())
	require.NoError(t, slower.Validate(90*time.Second))
}

func TestHeartbeatConfig_ValidateRejectsBadOverride(t *testing.T) {
	ok := heightsync.DefaultHeartbeatConfig()
	require.NoError(t, ok.Validate(heightsync.DefaultOriginatorFreshness))

	badIdle := ok
	badIdle.IdleTimeout = ok.Interval + ok.TurnTimeout // not strictly greater
	require.Error(t, badIdle.Validate(heightsync.DefaultOriginatorFreshness))

	// Overriding the interval alone stays valid: the derived knobs follow it
	// instead of being left behind at an absolute default.
	slower := heightsync.HeartbeatConfig{Interval: 5 * time.Second}
	require.NoError(t, slower.Validate(heightsync.DefaultOriginatorFreshness))

	// The pre-step-4 shipped value, now rejected on the shipped schedule: two
	// blocks of window against nine seconds of turnover budget is the mismatch
	// that flagged honest acks late and fired repair probes in steady state.
	dAck2 := ok
	dAck2.AckDeadlineBlocks = 2
	err := dAck2.Validate(heightsync.DefaultOriginatorFreshness)
	require.ErrorContains(t, err, "ack window")
	require.ErrorContains(t, err, "9s")

	// The same D_ack is fine where blocks are slow enough to mean it.
	dAck2Slow := dAck2
	dAck2Slow.BlockTime = 8 * time.Second
	require.NoError(t, dAck2Slow.Validate(heightsync.DefaultOriginatorFreshness))

	badCadence := ok
	badCadence.Interval = 40 * time.Second // 2 * 40s = 80s > F = 60s
	badCadence.IdleTimeout = 5 * time.Minute
	require.Error(t, badCadence.Validate(60*time.Second))
}

func TestHeartbeatConfig_FromSnapshotZeroUsesDefaults(t *testing.T) {
	got := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{})
	require.Equal(t, heightsync.DefaultHeartbeatConfig(), got)

	// Scheduling knobs overlay; evaluation knobs stay compiled. IntervalMs=2000
	// derives TurnTimeout/IdleTimeout, keeps the compiled D_ack=10, and still
	// passes Validate (6s budget inside a 10s window, 8s idle > 6s).
	overlay := heightsync.OverlayHeartbeatConfig(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{
			IntervalMs: 2000, BlockTimeMs: 6000, AckDeadlineBlocks: 7,
		},
	})
	require.False(t, overlay.Clamped)
	require.Equal(t, 2*time.Second, overlay.Config.Interval)
	require.Equal(t, 4*time.Second, overlay.Config.TurnTimeout, "turn timeout follows the overlay interval")
	require.Equal(t, 8*time.Second, overlay.Config.IdleTimeout, "T_idle follows the overlay interval")
	compiled := heightsync.DefaultHeartbeatConfig()
	require.Equal(t, compiled.AckDeadlineBlocks, overlay.Config.AckDeadlineBlocks,
		"D_ack is log-pure: a snapshot must not change Late flags")
	require.Equal(t, compiled.BlockTime, overlay.Config.BlockTime, "BlockTimeMs is ignored")
	require.Equal(t, compiled.DeltaBlocks, overlay.Config.DeltaBlocks)
	require.Equal(t, compiled.WindowBlocks, overlay.Config.WindowBlocks)
	require.NoError(t, overlay.Config.Validate(heightsync.DefaultOriginatorFreshness))

	explicit := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{
			IntervalMs: 2000, TurnTimeoutMs: 3000, IdleTimeoutMs: 9000,
		},
	})
	require.Equal(t, 2*time.Second, explicit.Interval)
	require.Equal(t, 3*time.Second, explicit.TurnTimeout)
	require.Equal(t, 9*time.Second, explicit.IdleTimeout)
	require.Equal(t, compiled.AckDeadlineBlocks, explicit.AckDeadlineBlocks)
}

func TestHeartbeatConfig_InvalidOverlayIsClamped(t *testing.T) {
	before := heightsync.OverlayClampCount()
	got := heightsync.OverlayHeartbeatConfig(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{IntervalMs: 8000},
	})
	require.True(t, got.Clamped, "8s interval ⇒ 24s budget against a compiled 10s window")
	require.Contains(t, got.Reason, "ack window")
	require.Equal(t, heightsync.DefaultHeartbeatConfig(), got.Config,
		"an invalid overlay must not ship")
	require.Equal(t, before+1, heightsync.OverlayClampCount())
	require.Contains(t, heightsync.LastOverlayClampReason(), "ack window")
}

func TestRepairConfig_FromSnapshot(t *testing.T) {
	got := heightsync.RepairConfigFromSnapshot(commrc.Snapshot{})
	require.Equal(t, heightsync.DefaultRepairStagger, got.Stagger)
	require.Zero(t, got.MaxProbesPerWindow)

	got = heightsync.RepairConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{ProbeStaggerMs: 250, MaxProbesPerWindow: 7},
	})
	require.Equal(t, 250*time.Millisecond, got.Stagger)
	require.Equal(t, 7, got.MaxProbesPerWindow)
}

func TestDevshardTx_HeartbeatFieldNumbers(t *testing.T) {
	md := (&types.DevshardTx{}).ProtoReflect().Descriptor()
	assertFieldNum(t, md, "heartbeat", 10)
	assertFieldNum(t, md, "height_ack", 11)

	reserved := md.ReservedRanges()
	require.True(t, reserved.Has(12) && reserved.Has(13), "oneof numbers 12 and 13 must be reserved for cPoC")

	hb := &types.MsgHeartbeat{TurnSeq: 1, ObservedHeight: 9, SlotsNum: 4, Reason: "quiet_session"}
	ack := &types.MsgHeightAck{TurnSeq: 1, RefNonce: 10, SlotId: 2, ObservedHeight: 9, SyncState: types.SyncState_SYNCED}
	raw, err := proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: hb}})
	require.NoError(t, err)
	var decoded types.DevshardTx
	require.NoError(t, proto.Unmarshal(raw, &decoded))
	require.Equal(t, hb.TurnSeq, decoded.GetHeartbeat().GetTurnSeq())

	raw, err = proto.Marshal(&types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}})
	require.NoError(t, err)
	decoded = types.DevshardTx{}
	require.NoError(t, proto.Unmarshal(raw, &decoded))
	require.Equal(t, ack.SlotId, decoded.GetHeightAck().GetSlotId())
}

func assertFieldNum(t *testing.T, md protoreflect.MessageDescriptor, name string, want protoreflect.FieldNumber) {
	t.Helper()
	fd := md.Fields().ByName(protoreflect.Name(name))
	require.NotNil(t, fd, "missing field %s", name)
	require.Equal(t, want, fd.Number(), "field %s", name)
}
