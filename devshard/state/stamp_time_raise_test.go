package state

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/logging"
	"devshard/signing"
	"devshard/types"
)

const (
	raiseTestFloor   = uint64(100)
	raiseTestFar     = uint64(999_990)
	raiseTestFarther = uint64(1_000_000)
	raiseTestT0      = int64(1_700_000_000_000)
)

func tenYearsMs() int64 { return (10 * 365 * 24 * time.Hour).Milliseconds() }

type captureWarn struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureWarn) Info(string, ...any)  {}
func (c *captureWarn) Error(string, ...any) {}
func (c *captureWarn) Debug(string, ...any) {}
func (c *captureWarn) Warn(msg string, _ ...any) {
	c.mu.Lock()
	c.lines = append(c.lines, msg)
	c.mu.Unlock()
}

func (c *captureWarn) count(msg string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, line := range c.lines {
		if line == msg {
			n++
		}
	}
	return n
}

func installWarnCapture(t *testing.T) *captureWarn {
	t.Helper()
	c := &captureWarn{}
	logging.SetLogger(c)
	t.Cleanup(func() { logging.SetLogger(logging.NewSlogAdapter()) })
	return c
}

func floorTimeAt(t *testing.T, sm *StateMachine, height uint64) int64 {
	t.Helper()
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	p, ok := sm.heightSyncFloor.PointAtHeight(height)
	require.True(t, ok, "floor entry at %d", height)
	return p.TimeMs
}

func settleAtDrain(t *testing.T, sm *StateMachine, id uint64) (due bool, rec *types.InferenceRecord) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	rec = sm.state.Inferences[id]
	require.NotNil(t, rec)
	var err error
	due, err = sm.liveTimeoutDueAtDrainLocked(rec, sm.state.LatestNonce+1)
	require.NoError(t, err)
	sm.settleLiveRecordLocked(rec, due)
	return due, rec
}

func v7Driver(t *testing.T) *floorDriver {
	t.Helper()
	return newVersionedFloorDriver(t, types.StartRequiresFloorProtocolVersion, nil, 0)
}

func (d *floorDriver) applyStore(txs ...*types.DevshardTx) {
	d.t.Helper()
	diff := testutil.SignDiff(d.t, d.user, "escrow-1", d.next(), txs)
	root, err := d.sm.ApplyDiff(diff)
	require.NoError(d.t, err)
	require.NoError(d.t, d.sm.inferenceStore.AppendDiff("escrow-1", types.DiffRecord{Diff: diff, StateHash: root}))
}

func (d *floorDriver) raiseStored(height uint64, hash []byte, timeMs int64) {
	d.t.Helper()
	d.turn++
	ref := d.next()
	d.applyStore(divHeartbeatTx(0, nil))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	d.applyStore(divAckTxWithTime(d.t, d.hosts[slot], d.turn, ref, slot, height, hash, types.SyncState_SYNCED, timeMs))
}

func finishAt(t *testing.T, host *signing.Secp256k1Signer, id uint64, slot uint32, height uint64, hash []byte, timeMs int64) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:       id,
		ResponseHash:      testutil.TestResponseHash,
		ServedHash:        testutil.TestServedHash,
		InputTokens:       80,
		OutputTokens:      40,
		ExecutorSlot:      slot,
		EscrowId:          "escrow-1",
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		ObservedTimeMs:    timeMs,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, host, msg)
	return txFinish(msg)
}

func TestFloorTimeRaiseKept(t *testing.T) {
	hash := []byte{0xaa}
	far := raiseTestT0 + tenYearsMs()

	t.Run("first timed entry", func(t *testing.T) {
		logs := installWarnCapture(t)
		d := v7Driver(t)
		d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
		require.Equal(t, raiseTestT0, floorTimeAt(t, d.sm, raiseTestFloor))
		require.Zero(t, logs.count(stampTimeRaiseDeferredMsg))
	})

	t.Run("within 45s", func(t *testing.T) {
		logs := installWarnCapture(t)
		d := v7Driver(t)
		d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
		next := raiseTestT0 + (40 * time.Second).Milliseconds()
		d.raiseWithTime(raiseTestFloor+1, []byte{0xab}, next)
		require.Equal(t, next, floorTimeAt(t, d.sm, raiseTestFloor+1))
		require.Zero(t, logs.count(stampTimeRaiseDeferredMsg))
	})

	t.Run("ten years", func(t *testing.T) {
		logs := installWarnCapture(t)
		d := v7Driver(t)
		d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
		logs.lines = nil
		d.raiseWithTime(raiseTestFar, []byte{0xee}, far)
		require.Equal(t, far, floorTimeAt(t, d.sm, raiseTestFar))
		require.Equal(t, 1, logs.count(stampTimeRaiseDeferredMsg))
	})

	t.Run("zero fill", func(t *testing.T) {
		logs := installWarnCapture(t)
		d := newVersionedFloorDriver(t, "v6", nil, 0)
		d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
		filled := []byte{0x11}
		d.raiseWithTime(raiseTestFloor+10, filled, 0)
		require.Zero(t, floorTimeAt(t, d.sm, raiseTestFloor+10))
		logs.lines = nil
		d.raiseWithTime(raiseTestFloor+10, filled, far)
		require.Equal(t, far, floorTimeAt(t, d.sm, raiseTestFloor+10))
		require.Equal(t, 1, logs.count(stampTimeRaiseDeferredMsg))
	})

	t.Run("snapshot and journal keep the carried time", func(t *testing.T) {
		d := v7Driver(t)
		d.raiseStored(raiseTestFloor, hash, raiseTestT0)
		d.raiseStored(raiseTestFar, []byte{0xee}, far)
		require.Equal(t, far, floorTimeAt(t, d.sm, raiseTestFar))

		snap, err := heightsync.FloorIndexFromProto(heightsync.FloorConfig{}, d.sm.ExportHeightSyncFloor())
		require.NoError(t, err)
		fresh := newSMWithKeys(t, d.user, d.hosts, types.StartRequiresFloorProtocolVersion, nil, 0)
		require.NoError(t, fresh.RestoreStateWithFloor(d.sm.ExportState(), snap))
		require.Equal(t, far, floorTimeAt(t, fresh, raiseTestFar))

		folded, err := NewStateMachine("escrow-1", d.sm.state.Config, d.sm.state.Group, 1_000_000, d.user.Address(),
			signing.NewSecp256k1Verifier(), d.sm.inferenceStore,
			WithStateRootAndProtocolVersion(types.StartRequiresFloorProtocolVersion))
		require.NoError(t, err)
		require.NoError(t, folded.RestoreStateWithFloor(d.sm.ExportState(), nil))
		require.Equal(t, far, floorTimeAt(t, folded, raiseTestFar))
	})
}

func TestTwoStampApplyStillSettlesFinished(t *testing.T) {
	logs := installWarnCapture(t)
	d := v7Driver(t)
	hash := []byte{0xaa}
	d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
	var ids []uint64
	for range 5 {
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, raiseTestFloor, hash, raiseTestT0)))
		ids = append(ids, id)
	}
	far1 := raiseTestT0 + tenYearsMs()
	far2 := far1 + time.Minute.Milliseconds()
	logs.lines = nil
	d.raiseWithTime(raiseTestFar, []byte{0xee}, far1)
	d.raiseWithTime(raiseTestFarther, []byte{0xef}, far2)
	require.Equal(t, far1, floorTimeAt(t, d.sm, raiseTestFar))
	require.Equal(t, far2, floorTimeAt(t, d.sm, raiseTestFarther))
	require.Equal(t, 2, logs.count(stampTimeRaiseDeferredMsg))

	for _, id := range ids {
		due, rec := settleAtDrain(t, d.sm, id)
		require.True(t, due)
		require.Equal(t, types.StatusFinished, rec.Status)
		require.Equal(t, rec.ReservedCost, rec.ActualCost)
		require.NotZero(t, rec.ReservedCost)
	}
}

func TestTwoFinishApplyStillSettlesFinished(t *testing.T) {
	logs := installWarnCapture(t)
	d := v7Driver(t)
	hash := []byte{0xaa}
	d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
	var ids []uint64
	for range 5 {
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, raiseTestFloor, hash, raiseTestT0)))
		ids = append(ids, id)
	}
	for _, id := range ids[:2] {
		require.NoError(t, d.apply(d.confirmWithTime(id, raiseTestFloor, hash, raiseTestT0)))
	}
	far1 := raiseTestT0 + tenYearsMs()
	far2 := far1 + time.Minute.Milliseconds()
	logs.lines = nil
	for i, height := range []uint64{raiseTestFar, raiseTestFarther} {
		id := ids[i]
		rec, ok := d.sm.GetInference(id)
		require.True(t, ok)
		timeMs := far1
		stamp := []byte{0xee}
		if i == 1 {
			timeMs = far2
			stamp = []byte{0xef}
		}
		require.NoError(t, d.apply(finishAt(t, d.hosts[rec.ExecutorSlot], id, rec.ExecutorSlot, height, stamp, timeMs)))
	}
	require.Equal(t, far1, floorTimeAt(t, d.sm, raiseTestFar))
	require.Equal(t, far2, floorTimeAt(t, d.sm, raiseTestFarther))
	require.Equal(t, 2, logs.count(stampTimeRaiseDeferredMsg))

	for _, id := range ids[2:] {
		due, rec := settleAtDrain(t, d.sm, id)
		require.True(t, due)
		require.Equal(t, types.StatusFinished, rec.Status)
		require.Equal(t, rec.ReservedCost, rec.ActualCost)
	}
}

func TestComposeDropsStampAheadOfWallClock(t *testing.T) {
	wall := time.UnixMilli(raiseTestT0)
	d := v7Driver(t)
	d.sm.stampWallClock = func() time.Time { return wall }
	hash := []byte{0xaa}
	d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))

	ahead := func(delta time.Duration, n byte) *types.DevshardTx {
		return divAckTxWithTime(t, d.hosts[0], d.turn, ref, 0, raiseTestFloor+uint64(n), []byte{n}, types.SyncState_SYNCED, wall.Add(delta).UnixMilli())
	}
	kept := ahead(29*time.Second, 2)
	_, applied, err := d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{
		ahead(31*time.Second, 1),
		kept,
	})
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Equal(t, kept.GetHeightAck().GetObservedTimeMs(), applied[0].GetHeightAck().GetObservedTimeMs())

	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, raiseTestFloor+2, []byte{2}, kept.GetHeightAck().GetObservedTimeMs())))
	require.NoError(t, d.apply(d.confirmWithTime(id, raiseTestFloor+2, []byte{2}, kept.GetHeightAck().GetObservedTimeMs())))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	futureFinish := finishAt(t, d.hosts[rec.ExecutorSlot], id, rec.ExecutorSlot, raiseTestFloor+3, []byte{3}, wall.Add(31*time.Second).UnixMilli())
	_, applied, err = d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{futureFinish})
	require.NoError(t, err)
	require.Empty(t, applied)
	rec, ok = d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status, "a dropped finish leaves the record unchanged")
}

func TestComposeKeepsStampInsideWallSkew(t *testing.T) {
	hash := []byte{0xaa}
	d := v7Driver(t)
	d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)

	t.Run("40s ahead of the floor", func(t *testing.T) {
		logs := installWarnCapture(t)
		stamp := raiseTestT0 + (40 * time.Second).Milliseconds()
		d.sm.stampWallClock = func() time.Time { return time.UnixMilli(stamp - (10 * time.Second).Milliseconds()) }
		d.turn++
		ref := d.next()
		require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
		ack := divAckTxWithTime(t, d.hosts[0], d.turn, ref, 0, raiseTestFloor+1, []byte{0x28}, types.SyncState_SYNCED, stamp)
		_, applied, err := d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{ack})
		require.NoError(t, err)
		require.Len(t, applied, 1)
		require.Equal(t, stamp, floorTimeAt(t, d.sm, raiseTestFloor+1))
		require.Zero(t, logs.count(stampTimeRaiseDeferredMsg))
	})

	t.Run("10min ahead of the floor", func(t *testing.T) {
		logs := installWarnCapture(t)
		stamp := raiseTestT0 + (10 * time.Minute).Milliseconds()
		d.sm.stampWallClock = func() time.Time { return time.UnixMilli(stamp - (10 * time.Second).Milliseconds()) }
		d.turn++
		ref := d.next()
		require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
		ack := divAckTxWithTime(t, d.hosts[0], d.turn, ref, 0, raiseTestFloor+2, []byte{0x0a}, types.SyncState_SYNCED, stamp)
		_, applied, err := d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{ack})
		require.NoError(t, err)
		require.Len(t, applied, 1)
		require.Equal(t, stamp, floorTimeAt(t, d.sm, raiseTestFloor+2))
		require.Equal(t, 1, logs.count(stampTimeRaiseDeferredMsg))
	})
}

func TestComposeDropsReviewerStampsAndDrainRefunds(t *testing.T) {
	wallMs := raiseTestT0 + (4 * time.Second).Milliseconds()
	d := v7Driver(t)
	d.sm.stampWallClock = func() time.Time { return time.UnixMilli(wallMs) }
	low, high := []byte{0xaa}, []byte{0xab}
	d.raiseWithTime(raiseTestFloor, low, raiseTestT0)
	d.raiseWithTime(raiseTestFloor+4, high, wallMs)
	var ids []uint64
	for range 5 {
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, raiseTestFloor+4, high, wallMs)))
		ids = append(ids, id)
	}
	far := wallMs + tenYearsMs()
	d.turn++
	ref := d.next()
	require.NoError(t, d.apply(divHeartbeatTx(0, nil)))
	acks := []*types.DevshardTx{
		divAckTxWithTime(t, d.hosts[0], d.turn, ref, 0, raiseTestFar, []byte{0xee}, types.SyncState_SYNCED, far),
		divAckTxWithTime(t, d.hosts[1], d.turn, ref, 1, raiseTestFarther, []byte{0xef}, types.SyncState_SYNCED, far+time.Minute.Milliseconds()),
	}
	_, applied, err := d.sm.ApplyLocalBestEffort(d.next(), acks)
	require.NoError(t, err)
	for _, tx := range applied {
		require.Nil(t, tx.GetHeightAck())
	}
	require.Equal(t, wallMs, floorTimeAt(t, d.sm, raiseTestFloor+4))

	for _, id := range ids[:2] {
		require.NoError(t, d.apply(d.confirmWithTime(id, raiseTestFloor+4, high, wallMs)))
		rec, ok := d.sm.GetInference(id)
		require.True(t, ok)
		fin := finishAt(t, d.hosts[rec.ExecutorSlot], id, rec.ExecutorSlot, raiseTestFar, []byte{0xee}, far)
		_, applied, err = d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{fin})
		require.NoError(t, err)
		require.Empty(t, applied)
	}
	for _, id := range ids {
		due, rec := settleAtDrain(t, d.sm, id)
		require.False(t, due)
		require.Equal(t, types.StatusTimedOut, rec.Status)
		require.Zero(t, rec.ActualCost)
	}
}

func TestApplyFutureStampWithoutWallClockKeepsTime(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	version := types.StartRequiresFloorProtocolVersion
	clocked := &floorDriver{t: t, sm: newSMWithKeys(t, user, hosts, version, nil, 0), user: user, hosts: hosts}
	plain := &floorDriver{t: t, sm: newSMWithKeys(t, user, hosts, version, nil, 0), user: user, hosts: hosts}
	clocked.sm.stampWallClock = func() time.Time { return time.UnixMilli(raiseTestT0) }
	hash := []byte{0xaa}
	clocked.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
	plain.raiseWithTime(raiseTestFloor, hash, raiseTestT0)

	clocked.turn++
	plain.turn = clocked.turn
	ref := clocked.next()
	require.Equal(t, ref, plain.next())
	require.NoError(t, clocked.apply(divHeartbeatTx(0, nil)))
	require.NoError(t, plain.apply(divHeartbeatTx(0, nil)))
	ack := divAckTxWithTime(t, hosts[0], clocked.turn, ref, 0, raiseTestFar, []byte{0xee}, types.SyncState_SYNCED, raiseTestT0+tenYearsMs())
	_, applied, err := clocked.sm.ApplyLocalBestEffort(clocked.next(), []*types.DevshardTx{ack})
	require.NoError(t, err)
	require.Empty(t, applied)
	require.NoError(t, plain.apply(ack))
	require.Equal(t, raiseTestT0+tenYearsMs(), floorTimeAt(t, plain.sm, raiseTestFar))
}

func TestCheckEvidenceFutureStamp(t *testing.T) {
	d := v7Driver(t)
	hash := []byte{0xaa}
	d.raiseWithTime(raiseTestFloor, hash, raiseTestT0)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, raiseTestFloor, hash, raiseTestT0)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)

	wall := time.UnixMilli(raiseTestT0)
	d.sm.stampWallClock = func() time.Time { return wall }
	future := d.confirmWithTime(id, raiseTestFloor+1, []byte{0xee}, wall.Add(31*time.Second).UnixMilli())
	require.ErrorIs(t, d.sm.CheckEvidence(&rec, future), types.ErrStampTimeInFuture)

	logs := installWarnCapture(t)
	stamp := raiseTestT0 + (10 * time.Minute).Milliseconds()
	d.sm.stampWallClock = func() time.Time { return time.UnixMilli(stamp - (10 * time.Second).Milliseconds()) }
	nearWall := d.confirmWithTime(id, raiseTestFloor+1, []byte{0xee}, stamp)
	require.NoError(t, d.sm.CheckEvidence(&rec, nearWall))
	require.Equal(t, 1, logs.count(stampTimeRaiseDeferredMsg))

	finishRec := rec
	finishRec.Status = types.StatusStarted
	finishRec.ConfirmedAtHeight = raiseTestFloor
	finishRec.ConfirmedAtBlockHash = hash
	futureFinish := finishAt(t, d.hosts[rec.ExecutorSlot], id, rec.ExecutorSlot, raiseTestFloor+1, []byte{0xef}, wall.Add(31*time.Second).UnixMilli())
	d.sm.stampWallClock = func() time.Time { return wall }
	require.ErrorIs(t, d.sm.CheckEvidence(&finishRec, futureFinish), types.ErrStampTimeInFuture)
}
