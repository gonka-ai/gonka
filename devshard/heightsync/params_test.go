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
	require.Equal(t, cfg.Interval, cfg.TurnTimeout)
	require.Equal(t, heightsync.DefaultIdleMultiple*cfg.Interval, cfg.IdleTimeout,
		"a host tolerates four missed turnovers before arming")
	require.Greater(t, cfg.IdleTimeout, cfg.Interval+cfg.TurnTimeout,
		"one lost turnover must never arm a host")
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

	dAck2 := ok
	dAck2.AckDeadlineBlocks = 2
	require.NoError(t, dAck2.Validate(heightsync.DefaultOriginatorFreshness))

	badCadence := ok
	badCadence.Interval = 40 * time.Second // 2 * 40s = 80s > F = 60s
	badCadence.IdleTimeout = 5 * time.Minute
	require.Error(t, badCadence.Validate(60*time.Second))
}

func TestHeartbeatConfig_FromSnapshotZeroUsesDefaults(t *testing.T) {
	got := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{})
	require.Equal(t, heightsync.DefaultHeartbeatConfig(), got)

	overlay := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{IntervalMs: 8000, AckDeadlineBlocks: 2},
	})
	require.Equal(t, 8*time.Second, overlay.Interval)
	require.Equal(t, 8*time.Second, overlay.TurnTimeout, "turn timeout follows the interval")
	require.Equal(t, 32*time.Second, overlay.IdleTimeout, "T_idle follows the interval")
	require.Equal(t, uint64(2), overlay.AckDeadlineBlocks)
	require.Equal(t, heightsync.DefaultSyncDeltaBlocks, overlay.DeltaBlocks)
	require.NoError(t, overlay.Validate(heightsync.DefaultOriginatorFreshness))

	explicit := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{
			IntervalMs: 2000, TurnTimeoutMs: 1500, IdleTimeoutMs: 9000,
		},
	})
	require.Equal(t, 2*time.Second, explicit.Interval)
	require.Equal(t, 1500*time.Millisecond, explicit.TurnTimeout)
	require.Equal(t, 9*time.Second, explicit.IdleTimeout)
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
