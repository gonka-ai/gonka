package state

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

type floorDriver struct {
	t     *testing.T
	sm    *StateMachine
	user  *signing.Secp256k1Signer
	hosts []*signing.Secp256k1Signer
	turn  uint64
}

func newFloorDriver(t *testing.T) *floorDriver {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	return &floorDriver{t: t, sm: sm, user: user, hosts: hosts}
}

func (d *floorDriver) next() uint64 { return d.sm.LatestNonce() + 1 }

func (d *floorDriver) apply(txs ...*types.DevshardTx) error {
	_, err := d.sm.ApplyDiff(testutil.SignDiff(d.t, d.user, "escrow-1", d.next(), txs))
	return err
}

func (d *floorDriver) raise(height uint64, hash []byte) {
	d.raiseWithTime(height, hash, 0)
}

// raiseClock stamps HeightClock time(height) so seal/drain/TimeoutDue can run
// from carried times without a header map.
func (d *floorDriver) raiseClock(height uint64, hash []byte) {
	d.raiseWithTime(height, hash, int64(height)*testutil.HeightClockBlockMs)
}

func (d *floorDriver) raiseWithTime(height uint64, hash []byte, timeMs int64) {
	d.t.Helper()
	d.turn++
	ref := d.next()
	require.NoError(d.t, d.apply(divHeartbeatTx(0, nil)))
	slot := uint32(d.turn % uint64(len(d.hosts)))
	require.NoError(d.t, d.apply(divAckTxWithTime(d.t, d.hosts[slot], d.turn, ref, slot, height, hash, types.SyncState_SYNCED, timeMs)))
	h, got, known := d.sm.HeightSyncFloorAsOf(d.next())
	require.True(d.t, known)
	require.Equal(d.t, height, h)
	require.Equal(d.t, hash, got)
}

func (d *floorDriver) start(id, height uint64, hash []byte) error {
	return d.apply(divStartTx(id, height, hash))
}

func (d *floorDriver) startClock(id, height uint64, hash []byte) error {
	return d.apply(startTxWithTime(id, height, hash, int64(height)*testutil.HeightClockBlockMs))
}

func (d *floorDriver) confirm(id, height uint64, hash []byte) *types.DevshardTx {
	rec, ok := d.sm.GetInference(id)
	require.True(d.t, ok)
	slot := int(rec.ExecutorSlot)
	return txConfirm(&types.MsgConfirmStart{
		InferenceId: id,
		ExecutorSig: testutil.SignExecutorReceipt(d.t, d.hosts[slot], "escrow-1", id, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens,
			testutil.ReceiptStamp{Height: height, Hash: hash}),
		ObservedHeight:    height,
		ObservedBlockHash: hash,
	})
}

func (d *floorDriver) confirmClock(id, height uint64, hash []byte) *types.DevshardTx {
	return d.confirmWithTime(id, height, hash, int64(height)*testutil.HeightClockBlockMs)
}

func (d *floorDriver) timeout(id uint64, reason types.TimeoutReason) *types.DevshardTx {
	votes := make([]*types.TimeoutVote, 0, len(d.hosts))
	for i, h := range d.hosts {
		vote := testutil.SignTimeoutVote(d.t, h, "escrow-1", id, reason, true)
		vote.VoterSlot = uint32(i)
		votes = append(votes, vote)
	}
	return &types.DevshardTx{Tx: &types.DevshardTx_TimeoutInference{TimeoutInference: &types.MsgTimeoutInference{
		InferenceId: id, Reason: reason, Votes: votes,
	}}}
}

func TestStartMustCarryExactlyTheFloor(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0x02, 0x50}
	d.raise(250, hash)

	id := d.next()
	require.ErrorIs(t, d.start(id, 251, hash), types.ErrStampNotFloor, "a stamp above the floor is a height the user made up")
	require.ErrorIs(t, d.start(id, 250, []byte{0x02, 0x51}), types.ErrStampNotFloor, "same height on another block")
	require.ErrorIs(t, d.start(id, 0, nil), types.ErrStampNotFloor, "an unstamped start skips the anchor the timeout needs")
	require.Error(t, d.start(id, 249, hash), "below the floor still invalidates the diff")

	require.NoError(t, d.start(id, 250, hash))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, uint64(250), rec.StartedAtHeight)
}

func TestStartOnNewProtocolRejectsEmptyFloor(t *testing.T) {
	spy := &rejectTimes{}
	d := newVersionedFloorDriver(t, types.StartRequiresFloorProtocolVersion, spy, 9)
	require.Equal(t, uint64(9), d.sm.HeartbeatConfig().DeltaBlocks)
	require.True(t, types.StartRequiresFloor(d.sm.ProtocolVersion()))

	require.ErrorIs(t, d.start(1, 0, nil), types.ErrStampNotFloor)
	require.ErrorIs(t, d.start(1, 10, []byte{0x01}), types.ErrStampNotFloor)
	require.Zero(t, spy.calls, "an empty floor is rejected before the map is read")
	_, ok := d.sm.GetInference(1)
	require.False(t, ok)
}

func TestStartOnNewProtocolArmsTimeoutFromTheMap(t *testing.T) {
	times := newHeaderTimes()
	const wantMs = int64(4_200_000)
	times.set(100, time.Duration(wantMs)*time.Millisecond)
	d := newVersionedFloorDriver(t, types.StartRequiresFloorProtocolVersion, times, 0)
	hash := []byte{0xaa}
	// v7 StampRequiresTime rejects zero stamp times; carry the map time.
	d.raiseWithTime(100, hash, wantMs)

	id := d.next()
	require.NoError(t, d.apply(startTxWithTime(id, 100, hash, wantMs)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, uint64(100), rec.StartedAtHeight)
	require.Equal(t, wantMs, rec.StartedAtTimeMs)

	_, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NotErrorIs(t, err, types.ErrTimeoutNotArmed)

	require.ErrorIs(t, d.start(id+1, 101, hash), types.ErrStampNotFloor)
	require.ErrorIs(t, d.start(id+1, 100, []byte{0xbb}), types.ErrStampNotFloor)
}

func TestStartOnPreviousProtocolAllowsEmptyFloor(t *testing.T) {
	d := newVersionedFloorDriver(t, "v6", newHeaderTimes(), 0)
	require.False(t, types.StartRequiresFloor(d.sm.ProtocolVersion()))
	require.NoError(t, d.start(1, 0, nil))
	rec, ok := d.sm.GetInference(1)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtTimeMs)
	_, err := d.sm.TimeoutRemaining(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, types.ErrTimeoutNotArmed)
}

func TestStartOnNewProtocolReplicaRejectsWithoutFloor(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	spy := &rejectTimes{}
	primary := newSMWithKeys(t, user, hosts, types.StartRequiresFloorProtocolVersion, spy, 9)
	replica := newSMWithKeys(t, user, hosts, types.StartRequiresFloorProtocolVersion, spy, 9)
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{divStartTx(1, 0, nil)})

	_, err := primary.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrStampNotFloor)
	_, err = replica.ApplyDiff(diff)
	require.ErrorIs(t, err, types.ErrStampNotFloor)
	require.Zero(t, spy.calls)
	require.Equal(t, uint64(9), replica.HeartbeatConfig().DeltaBlocks)
}

type rejectTimes struct {
	calls int
}

func (r *rejectTimes) HeaderTimeMs(uint64, []byte) (int64, error) {
	r.calls++
	return 0, types.ErrHeaderPending
}

func newVersionedFloorDriver(t *testing.T, version string, times HeaderTimeSource, delta uint64) *floorDriver {
	t.Helper()
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	sm := newSMWithKeys(t, user, hosts, version, times, delta)
	return &floorDriver{t: t, sm: sm, user: user, hosts: hosts}
}

func newSMWithKeys(t *testing.T, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer, version string, times HeaderTimeSource, delta uint64) *StateMachine {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000)
	opts := []SMOption{
		WithStateRootAndProtocolVersion(version),
		WithHeartbeatConfig(heightsync.HeartbeatConfig{DeltaBlocks: delta}),
	}
	if times != nil {
		opts = append(opts, WithHeaderTimes(times))
	}
	sm, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, store, opts...)
	require.NoError(t, err)
	return sm
}

func TestStartBeforeAnyHostStampHasNoAnchor(t *testing.T) {
	d := newVersionedFloorDriver(t, "v6", nil, 0)
	require.ErrorIs(t, d.start(1, 10, []byte{0x01}), types.ErrStampNotFloor, "there is no floor to carry yet")
	require.NoError(t, d.start(1, 0, nil))
	rec, ok := d.sm.GetInference(1)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtHeight)

	_, err := d.sm.TimeoutRemaining(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.ErrorIs(t, err, types.ErrTimeoutNotArmed)
	require.ErrorIs(t, d.apply(d.timeout(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED)), types.ErrTimeoutNotArmed)
}

func TestConfirmMayRaiseAndAnchorsOnItsOwnStamp(t *testing.T) {
	d := newFloorDriver(t)
	first, second := []byte{0x02, 0x50}, []byte{0x03, 0x00}
	d.raise(250, first)
	id := d.next()
	require.NoError(t, d.start(id, 250, first))
	d.raise(300, second)

	require.Error(t, d.apply(d.confirm(id, 249, first)), "a confirm below the start is rejected")
	require.ErrorIs(t, d.apply(d.confirm(id, 250, []byte{0x02, 0x51})), types.ErrStampNotFloor, "the same height on another block is rejected")
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.NoError(t, d.sm.CheckEvidence(&rec, d.confirm(id, 300, second)))

	require.NoError(t, d.apply(d.confirm(id, 300, second)))
	rec, ok = d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Equal(t, uint64(250), rec.StartedAtHeight)
	require.Equal(t, uint64(300), rec.ConfirmedAtHeight, "the record stores the confirm's stamp")
	require.Zero(t, rec.ConfirmedAtTimeMs)
}

func TestConfirmSeedsTheFloorWhenTheStartHadNone(t *testing.T) {
	d := newVersionedFloorDriver(t, "v6", nil, 0)
	require.NoError(t, d.start(1, 0, nil))
	hash := []byte{0x0a}
	require.NoError(t, d.apply(d.confirm(1, 40, hash)))
	rec, ok := d.sm.GetInference(1)
	require.True(t, ok)
	require.Zero(t, rec.StartedAtHeight)
	require.Equal(t, uint64(40), rec.ConfirmedAtHeight)
	require.Zero(t, rec.ConfirmedAtTimeMs)
	height, got, known := d.sm.HeightSyncFloorAsOf(d.next())
	require.True(t, known)
	require.Equal(t, uint64(40), height)
	require.Equal(t, hash, got)
}

func TestTimeoutIsDueOnlyOnceTheFloorClearsTheAnchor(t *testing.T) {
	d := newFloorDriver(t)
	refusalBlocks := uint64(d.sm.Config().RefusalTimeout)
	require.Positive(t, refusalBlocks)

	hash := []byte{0x01, 0x00}
	d.raiseClock(100, hash)
	id := d.next()
	require.NoError(t, d.startClock(id, 100, hash))

	d.raiseClock(100+refusalBlocks-1, []byte{0x01, 0x01})
	remaining, err := d.sm.TimeoutRemaining(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)
	require.NoError(t, err)
	require.Equal(t, time.Second, remaining)
	require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED), types.ErrTimeoutNotDue,
		"TimeoutDue ages from carried stamp/floor times")

	// Apply trusts votes only: a not-yet-due timeout with a quorum still commits.
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_REFUSED)))
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusTimedOut, rec.Status)
}

func TestExecutionTimeoutCountsFromTheConfirmAnchor(t *testing.T) {
	d := newFloorDriver(t)
	execBlocks := uint64(d.sm.Config().ExecutionTimeout)
	hash := []byte{0x01, 0x00}
	d.raiseClock(100, hash)
	id := d.next()
	require.NoError(t, d.startClock(id, 100, hash))
	d.raiseClock(150, []byte{0x01, 0x50})
	require.NoError(t, d.apply(d.confirmClock(id, 150, []byte{0x01, 0x50})))

	d.raiseClock(100+execBlocks, []byte{0x02, 0x00})
	require.ErrorIs(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), types.ErrTimeoutNotDue,
		"TimeoutDue opens at the confirm carried time, not the start")
	d.raiseClock(150+execBlocks, []byte{0x02, 0x01})
	require.NoError(t, d.sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
	require.NoError(t, d.apply(d.timeout(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION)))
}

func TestSettlementDoesNotPayAnUnanchoredReservation(t *testing.T) {
	d := newVersionedFloorDriver(t, "v6", nil, 0)
	require.NoError(t, d.start(1, 0, nil))
	rec, ok := d.sm.GetInference(1)
	require.True(t, ok)
	balance := d.sm.Balance()

	sealNonce := d.next()
	d.sm.mu.Lock()
	live, _ := d.sm.inferenceForWriteLocked(1)
	due, err := d.sm.liveTimeoutDueAtDrainLocked(live, sealNonce)
	require.NoError(t, err)
	d.sm.settleLiveRecordLocked(live, due)
	d.sm.mu.Unlock()

	require.Equal(t, types.StatusTimedOut, live.Status)
	require.Equal(t, balance+rec.ReservedCost, d.sm.Balance())
	stats, _ := d.sm.HostStatsFor(rec.ExecutorSlot)
	require.Zero(t, stats.Cost)
	require.Zero(t, stats.Missed)
}

func TestSettlementRefundsAnAnchoredReservationTheFloorNeverReached(t *testing.T) {
	for _, due := range []bool{false, true} {
		d := newFloorDriver(t)
		hash := []byte{0x01, 0x00}
		d.raiseClock(100, hash)
		id := d.next()
		require.NoError(t, d.startClock(id, 100, hash))
		if due {
			d.raiseClock(100+uint64(d.sm.Config().RefusalTimeout), []byte{0x02, 0x00})
		}
		rec, ok := d.sm.GetInference(id)
		require.True(t, ok)
		balance := d.sm.Balance()

		sealNonce := d.next()
		d.sm.mu.Lock()
		live, _ := d.sm.inferenceForWriteLocked(id)
		timeoutDue, err := d.sm.liveTimeoutDueAtDrainLocked(live, sealNonce)
		require.NoError(t, err)
		d.sm.settleLiveRecordLocked(live, timeoutDue)
		d.sm.mu.Unlock()

		stats, _ := d.sm.HostStatsFor(rec.ExecutorSlot)
		if due {
			require.Equal(t, types.StatusFinished, live.Status, "a timeout the user could have applied and did not is paid")
			require.Equal(t, rec.ReservedCost, stats.Cost)
			require.Equal(t, balance, d.sm.Balance())
		} else {
			require.Equal(t, types.StatusTimedOut, live.Status, "no timeout could ever have been due, so nothing is owed")
			require.Zero(t, stats.Cost)
			require.Equal(t, balance+rec.ReservedCost, d.sm.Balance())
		}
		require.Zero(t, stats.Missed)
	}
}

func seedFloor(t *testing.T, sm *StateMachine, height uint64, hash []byte) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.heightSyncFloor.Observe(sm.state.LatestNonce, []heightsync.FloorClaim{{
		Signer: 0, Height: height, Hash: hash, TimeMs: int64(height) * testutil.HeightClockBlockMs,
	}})
	p, known := sm.heightSyncFloor.PointAsOf(sm.state.LatestNonce + 1)
	require.True(t, known)
	require.Equal(t, height, p.Height)
	require.Equal(t, int64(height)*testutil.HeightClockBlockMs, p.TimeMs)
}

func makeTimeoutDue(t *testing.T, sm *StateMachine, ids ...uint64) {
	t.Helper()
	sm.mu.Lock()
	defer sm.mu.Unlock()
	var top uint64
	for _, id := range ids {
		rec, ok := sm.state.Inferences[id]
		if !ok {
			continue
		}
		if rec.StartedAtHeight == 0 {
			rec.StartedAtHeight = 1
		}
		if rec.Status == types.StatusStarted && rec.ConfirmedAtHeight == 0 {
			rec.ConfirmedAtHeight = 1
		}
		require.NoError(t, sm.updateCommittedEntryLocked(id, rec))
		top = max(top, rec.StartedAtHeight, rec.ConfirmedAtHeight)
	}
	due := top + uint64(max(sm.state.Config.RefusalTimeout, sm.state.Config.ExecutionTimeout))
	if cur, _ := sm.heightSyncFloor.Max(); cur > due {
		due = cur
	}
	sm.heightSyncFloor.Observe(sm.state.LatestNonce, []heightsync.FloorClaim{{Signer: 0, Height: due + 1, Hash: []byte{0xfe}}})
}

func restamp(t *testing.T, sm *StateMachine, hosts []*signing.Secp256k1Signer, escrowID string, nonce uint64, txs []*types.DevshardTx) []*types.DevshardTx {
	t.Helper()
	out := make([]*types.DevshardTx, 0, len(txs))
	for _, tx := range txs {
		switch {
		case tx.GetStartInference() != nil:
			msg := proto.Clone(tx.GetStartInference()).(*types.MsgStartInference)
			p, known := sm.HeightSyncFloorPointAsOf(nonce)
			if !known || p.Height == 0 {
				msg.ObservedHeight, msg.ObservedBlockHash, msg.ObservedTimeMs = 0, nil, 0
			} else {
				msg.ObservedHeight = p.Height
				msg.ObservedBlockHash = append([]byte(nil), p.Hash...)
				msg.ObservedTimeMs = p.TimeMs
			}
			tx = txStart(msg)
		case tx.GetConfirmStart() != nil:
			id := tx.GetConfirmStart().InferenceId
			rec, ok := sm.GetInference(id)
			if !ok {
				break
			}
			p, known := sm.HeightSyncFloorPointAsOf(id)
			var h uint64
			var hash []byte
			var timeMs int64
			if known && p.Height > 0 {
				h, hash, timeMs = p.Height, append([]byte(nil), p.Hash...), p.TimeMs
			}
			tx = txConfirm(&types.MsgConfirmStart{
				InferenceId: id,
				ExecutorSig: testutil.SignExecutorReceipt(t, hosts[rec.ExecutorSlot], escrowID, id, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens,
					testutil.ReceiptStamp{Height: h, Hash: hash, TimeMs: timeMs}),
				ObservedHeight:    h,
				ObservedBlockHash: hash,
				ObservedTimeMs:    timeMs,
			})
		}
		out = append(out, tx)
	}
	return out
}

func TestReceiptOutlivesThePrunedFloorHistory(t *testing.T) {
	d := newFloorDriver(t)
	hash := []byte{0x02, 0x50}
	d.raise(250, hash)
	id := d.next()
	require.NoError(t, d.start(id, 250, hash))

	for i := uint64(1); i <= heightsync.DefaultFloorWindow+1; i++ {
		d.raise(250+i, []byte{0x09, byte(i >> 8), byte(i)})
	}
	_, _, known := d.sm.HeightSyncFloorAsOf(id)
	require.False(t, known, "the fixture must push the assignment floor out of the window")

	receipt := d.confirm(id, 250, hash)
	rec, ok := d.sm.GetInference(id)
	require.True(t, ok)
	require.NoError(t, d.sm.CheckEvidence(&rec, receipt))
	require.NoError(t, d.apply(receipt))
	rec, _ = d.sm.GetInference(id)
	require.Equal(t, types.StatusStarted, rec.Status)
}
