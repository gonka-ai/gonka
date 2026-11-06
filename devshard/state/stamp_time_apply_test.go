package state

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/types"
)

func startTxWithTime(id, height uint64, hash []byte, timeMs int64) *types.DevshardTx {
	tx := divStartTx(id, height, hash)
	tx.GetStartInference().ObservedTimeMs = timeMs
	return tx
}

func (d *floorDriver) confirmWithTime(id, height uint64, hash []byte, timeMs int64) *types.DevshardTx {
	d.t.Helper()
	rec, ok := d.sm.GetInference(id)
	require.True(d.t, ok)
	slot := int(rec.ExecutorSlot)
	return txConfirm(&types.MsgConfirmStart{
		InferenceId: id,
		ExecutorSig: testutil.SignExecutorReceipt(d.t, d.hosts[slot], "escrow-1", id, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens,
			testutil.ReceiptStamp{Height: height, Hash: hash, TimeMs: timeMs}),
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		ObservedTimeMs:    timeMs,
	})
}

func TestApplyStart_CarriedTimeAutrustsEmptyMap(t *testing.T) {
	src := headertime.New()
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0xaa}
	d.raise(100, hash)

	const want int64 = 1_700_000_000_000
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, want)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, want, rec.StartedAtTimeMs)
	_, stored := src.StoredAt(100)
	require.False(t, stored, "map miss must autotrust without waiting")
}

func TestApplyConfirm_CarriedTimeAutrustsEmptyMap(t *testing.T) {
	src := headertime.New()
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0xbb}
	d.raise(100, hash)

	const want int64 = 1_800_000_000_000
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, want)))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, want+1)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, want, rec.StartedAtTimeMs)
	require.Equal(t, want+1, rec.ConfirmedAtTimeMs)
	_, stored := src.StoredAt(100)
	require.False(t, stored)
}

func TestApplyStamp_MapMissNoHeaderPending(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	hash := []byte{0xcc}
	d.raise(100, hash)
	id := d.next()
	err := d.apply(startTxWithTime(id, 100, hash, 9_000))
	require.NoError(t, err)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
}

func TestApplyStamp_MapHitDisagreeRejects(t *testing.T) {
	src := headertime.New()
	require.NoError(t, src.Seed(100, []byte{0xdd}, 5_000))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0xdd}
	d.raise(100, hash)

	id := d.next()
	err := d.apply(startTxWithTime(id, 100, hash, 9_000))
	require.ErrorIs(t, err, heightsync.ErrStampTimeMismatch)
	_, ok := d.sm.GetInference(id)
	require.False(t, ok, "reject must not leave the inference in state")
}

func TestApplyStamp_MapHitAgreeCopies(t *testing.T) {
	src := headertime.New()
	const want int64 = 5_000
	hash := []byte{0xee}
	require.NoError(t, src.Seed(100, hash, want))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	d.raise(100, hash)

	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, want)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, want, rec.StartedAtTimeMs)
}

func TestApplyStamp_LaterMapDisagreeWarns(t *testing.T) {
	src := headertime.New()
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0xff}
	d.raise(100, hash)

	const carried int64 = 7_000
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, carried)))

	var warns atomic.Int32
	d.sm.SetStampTimeMismatchHook(func(leg string, height uint64, _ []byte, stampMs, mapMs int64) {
		warns.Add(1)
		require.Equal(t, uint64(100), height)
		require.Equal(t, carried, stampMs)
		require.Equal(t, int64(9_000), mapMs)
		require.Contains(t, leg, "started_at")
	})

	require.NoError(t, src.Seed(100, hash, 9_000))
	require.Equal(t, int32(1), warns.Load(), "deferred map disagree must warn")

	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, carried, rec.StartedAtTimeMs, "state must not roll back")
	require.Equal(t, types.StatusPending, rec.Status)
}

func TestDeferredMapDisagree_WarnsFloorTime(t *testing.T) {
	src := headertime.New()
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0x10}
	const carried int64 = 4_000
	d.raiseWithTime(100, hash, carried)

	p, known := d.sm.HeightSyncFloorPointAsOf(d.sm.LatestNonce() + 1)
	require.True(t, known)
	require.Equal(t, carried, p.TimeMs)

	var warns atomic.Int32
	var sawFloor atomic.Bool
	d.sm.SetStampTimeMismatchHook(func(leg string, height uint64, gotHash []byte, stampMs, mapMs int64) {
		warns.Add(1)
		if leg == "floor" {
			sawFloor.Store(true)
			require.Equal(t, uint64(100), height)
			require.Equal(t, hash, gotHash)
			require.Equal(t, carried, stampMs)
			require.Equal(t, int64(9_000), mapMs)
		}
	})

	require.NoError(t, src.Seed(100, hash, 9_000))
	require.True(t, sawFloor.Load(), "deferred map fill must warn on floor TimeMs disagree")
	require.Positive(t, warns.Load())

	p2, known := d.sm.HeightSyncFloorPointAsOf(d.sm.LatestNonce() + 1)
	require.True(t, known)
	require.Equal(t, carried, p2.TimeMs, "floor TimeMs must not be rewritten")
}

func TestDeferredMapDisagree_WarnsConfirmHeightHashTime(t *testing.T) {
	src := headertime.New()
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	hash := []byte{0x11}
	d.raiseWithTime(100, hash, 1_000)

	const confirmMs int64 = 2_000
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, 1_000)))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, confirmMs)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, hash, rec.ConfirmedAtBlockHash)

	var sawConfirm atomic.Bool
	wantLeg := fmt.Sprintf("confirmed_at:%d", id)
	d.sm.SetStampTimeMismatchHook(func(leg string, height uint64, gotHash []byte, stampMs, mapMs int64) {
		if !strings.HasPrefix(leg, "confirmed_at:") {
			return
		}
		sawConfirm.Store(true)
		require.Equal(t, wantLeg, leg)
		require.Equal(t, uint64(100), height)
		require.Equal(t, hash, gotHash, "confirm deferred warn carries ConfirmedAtBlockHash")
		require.Equal(t, confirmMs, stampMs)
		require.Equal(t, int64(8_000), mapMs)
	})

	require.NoError(t, src.Seed(100, hash, 8_000))
	require.True(t, sawConfirm.Load(), "confirm carried time vs later map must warn")
}

func TestApplyHeartbeat_MapHitDisagreeRejects(t *testing.T) {
	src := headertime.New()
	hash := []byte{0x01}
	require.NoError(t, src.Seed(50, hash, 1_000))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)

	hb := divHeartbeatTx(50, hash)
	hb.GetHeartbeat().ObservedTimeMs = 2_000
	require.ErrorIs(t, d.apply(hb), heightsync.ErrStampTimeMismatch)
}

func TestApplyTimeout_StillIgnoresMap(t *testing.T) {
	// Votes-only timeout apply: map empty / missing rows must not block.
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	hash := []byte{0x02, 0x00}
	d.raise(100, hash)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, 1_000)))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, 1_000)))

	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

// storedHash differs from the executor stamp so the same-hash map check
// leaves the carried time alone. The newest-block check is what rejects it.
func executorClockDriver(t *testing.T, stamp []byte, mapMs int64) *floorDriver {
	t.Helper()
	src := headertime.New()
	require.NoError(t, src.Seed(100, []byte{0x11}, mapMs))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	d.raiseWithTime(100, stamp, mapMs)
	return d
}

func TestApplyConfirm_TimeBeforeNewestStoredBlockRejects(t *testing.T) {
	stamp := []byte{0xaa}
	d := executorClockDriver(t, stamp, 50_000)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, stamp, 50_000)))
	logs := captureStateLogs(t)
	err := d.apply(d.confirmWithTime(id, 100, stamp, 40_000))
	require.ErrorIs(t, err, types.ErrStampTimeBeforeChain)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	require.Contains(t, logs.String(), "executor stamp time is before the newest stored block")
	require.Contains(t, logs.String(), `"stage":"apply"`)
}

func TestComposeConfirm_TimeBeforeNewestStoredBlockDrops(t *testing.T) {
	stamp := []byte{0xab}
	d := executorClockDriver(t, stamp, 50_000)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, stamp, 50_000)))
	logs := captureStateLogs(t)
	_, applied, err := d.sm.ApplyLocalBestEffort(d.next(), []*types.DevshardTx{
		d.confirmWithTime(id, 100, stamp, 40_000),
	})
	require.NoError(t, err)
	require.Empty(t, applied)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusPending, rec.Status)
	require.Contains(t, logs.String(), "executor stamp time is before the newest stored block")
	require.Contains(t, logs.String(), `"stage":"compose"`)
}

func TestApplyFinish_TimeBeforeNewestStoredBlockRejects(t *testing.T) {
	stamp := []byte{0xac}
	d := executorClockDriver(t, stamp, 50_000)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, stamp, 50_000)))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, stamp, 50_000)))
	logs := captureStateLogs(t)
	err := d.apply(finishWithTime(t, d, id, 100, stamp, 40_000))
	require.ErrorIs(t, err, types.ErrStampTimeBeforeChain)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Contains(t, logs.String(), `"leg":"finish"`)
}

func TestApplyConfirm_TimeBeforeFloorLogsStrongDeferred(t *testing.T) {
	src := headertime.New()
	require.NoError(t, src.Seed(50, []byte{0x01}, 1_000))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	stamp := []byte{0xad}
	d.raiseWithTime(100, stamp, 50_000)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, stamp, 50_000)))
	logs := captureStateLogs(t)
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, stamp, 40_000)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Equal(t, int64(40_000), rec.ConfirmedAtTimeMs)
	require.Contains(t, logs.String(), "executor stamp time is before the floor; strong check deferred")
	require.NotContains(t, logs.String(), "newest stored block")
}

func TestApplyConfirm_FloorCarryBelowNewerMapAllows(t *testing.T) {
	src := headertime.New()
	stamp := []byte{0xae}
	require.NoError(t, src.Seed(100, stamp, 50_000))
	require.NoError(t, src.Seed(102, []byte{0x02}, 52_000))
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(src)
	d.raiseWithTime(100, stamp, 50_000)
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, stamp, 50_000)))
	logs := captureStateLogs(t)
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, stamp, 50_000)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, int64(50_000), rec.ConfirmedAtTimeMs)
	require.NotContains(t, logs.String(), "newest stored block")
	require.NotContains(t, logs.String(), "strong check deferred")
}

func TestCheckEvidence_TimeBeforeNewestStoredBlockRejectsAndLogs(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	src := headertime.New()
	require.NoError(t, src.Seed(42, []byte("block-42"), 42*testutil.HeightClockBlockMs))
	sm.SetHeaderTimes(src)
	rec := evidenceRecord(types.StatusPending)
	const carried int64 = 1_000
	tx := evidenceConfirmFor(t, hosts[1], "escrow-1", 1, rec, testutil.ReceiptStamp{
		Height: 42, Hash: []byte("block-42"), TimeMs: carried,
	})
	tx.GetConfirmStart().ObservedTimeMs = carried
	logs := captureStateLogs(t)
	err := sm.CheckEvidence(rec, tx)
	require.ErrorIs(t, err, types.ErrStampTimeBeforeChain)
	require.Contains(t, logs.String(), "executor stamp time is before the newest stored block")
	require.Contains(t, logs.String(), `"stage":"challenge"`)
	require.Contains(t, logs.String(), `"leg":"confirm"`)
}

func TestCheckEvidence_TimeBeforeFloorLogsStrongDeferred(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	rec := evidenceRecord(types.StatusPending)
	const carried int64 = 1_000
	tx := evidenceConfirmFor(t, hosts[1], "escrow-1", 1, rec, testutil.ReceiptStamp{
		Height: 42, Hash: []byte("block-42"), TimeMs: carried,
	})
	tx.GetConfirmStart().ObservedTimeMs = carried
	logs := captureStateLogs(t)
	require.NoError(t, sm.CheckEvidence(rec, tx))
	require.Contains(t, logs.String(), "executor stamp time is before the floor; strong check deferred")
	require.Contains(t, logs.String(), `"stage":"challenge"`)
}

func TestCheckEvidence_FinishTimeBeforeNewestStoredBlockRejectsAndLogs(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	src := headertime.New()
	require.NoError(t, src.Seed(42, []byte("other"), 42*testutil.HeightClockBlockMs))
	sm.SetHeaderTimes(src)
	msg := evidenceFinishMsg("escrow-1", 1)
	msg.ObservedHeight = 42
	msg.ObservedBlockHash = []byte("block-42")
	msg.ObservedTimeMs = 1_000
	logs := captureStateLogs(t)
	err := sm.CheckEvidence(evidenceRecord(types.StatusStarted), signedEvidenceFinish(t, hosts[1], msg))
	require.ErrorIs(t, err, types.ErrStampTimeBeforeChain)
	require.Contains(t, logs.String(), `"stage":"challenge"`)
	require.Contains(t, logs.String(), `"leg":"finish"`)
}

func finishWithTime(t *testing.T, d *floorDriver, id, height uint64, hash []byte, timeMs int64) *types.DevshardTx {
	t.Helper()
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	msg := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: rec.ExecutorSlot,
		EscrowId: "escrow-1", ObservedHeight: height, ObservedBlockHash: hash, ObservedTimeMs: timeMs,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, d.hosts[rec.ExecutorSlot], msg)
	return txFinish(msg)
}
