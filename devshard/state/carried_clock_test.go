package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks/headertime"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// Step 5: two machines with empty header maps but identical carried stamp/floor
// times must agree on the Finished auto-seal clock gate.
func TestAutoSeal_EmptyMapsAgreeOnCarriedTimes(t *testing.T) {
	build := func(t *testing.T) *floorDriver {
		d := newFloorDriver(t)
		d.sm.SetHeaderTimes(headertime.New())
		hash := []byte{0xca}
		confirmAt := time.Hour
		d.raiseWithTime(1000, hash, confirmAt.Milliseconds())
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, 1000, hash, confirmAt.Milliseconds())))
		require.NoError(t, d.apply(d.confirmWithTime(id, 1000, hash, confirmAt.Milliseconds())))

		finish := &types.MsgFinishInference{
			InferenceId: id, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
			InputTokens: 10, OutputTokens: 20, ExecutorSlot: uint32(id % uint64(len(d.hosts))), EscrowId: "escrow-1",
			ObservedHeight: 1000, ObservedBlockHash: hash, ObservedTimeMs: confirmAt.Milliseconds(),
		}
		finish.ProposerSig = testutil.SignProposerTx(t, d.hosts[finish.ExecutorSlot], finish)
		require.NoError(t, d.apply(txFinish(finish)))

		grace := int64(d.sm.Config().InferenceSealGraceSeconds)
		exec := d.sm.Config().ExecutionTimeout
		required := time.Duration(FinishedClockRequiredSeconds(grace, exec)) * time.Second
		d.raiseWithTime(2000, []byte{0xcb}, (confirmAt + required).Milliseconds())
		d.raiseWithTime(2000+heightsync.SealDrainFloorLagBlocks, []byte{0xcc}, (confirmAt + required).Milliseconds())
		return d
	}

	a := build(t)
	b := build(t)
	ca := a.sm.AutoSealStateClock()
	cb := b.sm.AutoSealStateClock()
	require.True(t, ca.Known)
	require.Equal(t, ca, cb, "empty maps: seal clock is identical from carried floor/confirm times")
	require.Greater(t, ca.ClockMs, ca.MinConfirmedAtMs)
}

func TestDrainCreditUsesCarriedTimesWithEmptyMap(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	hash := []byte{0xdc}
	anchor := time.Hour
	refusal := time.Duration(d.sm.Config().RefusalTimeout) * time.Second

	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))

	// Not yet due: refund path (TimedOut).
	d.raiseWithTime(150, []byte{0xdd}, (anchor + refusal - time.Second).Milliseconds())
	sealNonce := d.next()
	d.sm.mu.Lock()
	live, _ := d.sm.inferenceForWriteLocked(id)
	due, err := d.sm.liveTimeoutDueAtDrainLocked(live, sealNonce)
	require.NoError(t, err)
	require.False(t, due)
	balance := d.sm.state.Balance
	d.sm.settleLiveRecordLocked(live, due)
	d.sm.mu.Unlock()
	require.Equal(t, types.StatusTimedOut, live.Status)
	require.Equal(t, balance+live.ReservedCost, d.sm.Balance())

	// Fresh escrow: due → credit Finished.
	d2 := newFloorDriver(t)
	d2.sm.SetHeaderTimes(headertime.New())
	d2.raiseWithTime(100, hash, anchor.Milliseconds())
	id2 := d2.next()
	require.NoError(t, d2.apply(startTxWithTime(id2, 100, hash, anchor.Milliseconds())))
	d2.raiseWithTime(200, []byte{0xde}, (anchor + refusal).Milliseconds())
	d2.raiseWithTime(200+heightsync.SealDrainFloorLagBlocks, []byte{0xdf}, (anchor + refusal).Milliseconds())
	seal2 := d2.next()
	d2.sm.mu.Lock()
	live2, _ := d2.sm.inferenceForWriteLocked(id2)
	due2, err := d2.sm.liveTimeoutDueAtDrainLocked(live2, seal2)
	require.NoError(t, err)
	require.True(t, due2)
	bal2 := d2.sm.state.Balance
	d2.sm.settleLiveRecordLocked(live2, due2)
	d2.sm.mu.Unlock()
	require.Equal(t, types.StatusFinished, live2.Status)
	require.Equal(t, bal2, d2.sm.Balance())
}

func TestMissingFloorTimeIsNotHeaderPending(t *testing.T) {
	d := newFloorDriver(t)
	d.sm.SetHeaderTimes(headertime.New())
	hash := []byte{0xef}
	d.raiseWithTime(100, hash, time.Hour.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, time.Hour.Milliseconds())))

	clock := d.sm.AutoSealStateClock()
	require.False(t, clock.Known, "a floor that is not yet four blocks behind skips the seal clock")

	sealNonce := d.next()
	d.sm.mu.Lock()
	err := d.sm.drainLiveIntoSealedAccLocked(sealNonce)
	d.sm.mu.Unlock()
	require.ErrorIs(t, err, types.ErrFloorTimeMissing)
	require.NotErrorIs(t, err, types.ErrHeaderPending)
}

// pairedFloorDrivers returns two machines that share hosts and user so signed
// diffs and height-sync turns stay interchangeable.
func pairedFloorDrivers(t *testing.T) (a, b *floorDriver) {
	t.Helper()
	hosts := fourHosts(t)
	smA, user := newTestSM(t, hosts, 1_000_000)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	smB, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000),
		WithStateRootAndProtocolVersion("v6"))
	require.NoError(t, err)
	return &floorDriver{t: t, sm: smA, user: user, hosts: hosts},
		&floorDriver{t: t, sm: smB, user: user, hosts: hosts}
}

// Step 6: auto-seal at a seal nonce with an empty map and no DiffRecord
// HeaderTimes succeeds when stamps carried times (terminal TimedOut skips the
// clock gate; nonce gate alone seals).
func TestAutoSeal_EmptyMapNoHeaderTimesRecord(t *testing.T) {
	d := newFloorDriver(t)
	spy := &catchUpTimes{headerTimes: newHeaderTimes()}
	d.sm.SetHeaderTimes(spy)
	d.sm.mu.Lock()
	d.sm.state.Config.AutoSealEveryNNonces = 1
	d.sm.state.Config.InferenceSealGraceNonces = 0
	d.sm.mu.Unlock()

	hash := []byte{0xa1}
	anchor := time.Hour
	d.raiseWithTime(100, hash, anchor.Milliseconds())
	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
	require.NoError(t, d.apply(d.confirmWithTime(id, 100, hash, anchor.Milliseconds())))
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)))

	_, live := d.sm.GetInference(id)
	require.False(t, live, "terminal TimedOut seals on the same nonce with empty map")
	require.Contains(t, d.sm.ExportSealedNonces(), id)
	require.Empty(t, spy.takeEnsured(), "apply must not call EnsureFrom / map catch-up")
}

// Step 6: drain on settlement with empty map; replay without HeaderTimes
// matches a replica that never had a map.
func TestDrainReplay_EmptyMapNoHeaderTimes(t *testing.T) {
	a, b := pairedFloorDrivers(t)
	a.sm.SetHeaderTimes(headertime.New())
	b.sm.SetHeaderTimes(headertime.New())

	hash := []byte{0xb2}
	anchor := time.Hour
	seed := func(d *floorDriver) uint64 {
		t.Helper()
		d.raiseWithTime(100, hash, anchor.Milliseconds())
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
		dueMs := (anchor + time.Duration(d.sm.Config().RefusalTimeout)*time.Second).Milliseconds()
		d.raiseWithTime(200, []byte{0xb3}, dueMs)
		return id
	}
	idA := seed(a)
	idB := seed(b)
	require.Equal(t, idA, idB)
	require.Equal(t, a.sm.LatestNonce(), b.sm.LatestNonce())

	var pages []types.DiffRecord
	record := func(txs ...*types.DevshardTx) {
		t.Helper()
		nonce := a.next()
		diff := testutil.SignDiff(t, a.user, "escrow-1", nonce, txs)
		root, err := a.sm.ApplyDiff(diff)
		require.NoError(t, err)
		require.NotErrorIs(t, err, types.ErrHeaderPending)
		pages = append(pages, types.DiffRecord{Diff: diff, StateHash: root})
	}
	record(txFinalize())
	for a.sm.Phase() != types.PhaseSettlement {
		record()
	}
	require.Equal(t, types.PhaseSettlement, a.sm.Phase())
	_, live := a.sm.GetInference(idA)
	require.False(t, live, "settlement drain folds live records with empty map")
	rootA, err := a.sm.ComputeStateRoot()
	require.NoError(t, err)

	for _, page := range pages {
		page.HeaderTimesKnown = true // legacy seed must be ignored
		page.HeaderTimes = []types.HeaderTimeRow{{Height: 999, TimeMs: 1}}
		_, err := b.sm.ApplyLocalPersistedRecord(page)
		require.NoError(t, err)
		require.NotErrorIs(t, err, types.ErrHeaderPending)
	}
	require.Equal(t, types.PhaseSettlement, b.sm.Phase())
	rootB, err := b.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootA, rootB, "replay without map/HeaderTimes matches live empty-map drain")
}

// Step 6: catch-up apply of a drain page with empty map is not ErrHeaderPending.
func TestCatchUpDrainPage_EmptyMapNoHeaderPending(t *testing.T) {
	leader, follower := pairedFloorDrivers(t)
	leader.sm.SetHeaderTimes(headertime.New())
	spy := &catchUpTimes{headerTimes: newHeaderTimes()}
	follower.sm.SetHeaderTimes(spy)

	hash := []byte{0xc4}
	anchor := time.Hour
	seed := func(d *floorDriver) {
		t.Helper()
		d.raiseWithTime(100, hash, anchor.Milliseconds())
		id := d.next()
		require.NoError(t, d.apply(startTxWithTime(id, 100, hash, anchor.Milliseconds())))
		dueMs := (anchor + time.Duration(d.sm.Config().RefusalTimeout)*time.Second).Milliseconds()
		d.raiseWithTime(200, []byte{0xc5}, dueMs)
	}
	seed(leader)
	seed(follower)
	require.Equal(t, leader.sm.LatestNonce(), follower.sm.LatestNonce())

	var drainPages []types.Diff
	applySigned := func(txs ...*types.DevshardTx) types.Diff {
		t.Helper()
		nonce := leader.next()
		diff := testutil.SignDiff(t, leader.user, "escrow-1", nonce, txs)
		_, err := leader.sm.ApplyDiff(diff)
		require.NoError(t, err)
		return diff
	}
	drainPages = append(drainPages, applySigned(txFinalize()))
	for leader.sm.Phase() != types.PhaseSettlement {
		drainPages = append(drainPages, applySigned())
	}

	for _, diff := range drainPages {
		started := time.Now()
		_, err := follower.sm.ApplyDiff(diff)
		require.NoError(t, err)
		require.NotErrorIs(t, err, types.ErrHeaderPending)
		require.Less(t, time.Since(started), 200*time.Millisecond)
	}
	require.Equal(t, types.PhaseSettlement, follower.sm.Phase())
	require.Empty(t, spy.takeEnsured(), "catch-up apply must not EnsureFrom")

	rootL, err := leader.sm.ComputeStateRoot()
	require.NoError(t, err)
	rootF, err := follower.sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootL, rootF)
}
