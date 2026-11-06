package state

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/types"
)

// mapPair is two machines that share hosts/user so the same signed diffs apply
// on both. full has a header map; empty does not. Issue 12 acceptance cases
// require equal roots/balance/stats when only the map differs.
type mapPair struct {
	t     *testing.T
	full  *floorDriver
	empty *floorDriver
	src   *headerTimes
	turn  uint64
}

func newMapPair(t *testing.T) *mapPair {
	t.Helper()
	full, empty := pairedFloorDrivers(t)
	src := newHeaderTimes()
	full.sm.SetHeaderTimes(src)
	empty.sm.SetHeaderTimes(headertime.New())
	return &mapPair{t: t, full: full, empty: empty, src: src}
}

func (p *mapPair) applyBoth(txs ...*types.DevshardTx) []byte {
	p.t.Helper()
	require.Equal(p.t, p.full.sm.LatestNonce(), p.empty.sm.LatestNonce())
	nonce := p.full.sm.LatestNonce() + 1
	diff := testutil.SignDiff(p.t, p.full.user, "escrow-1", nonce, txs)
	rootFull, err := p.full.sm.ApplyDiff(diff)
	require.NoError(p.t, err)
	rootEmpty, err := p.empty.sm.ApplyDiff(diff)
	require.NoError(p.t, err)
	require.Equal(p.t, rootFull, rootEmpty, "full vs empty map: equal StateHash at nonce %d", nonce)
	return rootFull
}

func (p *mapPair) raiseBoth(height uint64, hash []byte, timeMs int64) {
	p.t.Helper()
	p.turn++
	ref := p.full.sm.LatestNonce() + 1
	p.applyBoth(divHeartbeatTx(0, nil))
	slot := uint32(p.turn % uint64(len(p.full.hosts)))
	p.applyBoth(divAckTxWithTime(p.t, p.full.hosts[slot], p.turn, ref, slot, height, hash, types.SyncState_SYNCED, timeMs))
	if timeMs > 0 {
		p.src.set(height, time.Duration(timeMs)*time.Millisecond)
	}
	for _, d := range []*floorDriver{p.full, p.empty} {
		h, got, known := d.sm.HeightSyncFloorAsOf(d.sm.LatestNonce() + 1)
		require.True(p.t, known)
		require.Equal(p.t, height, h)
		require.Equal(p.t, hash, got)
	}
}

func (p *mapPair) assertEqualRoots() {
	p.t.Helper()
	rootFull, err := p.full.sm.ComputeStateRoot()
	require.NoError(p.t, err)
	rootEmpty, err := p.empty.sm.ComputeStateRoot()
	require.NoError(p.t, err)
	require.Equal(p.t, rootFull, rootEmpty)
}

func (p *mapPair) assertEqualBalanceAndHostStats() {
	p.t.Helper()
	require.Equal(p.t, p.full.sm.Balance(), p.empty.sm.Balance())
	fullState := p.full.sm.ExportState()
	emptyState := p.empty.sm.ExportState()
	require.Equal(p.t, len(fullState.HostStats), len(emptyState.HostStats))
	for slot, hs := range fullState.HostStats {
		other, ok := emptyState.HostStats[slot]
		require.True(p.t, ok, "slot %d", slot)
		require.Equal(p.t, hs, other, "slot %d host stats", slot)
	}
}

// Issue 12.1: seal-interval nonce over Finished past grace — full vs empty map.
func TestIssue12_SealNonce_FullVsEmptyMapEqualRoots(t *testing.T) {
	p := newMapPair(t)
	p.full.sm.mu.Lock()
	p.full.sm.state.Config.AutoSealEveryNNonces = 1
	p.full.sm.state.Config.InferenceSealGraceNonces = 0
	p.full.sm.state.Config.InferenceSealGraceSeconds = 1
	p.full.sm.state.Config.ExecutionTimeout = 0
	p.full.sm.mu.Unlock()
	p.empty.sm.mu.Lock()
	p.empty.sm.state.Config.AutoSealEveryNNonces = 1
	p.empty.sm.state.Config.InferenceSealGraceNonces = 0
	p.empty.sm.state.Config.InferenceSealGraceSeconds = 1
	p.empty.sm.state.Config.ExecutionTimeout = 0
	p.empty.sm.mu.Unlock()

	hash := []byte{0x12, 0x01}
	confirmAt := time.Hour
	p.raiseBoth(1000, hash, confirmAt.Milliseconds())
	id := p.full.sm.LatestNonce() + 1
	p.applyBoth(startTxWithTime(id, 1000, hash, confirmAt.Milliseconds()))
	p.applyBoth(p.full.confirmWithTime(id, 1000, hash, confirmAt.Milliseconds()))

	finish := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: uint32(id % uint64(len(p.full.hosts))), EscrowId: "escrow-1",
		ObservedHeight: 1000, ObservedBlockHash: hash, ObservedTimeMs: confirmAt.Milliseconds(),
	}
	finish.ProposerSig = testutil.SignProposerTx(t, p.full.hosts[finish.ExecutorSlot], finish)
	p.applyBoth(txFinish(finish))

	grace := int64(p.full.sm.Config().InferenceSealGraceSeconds)
	exec := p.full.sm.Config().ExecutionTimeout
	required := time.Duration(FinishedClockRequiredSeconds(grace, exec)) * time.Second
	p.raiseBoth(2000, []byte{0x12, 0x02}, (confirmAt + required).Milliseconds())
	p.raiseBoth(2000+heightsync.SealDrainFloorLagBlocks, []byte{0x12, 0x2a}, (confirmAt + required).Milliseconds())

	for {
		_, live := p.full.sm.GetInference(id)
		if !live {
			break
		}
		p.applyBoth()
		require.Less(t, p.full.sm.LatestNonce(), id+100)
	}
	require.Contains(t, p.full.sm.ExportSealedNonces(), id)
	require.Contains(t, p.empty.sm.ExportSealedNonces(), id)
	p.assertEqualRoots()
}

// Issue 12.2: settlement drain of Started past ExecutionTimeout — equal balance,
// host stats, and roots with full vs empty map.
func TestIssue12_DrainStartedPastExec_FullVsEmptyMapAgree(t *testing.T) {
	p := newMapPair(t)
	hash := []byte{0x12, 0x03}
	anchor := time.Hour
	exec := time.Duration(p.full.sm.Config().ExecutionTimeout) * time.Second

	p.raiseBoth(100, hash, anchor.Milliseconds())
	id := p.full.sm.LatestNonce() + 1
	p.applyBoth(startTxWithTime(id, 100, hash, anchor.Milliseconds()))
	p.applyBoth(p.full.confirmWithTime(id, 100, hash, anchor.Milliseconds()))

	dueMs := (anchor + exec).Milliseconds()
	p.raiseBoth(200, []byte{0x12, 0x04}, dueMs)

	p.applyBoth(txFinalize())
	for p.full.sm.Phase() != types.PhaseSettlement {
		p.applyBoth()
	}
	require.Equal(t, types.PhaseSettlement, p.empty.sm.Phase())
	_, liveFull := p.full.sm.GetInference(id)
	_, liveEmpty := p.empty.sm.GetInference(id)
	require.False(t, liveFull)
	require.False(t, liveEmpty)

	p.assertEqualBalanceAndHostStats()
	p.assertEqualRoots()
}

// Issue 12.3: timeout diff with valid votes — both maps accept.
func TestIssue12_TimeoutDiff_FullVsEmptyMapBothAccept(t *testing.T) {
	p := newMapPair(t)
	hash := []byte{0x12, 0x05}
	anchor := time.Hour
	refusal := time.Duration(p.full.sm.Config().RefusalTimeout) * time.Second

	p.raiseBoth(100, hash, anchor.Milliseconds())
	id := p.full.sm.LatestNonce() + 1
	p.applyBoth(startTxWithTime(id, 100, hash, anchor.Milliseconds()))
	p.raiseBoth(200, []byte{0x12, 0x06}, (anchor + refusal).Milliseconds())

	timeoutTx := p.full.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	p.applyBoth(timeoutTx)

	recFull, ok := p.full.sm.GetInference(id)
	require.True(t, ok)
	recEmpty, ok := p.empty.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, recFull.Status)
	require.Equal(t, types.StatusTimedOut, recEmpty.Status)
	p.assertEqualRoots()
}

// Issue 12.4: journal produced with a full map replays onto an empty map with
// matching StateHash at every nonce, including seal and drain nonces.
func TestIssue12_RestartEmptyMap_ReplayMatchesPersistedHashes(t *testing.T) {
	live, replay := pairedFloorDrivers(t)
	src := newHeaderTimes()
	live.sm.SetHeaderTimes(src)
	replay.sm.SetHeaderTimes(headertime.New())

	live.sm.mu.Lock()
	live.sm.state.Config.AutoSealEveryNNonces = 1
	live.sm.state.Config.InferenceSealGraceNonces = 0
	live.sm.state.Config.InferenceSealGraceSeconds = 1
	live.sm.state.Config.ExecutionTimeout = 0
	live.sm.mu.Unlock()
	replay.sm.mu.Lock()
	replay.sm.state.Config.AutoSealEveryNNonces = 1
	replay.sm.state.Config.InferenceSealGraceNonces = 0
	replay.sm.state.Config.InferenceSealGraceSeconds = 1
	replay.sm.state.Config.ExecutionTimeout = 0
	replay.sm.mu.Unlock()

	var pages []types.DiffRecord
	record := func(txs ...*types.DevshardTx) {
		t.Helper()
		nonce := live.sm.LatestNonce() + 1
		diff := testutil.SignDiff(t, live.user, "escrow-1", nonce, txs)
		root, err := live.sm.ApplyDiff(diff)
		require.NoError(t, err)
		pages = append(pages, types.DiffRecord{Diff: diff, StateHash: append([]byte(nil), root...)})
	}
	raise := func(height uint64, hash []byte, timeMs int64) {
		t.Helper()
		live.turn++
		ref := live.sm.LatestNonce() + 1
		record(divHeartbeatTx(0, nil))
		slot := uint32(live.turn % uint64(len(live.hosts)))
		record(divAckTxWithTime(t, live.hosts[slot], live.turn, ref, slot, height, hash, types.SyncState_SYNCED, timeMs))
		if timeMs > 0 {
			src.set(height, time.Duration(timeMs)*time.Millisecond)
		}
	}

	hash := []byte{0x12, 0x07}
	confirmAt := time.Hour
	raise(1000, hash, confirmAt.Milliseconds())
	id := live.sm.LatestNonce() + 1
	record(startTxWithTime(id, 1000, hash, confirmAt.Milliseconds()))
	record(live.confirmWithTime(id, 1000, hash, confirmAt.Milliseconds()))
	finish := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: 10, OutputTokens: 20, ExecutorSlot: uint32(id % uint64(len(live.hosts))), EscrowId: "escrow-1",
		ObservedHeight: 1000, ObservedBlockHash: hash, ObservedTimeMs: confirmAt.Milliseconds(),
	}
	finish.ProposerSig = testutil.SignProposerTx(t, live.hosts[finish.ExecutorSlot], finish)
	record(txFinish(finish))

	grace := int64(live.sm.Config().InferenceSealGraceSeconds)
	required := time.Duration(FinishedClockRequiredSeconds(grace, live.sm.Config().ExecutionTimeout)) * time.Second
	raise(2000, []byte{0x12, 0x08}, (confirmAt + required).Milliseconds())
	raise(2000+heightsync.SealDrainFloorLagBlocks, []byte{0x12, 0x09}, (confirmAt + required).Milliseconds())
	for {
		_, liveRec := live.sm.GetInference(id)
		if !liveRec {
			break
		}
		record()
		require.Less(t, live.sm.LatestNonce(), id+100)
	}
	require.Contains(t, live.sm.ExportSealedNonces(), id)

	// Drain path: second inference Started past refusal, then finalize.
	anchor := 2 * time.Hour
	raise(3000, []byte{0x12, 0x09}, anchor.Milliseconds())
	id2 := live.sm.LatestNonce() + 1
	record(startTxWithTime(id2, 3000, []byte{0x12, 0x09}, anchor.Milliseconds()))
	refusal := time.Duration(live.sm.Config().RefusalTimeout) * time.Second
	raise(3100, []byte{0x12, 0x0a}, (anchor + refusal).Milliseconds())
	record(txFinalize())
	for live.sm.Phase() != types.PhaseSettlement {
		record()
	}
	require.Equal(t, types.PhaseSettlement, live.sm.Phase())

	for i, page := range pages {
		got, err := replay.sm.ApplyLocalPersistedRecord(page)
		require.NoError(t, err, "replay nonce %d (page %d)", page.Nonce, i)
		require.True(t, bytes.Equal(page.StateHash, got),
			"empty-map replay StateHash mismatch at nonce %d", page.Nonce)
	}
	require.Equal(t, live.sm.Phase(), replay.sm.Phase())
	require.Equal(t, live.sm.Balance(), replay.sm.Balance())
	liveRoot, err := live.sm.ComputeStateRoot()
	require.NoError(t, err)
	replayRoot, err := replay.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, liveRoot, replayRoot)
}
