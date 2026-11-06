package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/types"
)

func TestFloorPoint_StoresAckTimeWithoutMap(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0xa1}
	const want int64 = 1_700_000_000_000
	d.raiseWithTime(100, hash, want)

	// Clear any header map so the floor time must come from the stamp alone.
	d.sm.SetHeaderTimes(headertime.New())

	p, known := d.sm.HeightSyncFloorPointAsOf(d.next())
	require.True(t, known)
	require.Equal(t, uint64(100), p.Height)
	require.Equal(t, hash, p.Hash)
	require.Equal(t, want, p.TimeMs)
}

func TestFloorPoint_SameTimeStableAcrossHosts(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0xa2}
	const want int64 = 5_000
	d.raiseWithTime(100, hash, want)

	// Second host confirms the same floor height/hash/time (no raise).
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 100, hash, types.SyncState_SYNCED, want)))

	p, known := d.sm.HeightSyncFloorPointAsOf(d.next())
	require.True(t, known)
	require.Equal(t, want, p.TimeMs)
}

func TestFloorPoint_DisagreeKeepsFirst(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0xa3}
	d.raiseWithTime(100, hash, 7_000)

	// Second host confirms the same floor with a disagreeing time; first wins.
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 100, hash, types.SyncState_SYNCED, 9_000)))

	p, known := d.sm.HeightSyncFloorPointAsOf(d.next())
	require.True(t, known)
	require.Equal(t, int64(7_000), p.TimeMs, "first wins; replica roots must not flip")
}

func TestFloorPoint_LegacyZeroUntilFilled(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0xa4}
	d.raiseWithTime(100, hash, 0) // legacy stamp with no carried time
	p, known := d.sm.HeightSyncFloorPointAsOf(d.next())
	require.True(t, known)
	require.Zero(t, p.TimeMs)

	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 100, hash, types.SyncState_SYNCED, 4_000)))

	p, known = d.sm.HeightSyncFloorPointAsOf(d.next())
	require.True(t, known)
	require.Equal(t, int64(4_000), p.TimeMs)
}

func TestFloorPoint_TwoReplicasAgreeOnFirstWin(t *testing.T) {
	hash := []byte{0xa5}
	build := func(t *testing.T) *floorDriver {
		d := newFloorDriver(t)
		d.raiseWithTime(100, hash, 7_000)
		d.turn++
		ref := d.next()
		require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
		slot := uint32(d.turn % uint64(len(d.hosts)))
		require.NoError(t, d.apply(divAckTxWithTime(t, d.hosts[slot], d.turn, ref, slot, 100, hash, types.SyncState_SYNCED, 9_000)))
		return d
	}
	a := build(t)
	b := build(t)
	pa, oka := a.sm.HeightSyncFloorPointAsOf(a.next())
	pb, okb := b.sm.HeightSyncFloorPointAsOf(b.next())
	require.True(t, oka)
	require.True(t, okb)
	require.Equal(t, pa.TimeMs, pb.TimeMs)
	require.Equal(t, int64(7_000), pa.TimeMs)
}
