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

func TestHeartbeatConfig_ValidateRejectsBadOverride(t *testing.T) {
	ok := heightsync.DefaultHeartbeatConfig()
	require.NoError(t, ok.Validate(heightsync.DefaultAssumedBlockTime, heightsync.DefaultOriginatorFreshness))

	badIdle := ok
	badIdle.IdleBlocks = ok.IntervalBlocks + ok.AckDeadlineBlocks // not strictly greater
	require.Error(t, badIdle.Validate(heightsync.DefaultAssumedBlockTime, heightsync.DefaultOriginatorFreshness))

	badRounds := ok
	badRounds.MinRoundsPerBlock = 1
	require.Error(t, badRounds.Validate(heightsync.DefaultAssumedBlockTime, heightsync.DefaultOriginatorFreshness))

	dAck2 := ok
	dAck2.AckDeadlineBlocks = 2
	dAck2.IdleBlocks = 4
	require.NoError(t, dAck2.Validate(heightsync.DefaultAssumedBlockTime, heightsync.DefaultOriginatorFreshness))

	badCadence := ok
	badCadence.IntervalBlocks = 20 // 20 * 6s = 120s > F/2 = 30s
	require.Error(t, badCadence.Validate(6*time.Second, 60*time.Second))
}

func TestHeartbeatConfig_FromSnapshotZeroUsesDefaults(t *testing.T) {
	got := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{})
	require.Equal(t, heightsync.DefaultHeartbeatConfig(), got)

	overlay := heightsync.HeartbeatConfigFromSnapshot(commrc.Snapshot{
		HeightSync: commrc.HeightSyncParams{IntervalBlocks: 8, AckDeadlineBlocks: 2, IdleBlocks: 20},
	})
	require.Equal(t, uint64(8), overlay.IntervalBlocks)
	require.Equal(t, uint64(2), overlay.AckDeadlineBlocks)
	require.Equal(t, uint64(20), overlay.IdleBlocks)
	require.Equal(t, heightsync.DefaultSyncDeltaBlocks, overlay.DeltaBlocks)
	require.Equal(t, heightsync.DefaultMinRoundsPerBlock, overlay.MinRoundsPerBlock, "MinRoundsPerBlock is compiled-only")
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
