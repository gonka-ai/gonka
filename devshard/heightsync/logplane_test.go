package heightsync_test

import (
	"context"
	"crypto/sha256"
	"testing"

	"github.com/stretchr/testify/require"

	"common/chainoracle/blocks"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

type mapOracle struct {
	latest *blocks.Header
	at     map[int64]*blocks.Header
}

func (o *mapOracle) Latest(context.Context) (*blocks.Header, error) {
	if o.latest == nil {
		return nil, context.Canceled
	}
	h := *o.latest
	h.BlockHash = append([]byte(nil), o.latest.BlockHash...)
	return &h, nil
}

func (o *mapOracle) At(_ context.Context, height int64) (*blocks.Header, error) {
	if o.at == nil {
		return nil, context.Canceled
	}
	hdr, ok := o.at[height]
	if !ok || hdr == nil {
		return nil, context.Canceled
	}
	h := *hdr
	h.BlockHash = append([]byte(nil), hdr.BlockHash...)
	return &h, nil
}

func (o *mapOracle) Prove(context.Context, string, int64) (*blocks.Proof, error) {
	return nil, blocks.ErrProveNotImplemented
}

func (o *mapOracle) Subscribe(context.Context, int64) (<-chan *blocks.Header, error) {
	ch := make(chan *blocks.Header)
	close(ch)
	return ch, nil
}

func testGroup(t *testing.T, n int) ([]*signing.Secp256k1Signer, map[uint32]string) {
	t.Helper()
	signers := make([]*signing.Secp256k1Signer, n)
	keys := make(map[uint32]string, n)
	for i := 0; i < n; i++ {
		signers[i] = testutil.MustGenerateKey(t)
		keys[uint32(i)] = signers[i].Address()
	}
	return signers, keys
}

func signedAck(t *testing.T, signer *signing.Secp256k1Signer, ref uint64, slot uint32, height uint64, hash []byte, st types.SyncState) *types.MsgHeightAck {
	t.Helper()
	ack := &types.MsgHeightAck{
		RefNonce:          ref,
		SlotId:            slot,
		ObservedHeight:    height,
		ObservedBlockHash: append([]byte(nil), hash...),
		SyncState:         st,
		PeerSeen:          []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(signer, ack))
	return ack
}

func hbTx(height, slots uint64, hash []byte, vec []*types.SyncVectorEntry) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SlotsNum:          slots,
		Reason:            string(heightsync.ReasonQuietSession),
		SyncVector:        vec,
	}}}
}

func signedAckTx(ack *types.MsgHeightAck) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
}

func baseState(t *testing.T, n int) (heightsync.LogPlaneState, []*signing.Secp256k1Signer) {
	t.Helper()
	signers, keys := testGroup(t, n)
	cfg := heightsync.DefaultHeartbeatConfig()
	return heightsync.LogPlaneState{
		SlotsNum: uint64(n),
		SlotKeys: keys,
		Verifier: signing.NewSecp256k1Verifier(),
		Tracker:  heightsync.NewTurnTracker(uint64(n), 0, cfg),
		Floor:    heightsync.NewFloorIndex(),
		Cfg:      cfg,
		EscrowID: "escrow-1",
	}, signers
}

// seedFloor puts one *host* signer's claim into the floor. Heights in these
// fixtures are inside W_conf of the standing floor, which is the unaided path.
// Sequencer heartbeats do not establish F (spec §14 rule 3).
func seedFloor(f *heightsync.FloorIndex, nonce, height uint64, hash []byte) {
	f.Observe(nonce, []heightsync.FloorClaim{{Signer: 0, Height: height, Hash: hash}})
}

func startTxAt(id, height uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{
		InferenceId: id, ObservedHeight: height, ObservedBlockHash: hash,
	}}}
}

func confirmTxAt(id, height uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: id, ObservedHeight: height, ObservedBlockHash: hash,
	}}}
}

func finishTxAt(id, height uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: id, ObservedHeight: height, ObservedBlockHash: hash,
	}}}
}

// ackTxAt builds an unsigned ack. Use where the test drives L0 rather than L2.
func ackTxAt(refNonce uint64, slot uint32, height uint64, hash []byte) *types.DevshardTx {
	return signedAckTx(&types.MsgHeightAck{
		RefNonce: refNonce, SlotId: slot,
		ObservedHeight: height, ObservedBlockHash: hash, PeerSeen: []byte{0xff},
	})
}

func TestLogPlane_FabricatedAckRejected(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)

	fake := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	fake.HostSig[0] ^= 0xff
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(fake)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrAckSigInvalid)
	require.Equal(t, "ack_sig_invalid", res.Reason)
}

func TestLogPlane_AckCausalityRejected(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)

	ack := signedAck(t, signers[0], 99, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrAckCausality)
	require.Equal(t, "ack_causality", res.Reason)

	// An ack naming a turn other than its ref_nonce's used to be the second half
	// of this check. It is unexpressible now: the turn follows from ref_nonce.
	ack2 := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack2)},
	}, st)
	require.NoError(t, res.Err, "ref_nonce 1 names a folded heartbeat")
}

func TestLogPlane_TwoHeartbeatsInDiffAckOfFirstAccepted(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	const landing = uint64(10)
	ack := signedAck(t, signers[0], landing, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: landing,
		Txs: []*types.DevshardTx{
			hbTx(50, 3, hash, nil),
			hbTx(50, 3, hash, nil),
			signedAckTx(ack),
		},
	}, st)
	require.NoError(t, res.Err, "L3 must accept an ack of the first heartbeat's nonce")
}

// TestLogPlane_TurnIdentityIsLogAssigned pins the property that replaced the
// stay-or-next framing rule: a turn is named by the nonce its span opens at, so
// a heartbeat has no field with which to claim one. The bound L1 used to enforce
// (reject a jump such as turn 2^60, which would have pruned the retain window)
// is not needed, because the identity is no longer the sender's to choose.
func TestLogPlane_TurnIdentityIsLogAssigned(t *testing.T) {
	hash := []byte{0xaa}

	t.Run("span_shares_one_turn", func(t *testing.T) {
		st, _ := baseState(t, 3)
		for nonce := uint64(1); nonce <= 3; nonce++ {
			res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
				Nonce: nonce,
				Txs:   []*types.DevshardTx{hbTx(50, 3, hash, nil)},
			}, st)
			require.NoError(t, res.Err)
			st.Tracker.Observe(nonce, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
			require.Equal(t, uint64(1), st.Tracker.LatestTurnStart(),
				"nonce %d is inside the span opened at 1", nonce)
		}
		require.Equal(t, 1, st.Tracker.TurnCount())
		require.Equal(t, [2]uint64{1, 3}, st.Tracker.Record(1).RequestSpan)
	})

	t.Run("next_span_opens_next_turn", func(t *testing.T) {
		st, _ := baseState(t, 3)
		st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
		st.Tracker.Observe(4, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
		require.Equal(t, uint64(4), st.Tracker.LatestTurnStart())
		require.NotNil(t, st.Tracker.Record(1), "opening a turn does not evict its predecessor")
		require.Equal(t, uint64(1), st.Tracker.TurnBefore(4))
	})

	t.Run("turn_ids_stay_inside_the_nonce_chain", func(t *testing.T) {
		// The old attack wrote turn_seq = 1<<60 and emptied the retain window.
		// A turn id is a nonce now, and applyCore only admits LatestNonce+1, so
		// the ids the tracker can ever hold are bounded by the log's own length.
		st, _ := baseState(t, 3)
		for nonce := uint64(1); nonce <= 10; nonce++ {
			st.Tracker.Observe(nonce, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
		}
		require.LessOrEqual(t, st.Tracker.LatestTurnStart(), uint64(10))
		require.NotNil(t, st.Tracker.Record(1), "the first turn is still inside retain")
	})

	t.Run("ack_of_unknown_ref_nonce_is_l3", func(t *testing.T) {
		st, signers := baseState(t, 3)
		st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
		ack := signedAck(t, signers[0], 99, 0, 50, hash, types.SyncState_SYNCED)
		res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
			Nonce: 2,
			Txs:   []*types.DevshardTx{signedAckTx(ack)},
		}, st)
		require.ErrorIs(t, res.Err, heightsync.ErrAckCausality)
		require.Equal(t, uint64(1), st.Tracker.LatestTurnStart())
		require.NotNil(t, st.Tracker.Record(1), "a rejected ack must not disturb turn 1")
	})
}

func TestLogPlane_LateAckAfterTurnPruneAccepted(t *testing.T) {
	st, signers := baseState(t, 3)
	const n = heightsync.DefaultTurnRetain + 5
	// One turn per span of 3, so turn starts are 1, 4, 7, ... — enough turns to
	// push the first one out of the retain window.
	for i := uint64(1); i <= n; i++ {
		nonce := 1 + (i-1)*3
		st.Tracker.Observe(nonce, []*types.DevshardTx{hbTx(0, 3, nil, nil)}, 0)
	}
	require.Nil(t, st.Tracker.Record(1), "turn record past retain is gone")
	start, ok := st.Tracker.HeartbeatTurn(1)
	require.True(t, ok, "heartbeatAt must survive turn prune")
	require.Equal(t, uint64(1), start)

	hash := []byte{0xaa}
	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: n + 1,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.NoError(t, res.Err, "L3 must accept an ack whose heartbeat is journaled after the turn record is pruned")
}

func TestCheckDiffLogPlane_LongOpenSessionBounded(t *testing.T) {
	st, _ := baseState(t, 3)
	const n = 5000
	for i := uint64(1); i <= n; i++ {
		st.Tracker.Observe(i, []*types.DevshardTx{hbTx(0, 3, nil, nil)}, 0)
	}
	require.LessOrEqual(t, st.Tracker.TurnCount(), int(heightsync.DefaultTurnRetain)+1)

	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: n + 1,
		Txs:   []*types.DevshardTx{hbTx(0, 3, nil, nil)},
	}, st)
	require.NoError(t, res.Err)
}

func TestLogPlane_RefStampBelowFloorRejected(t *testing.T) {
	// L0 on a reference height: the floor as of the producing nonce is 80, so a
	// confirm claiming 50 is a regression no honest producer could author — it
	// had the log and could have carried 80 or omitted the stamp.
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	seedFloor(st.Floor, 1, 80, hash)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{confirmTxAt(5, 50, hash)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression)
	require.Equal(t, "height_regression", res.Reason)
}

func TestLogPlane_AckBelowFloorRejectedAndLiftAccepted(t *testing.T) {
	// The log has one height semantics, so an ack is under L0 exactly like an
	// executor receipt: below F(m) is a regression, and a lagging host clears the
	// bar by lifting to the floor rather than by writing a lower number. Its real
	// follower tip is reported in the response-leg Anchor and sync_state instead,
	// where divergence is monitored (spec §14).
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	seedFloor(st.Floor, 1, 80, hash)
	st.Tracker.Observe(2, []*types.DevshardTx{hbTx(80, 3, hash, nil)}, 80)

	low := signedAck(t, signers[0], 2, 0, 50, hash, types.SyncState_CATCHING_UP)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 3,
		Txs:   []*types.DevshardTx{signedAckTx(low)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression,
		"an ack below F(ref_nonce) could have carried the floor instead")

	lift := signedAck(t, signers[0], 2, 0, 80, hash, types.SyncState_CATCHING_UP)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 3,
		Txs:   []*types.DevshardTx{signedAckTx(lift)},
	}, st)
	require.NoError(t, res.Err,
		"lifting to the floor is always available, so a lagging host is never forced into an invalid diff")

	// A heartbeat below the floor is the same violation, one signer over.
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 4,
		Txs:   []*types.DevshardTx{hbTx(50, 3, hash, nil)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression)
}

func TestLogPlane_SequencerStampAboveFloorWindowRejected(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	seedFloor(st.Floor, 1, 80, hash)
	w := st.Cfg.WindowBlocks
	over := 80 + w + 1

	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(over, 3, hash, nil)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightUnbacked)
	require.Equal(t, "height_unbacked", res.Reason)

	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{startTxAt(2, over, hash)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightUnbacked)

	atCap := 80 + w
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(atCap, 3, hash, nil)},
	}, st)
	require.NoError(t, res.Err, "F+W_conf is still a plausible carry")

	// Host stamps are not this cap: unaided / Q bound F, not L0.
	st.Tracker.Observe(2, []*types.DevshardTx{hbTx(80, 3, hash, nil)}, 80)
	ack := signedAck(t, signers[0], 2, 0, over, hash, types.SyncState_SYNCED)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 3,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.NoError(t, res.Err)

	empty := heightsync.NewFloorIndex()
	st.Floor = empty
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(1<<40, 3, hash, nil)},
	}, st)
	require.NoError(t, res.Err, "no floor yet: the turn clock, not L0, ignores the user number")
}

func TestLogPlane_AckJudgedAgainstRefNonceFloor(t *testing.T) {
	// The ack half of the producing-nonce rule, and what makes bringing acks
	// under L0 safe at all. The ack answers the heartbeat at nonce 2, where the
	// floor was 80. It lands at nonce 6, by which time another party pushed the
	// floor to 100. Judged against the landing floor an honest ack would fail
	// whenever the pipeline was busy — and late acks (§10.6) always would.
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	seedFloor(st.Floor, 1, 80, hash)
	st.Tracker.Observe(2, []*types.DevshardTx{hbTx(80, 3, hash, nil)}, 80)
	seedFloor(st.Floor, 5, 100, hash)

	ack := signedAck(t, signers[0], 2, 0, 80, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.NoError(t, res.Err, "80 >= F(2)=80; the landing floor of 100 is not its basis")

	below := signedAck(t, signers[0], 2, 0, 79, hash, types.SyncState_SYNCED)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{signedAckTx(below)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression,
		"below its own producing floor is still a regression")
}

func TestLogPlane_UnstampedLegIsNotRegression(t *testing.T) {
	st, _ := baseState(t, 3)
	seedFloor(st.Floor, 1, 80, []byte{0xaa})
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(50, 3, nil, nil)},
	}, st)
	require.NoError(t, res.Err)

	// Same for a reference leg: absence is always legal, at any floor. Otherwise
	// a present-then-absent pair reads as a regression on every verifier.
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{confirmTxAt(5, 0, nil)},
	}, st)
	require.NoError(t, res.Err)
}

func TestLogPlane_ConfirmJudgedAgainstProducingNonce(t *testing.T) {
	// The pipelining case, and the reason the basis is the producing nonce rather
	// than the landing nonce. The confirm for inference 3 was made when the floor
	// was 80; it lands at nonce 6, by which time another party has pushed the
	// floor to 100. Judging it against the landing floor would reject a stamp
	// whose producer could not possibly have known 100 — which is exactly the
	// failure the E2E courier scenarios hit.
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	seedFloor(st.Floor, 1, 80, hash)
	seedFloor(st.Floor, 5, 100, hash)

	floorAtProducer, _, known := st.Floor.AsOf(3)
	require.True(t, known)
	require.Equal(t, uint64(80), floorAtProducer, "fixture must exercise a floor that rose after production")

	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{confirmTxAt(3, 90, hash)},
	}, st)
	require.NoError(t, res.Err, "90 >= F(3)=80; the landing floor of 100 is not its basis")

	// Below its own producing floor is still a regression.
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 6,
		Txs:   []*types.DevshardTx{confirmTxAt(3, 79, hash)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression)
}

func TestLogPlane_PerInferenceHeightOrder(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}

	// start is user-signed and carries the roster maximum; confirm is the
	// executor's own view. An executor legitimately behind that maximum is not a
	// regression, so this cross-signer pair is deliberately not compared.
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs: []*types.DevshardTx{
			startTxAt(1, 100, hash),
			confirmTxAt(1, 90, hash),
		},
	}, st)
	require.NoError(t, res.Err, "confirm below start across signers is honest lag, not a regression")

	// confirm and finish are both executor-signed, so this pair is genuine
	// per-signer monotonicity and stays enforced.
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs: []*types.DevshardTx{
			confirmTxAt(1, 90, hash),
			finishTxAt(1, 89, hash),
		},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression)
	require.Equal(t, "height_regression", res.Reason)
}

func TestLogPlane_NoEnvelopeSkipsCrossPlaneChecks(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	in := heightsync.LogPlaneInput{
		Nonce:        2,
		Txs:          []*types.DevshardTx{signedAckTx(ack)},
		LocalAligned: 10_000,
	}
	without := heightsync.CheckDiffLogPlane(context.Background(), in, st)
	require.NoError(t, without.Err)

	sec := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       99,
		MainnetBlockHashHex: "aabb",
		Direction:           "response",
	}
	in.Sec = sec
	withSec := heightsync.CheckDiffLogPlane(context.Background(), in, st)
	require.NoError(t, withSec.Err, "L4/L5a must not INVALID")
	require.True(t, hasMark(withSec.Marks, heightsync.MarkDisputeOriginator) || hasMark(withSec.Marks, heightsync.MarkAdmissionDelta))
	require.False(t, hasMark(without.Marks, heightsync.MarkDisputeOriginator))
	require.False(t, hasMark(without.Marks, heightsync.MarkAdmissionDelta))
}

func TestLogPlane_HistoricalReplayNoInvalidation(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)
	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:        2,
		Txs:          []*types.DevshardTx{signedAckTx(ack)},
		LocalAligned: 50_000, // far ahead of stamps; no envelope
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkAdmissionDelta))
}

func TestLogPlane_FutureDatedStampDeferredFail(t *testing.T) {
	st, _ := baseState(t, 3)
	wrong := []byte{0x01}
	canon := []byte{0x02}
	or := &mapOracle{
		latest: &blocks.Header{Height: 50, BlockHash: canon},
		at: map[int64]*blocks.Header{
			50: {Height: 50, BlockHash: canon},
		},
	}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  1,
		Txs:    []*types.DevshardTx{hbTx(50, 3, wrong, nil)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err, "L6 must not INVALID the diff")
	require.NotEmpty(t, res.DeferredFails)
	require.Equal(t, heightsync.MarkDeferredFail, res.DeferredFails[0].Kind)
}

func TestLogPlane_L6SkipsDummyOracleHeader(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0x01}
	or := &mapOracle{
		latest: blocks.DummyHeader(50),
		at:     map[int64]*blocks.Header{50: blocks.DummyHeader(50)},
	}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  1,
		Txs:    []*types.DevshardTx{hbTx(50, 3, hash, nil)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err)
	require.Empty(t, res.DeferredFails)
}

func TestHeightAck_FalseSyncedDeferredFail(t *testing.T) {
	st, signers := baseState(t, 3)
	goodHash := []byte{0xaa}
	badHash := []byte{0xbb}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, goodHash, nil)}, 50)

	falseSynced := signedAck(t, signers[0], 1, 0, 50, badHash, types.SyncState_SYNCED)
	or := &mapOracle{
		latest: &blocks.Header{Height: 50, BlockHash: goodHash},
		at:     map[int64]*blocks.Header{50: {Height: 50, BlockHash: goodHash}},
	}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  2,
		Txs:    []*types.DevshardTx{signedAckTx(falseSynced)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err)
	require.True(t, hasMark(res.Marks, heightsync.MarkDeferredFail))

	honestStale := signedAck(t, signers[1], 1, 1, 50, goodHash, types.SyncState_ORACLE_STALE)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  3,
		Txs:    []*types.DevshardTx{signedAckTx(honestStale)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkDeferredFail))
}

// TestLogPlane_L6BlamesTheFloorsAuthorForACarriedPair: when a producer carries
// F(m), an unreconcilable pair must blame the floor's author, not every carrier.
//
// The producer rule gives a party behind the floor one legal value — F(m) — so
// an unreconcilable pair does not stay with its author: every honest carrier
// repeats it and would otherwise collect an L6 mark for it. That is only
// acceptable if the mark says where the pair came from, which the log can always
// answer, because a carry is by construction identical to a floor entry and the
// floor records who set it.
func TestLogPlane_L6BlamesTheFloorsAuthorForACarriedPair(t *testing.T) {
	st, signers := baseState(t, 3)
	dead, live := []byte{0xde}, []byte{0xad}
	st.Floor.Observe(1, []heightsync.FloorClaim{{Signer: 2, Height: 80, Hash: dead}})
	st.Tracker.Observe(2, []*types.DevshardTx{hbTx(80, 3, dead, nil)}, 80)
	or := &mapOracle{
		latest: &blocks.Header{Height: 90, BlockHash: live},
		at: map[int64]*blocks.Header{
			80: {Height: 80, BlockHash: live},
			90: {Height: 90, BlockHash: live},
		},
	}

	carried := signedAck(t, signers[0], 2, 0, 80, dead, types.SyncState_CATCHING_UP)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  3,
		Txs:    []*types.DevshardTx{signedAckTx(carried)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err, "L6 never invalidates")
	require.Len(t, res.DeferredFails, 1)
	require.Equal(t, uint32(0), res.DeferredFails[0].Slot, "the carrier is still named: it signed the tx")
	require.Equal(t, "slot 2", res.DeferredFails[0].Origin,
		"blame for the pair itself follows the floor's author")
	require.Equal(t, uint64(1), res.DeferredFails[0].OriginNonce)

	// A pair the signer chose itself has no origin to point at.
	own := signedAck(t, signers[1], 2, 1, 90, dead, types.SyncState_SYNCED)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  3,
		Txs:    []*types.DevshardTx{signedAckTx(own)},
		Oracle: or,
	}, st)
	require.Len(t, res.DeferredFails, 1)
	require.Empty(t, res.DeferredFails[0].Origin, "a first-party claim is its own author")
}

func TestSyncVector_AckedContradictsLog(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	vec := []*types.SyncVectorEntry{{
		SlotId: 0, Status: types.AckStatus_ACKED, ObservedHeight: 40, AckNonce: 9,
	}}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(50, 3, hash, vec)},
	}, st)
	require.NoError(t, res.Err)
	require.True(t, hasMark(res.Marks, heightsync.MarkVectorContradiction))
}

func TestRepairProbe_HeightNoBlame(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	vec := []*types.SyncVectorEntry{{
		SlotId: 0, Status: types.AckStatus_MISSING,
	}}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(50, 3, hash, vec)},
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkVectorContradiction))
}

func TestLogPlane_L7SameDiffAckSatisfiesVector(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 3, hash, nil)}, 50)

	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	vec := []*types.SyncVectorEntry{{
		SlotId: 0, Status: types.AckStatus_ACKED, ObservedHeight: 50, AckNonce: 4,
	}}
	// The next turn opens at 4: nonces 1..3 belong to the span already open, so a
	// heartbeat inside them would join that turn instead of starting one.
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 4,
		Txs: []*types.DevshardTx{
			signedAckTx(ack),
			hbTx(50, 3, hash, vec),
		},
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkVectorContradiction),
		"an ack in the same diff that completes the preceding turn must satisfy L7")
}

func BenchmarkCheckDiffLogPlane_LongSession(b *testing.B) {
	st, signers := baseState(&testing.T{}, 3)
	hash := []byte{0xaa}
	const n = 500
	for i := uint64(1); i <= n; i++ {
		h := 100 + i
		txs := []*types.DevshardTx{hbTx(h, 3, hash, nil)}
		for s := uint32(0); s < 2; s++ {
			txs = append(txs, signedAckTx(signedAck(&testing.T{}, signers[s], i, s, h, hash, types.SyncState_SYNCED)))
		}
		st.Tracker.Observe(i, txs, h)
	}
	if st.Tracker.TurnCount() > int(heightsync.DefaultTurnRetain)+1 {
		b.Fatalf("turn map not bounded by retain: got %d", st.Tracker.TurnCount())
	}
	hb := hbTx(100+n+1, 3, hash, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
			Nonce: n + 1,
			Txs:   []*types.DevshardTx{hb},
		}, st)
	}
}

func TestLogPlane_PerInferenceHeightOrderSkippedWithoutStamps(t *testing.T) {
	st, _ := baseState(t, 3)
	start := &types.DevshardTx{Tx: &types.DevshardTx_StartInference{StartInference: &types.MsgStartInference{InferenceId: 1}}}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{start},
	}, st)
	require.NoError(t, res.Err)
}

func hasMark(marks []heightsync.AttributableMark, kind heightsync.MarkKind) bool {
	for _, m := range marks {
		if m.Kind == kind {
			return true
		}
	}
	return false
}

// TestTurnComplete_IsNotAHeightCertificate pins the soundness argument that
// withdrew (C-turn): a complete turn is a reachability certificate, not a
// height certificate. Every ack carries a reference height, so a host whose
// follower never reached 500 still acks 500 by lifting to a floor another party
// set. Counting Q of those must not be treated as Q independent height witnesses.
func TestTurnComplete_IsNotAHeightCertificate(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{hbTx(500, 4, []byte{1}, nil)}, 500)

	tr.Observe(14, []*types.DevshardTx{
		signedAckTx(&types.MsgHeightAck{RefNonce: 10, SlotId: 0, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
		signedAckTx(&types.MsgHeightAck{RefNonce: 10, SlotId: 1, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
		signedAckTx(&types.MsgHeightAck{RefNonce: 10, SlotId: 2, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
	}, 500)
	require.True(t, tr.CompletedAtOrAbove(500), "the turn itself completed: Q slots were reachable")
}

func TestLogPlane_CheckEnvelopeBindingIndependent(t *testing.T) {
	hash := []byte{0xaa}
	sec := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       40,
		MainnetBlockHashHex: "aa",
		Direction:           "request",
	}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(30, 3, hash, nil)},
		Sec:   sec,
	}, heightsync.DefaultHeartbeatConfig())
	require.True(t, hasMark(marks, heightsync.MarkDisputeCarrier))
}

// TestLogPlane_HeartbeatLiftDoesNotTripEnvelopeBinding is the request-leg mirror
// of TestLogPlane_AckLiftDoesNotTripEnvelopeBinding. The section carries the
// sequencer's own oracle read and the heartbeat carries a reference height, so a
// lagging sequencer writes 50 in the log and 40 on the wire — the producer rule
// obliging it to lift to F(m). Strict equality named all of them carriers of a
// dispute while catching no attacker: a sequencer inventing a height simply puts
// the same lie in both fields.
func TestLogPlane_HeartbeatLiftDoesNotTripEnvelopeBinding(t *testing.T) {
	hash := []byte{0xaa}
	sec := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       40,
		MainnetBlockHashHex: "aa",
		Direction:           "request",
	}
	floor := heightsync.NewFloorIndex()
	seedFloor(floor, 1, 50, hash)

	check := func(h uint64, f *heightsync.FloorIndex) []heightsync.AttributableMark {
		return heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
			Nonce: 2,
			Txs:   []*types.DevshardTx{hbTx(h, 3, hash, nil)},
			Sec:   sec,
			Floor: f,
		}, heightsync.DefaultHeartbeatConfig())
	}

	require.False(t, hasMark(check(50, floor), heightsync.MarkDisputeCarrier),
		"lifting to F(m) is the producer rule, not a self-contradiction")
	require.True(t, hasMark(check(51, floor), heightsync.MarkDisputeCarrier),
		"one block above both its own read and the floor is a claim it cannot justify")
	require.True(t, hasMark(check(30, floor), heightsync.MarkDisputeCarrier),
		"a heartbeat under the height its own signed envelope reports is the contradiction L4 exists for")

	// Transport-edge callers hold no floor, so the exact bound degrades to the
	// half that is checkable without one — exactly as the ack leg degrades.
	require.False(t, hasMark(check(51, nil), heightsync.MarkDisputeCarrier),
		"without a floor a lift is indistinguishable from an honest one")
	require.True(t, hasMark(check(30, nil), heightsync.MarkDisputeCarrier),
		"understatement needs no floor to detect")
}

// TestLogPlane_CarryForwardSectionSkipsHeartbeatBinding pins the scope of the
// request leg. A carried peer tip is nobody's first-party read, so there is no
// self-contradiction available to detect and binding the heartbeat to it would
// blame the relayer for the originator's number.
func TestLogPlane_CarryForwardSectionSkipsHeartbeatBinding(t *testing.T) {
	hash := []byte{0xaa}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(30, 3, hash, nil)},
		Sec: &heightsync.HeightSyncSection{
			ProofType:             heightsync.AnchorProofType,
			MainnetHeight:         900,
			MainnetBlockHashHex:   "aa",
			Direction:             "request",
			OriginatorSenderID:    "gonka1peer",
			OriginatorTimestampMs: 1_700_000_000_000,
		},
	}, heightsync.DefaultHeartbeatConfig())
	require.False(t, hasMark(marks, heightsync.MarkDisputeCarrier),
		"a relayed tip is not the sequencer's own claim to contradict")
}

// TestLogPlane_AckLiftDoesNotTripEnvelopeBinding is the honest path of L4's
// asymmetric rule. The ack's Diff height is a reference height and the
// response-leg Anchor is the host's raw reading, so on a lagging host the two
// legitimately differ: 50 in the log, 40 on the wire. Requiring them equal would
// mark every honest lagging host as disputing itself. The heartbeat leg is bound
// the same way for the same reason — see
// TestLogPlane_HeartbeatLiftDoesNotTripEnvelopeBinding.
//
// The bound is still exact, so the one-block lie L4 exists to catch is caught:
// 51 clears neither the anchor nor the floor and is marked.
func TestLogPlane_AckLiftDoesNotTripEnvelopeBinding(t *testing.T) {
	_, signers := baseState(t, 3)
	hash := []byte{0xaa}
	sec := &heightsync.HeightSyncSection{
		ProofType:           heightsync.AnchorProofType,
		MainnetHeight:       40,
		MainnetBlockHashHex: "aa",
		Direction:           "response",
	}
	floor := heightsync.NewFloorIndex()
	seedFloor(floor, 10, 50, hash) // the heartbeat this ack answers carried 50

	check := func(ackHeight uint64) []heightsync.AttributableMark {
		ack := signedAck(t, signers[0], 10, 0, ackHeight, hash, types.SyncState_CATCHING_UP)
		return heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
			Nonce: 12,
			Txs:   []*types.DevshardTx{signedAckTx(ack)},
			Sec:   sec,
			Floor: floor,
		}, heightsync.DefaultHeartbeatConfig())
	}

	require.False(t, hasMark(check(50), heightsync.MarkDisputeOriginator),
		"lifting to F(ref_nonce+1) is the producer rule, not a self-contradiction")
	require.True(t, hasMark(check(51), heightsync.MarkDisputeOriginator),
		"one block above both the anchor and the floor is still a mark")
	require.True(t, hasMark(check(40), heightsync.MarkDisputeOriginator),
		"the raw anchor value is below the floor, so it is not a legal ack either")
}

func TestMarks_RequestLegEvidenceVerifiesOffline(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	body := []byte(`{"nonce":1,"height_sync":{"mainnet_height":40}}`)
	const ts int64 = 1_700_000_000 // 2023 — years outside the ±30s admission window
	digest := heightsync.CanonicalRequestLegBytes("escrow-1", body, ts)
	sig, err := user.Sign(digest)
	require.NoError(t, err)

	mark := heightsync.AttributableMark{
		Kind:      heightsync.MarkDisputeCarrier,
		Nonce:     1,
		Blob:      digest,
		Sig:       sig,
		EscrowID:  "escrow-1",
		Timestamp: ts,
		Detail:    "heartbeat height 30 != max(request section 40, floor) = 40",
	}
	addr, err := heightsync.VerifyRequestLegMark(signing.NewSecp256k1Verifier(), mark)
	require.NoError(t, err)
	require.Equal(t, user.Address(), addr)
	addr, err = heightsync.VerifyRequestLegOffline(signing.NewSecp256k1Verifier(), heightsync.RequestLegEvidence{
		Body:      body,
		Sig:       sig,
		Timestamp: ts,
		EscrowID:  "escrow-1",
	})
	require.NoError(t, err)
	require.Equal(t, user.Address(), addr)
	require.Contains(t, mark.Detail, "30")
	require.Contains(t, mark.Detail, "40")
}

func TestCheckEnvelopeBinding_RequestLegBlobBounded(t *testing.T) {
	hash := []byte{0xaa}
	body := make([]byte, 100_000)
	for i := range body {
		body[i] = 'x'
	}
	marks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(30, 3, hash, nil)},
		Sec: &heightsync.HeightSyncSection{
			ProofType:           heightsync.AnchorProofType,
			MainnetHeight:       40,
			MainnetBlockHashHex: "aa",
			Direction:           "request",
		},
		RequestLeg: &heightsync.RequestLegEvidence{
			Body:      body,
			Sig:       []byte{0x01},
			Timestamp: 1_700_000_000,
			EscrowID:  "escrow-1",
		},
	}, heightsync.DefaultHeartbeatConfig())
	require.True(t, hasMark(marks, heightsync.MarkDisputeCarrier))
	for _, m := range marks {
		require.LessOrEqual(t, len(m.Blob), heightsync.MaxMarkBlobBytes)
		require.Equal(t, sha256.Size, len(m.Blob), "request-leg L4 stores CanonicalRequestLegBytes")
		require.Equal(t, heightsync.CanonicalRequestLegBytes("escrow-1", body, 1_700_000_000), m.Blob)
	}
}

func TestLogPlane_AckWithoutVerifierRejected(t *testing.T) {
	st, signers := baseState(t, 2)
	st.Verifier = nil
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 2, hash, nil)}, 50)

	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrAckSigInvalid)
	require.Equal(t, "ack_sig_invalid", res.Reason)
}

func TestLogPlane_HeartbeatWithoutVerifierOK(t *testing.T) {
	st, _ := baseState(t, 2)
	st.Verifier = nil
	hash := []byte{0xaa}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(50, 2, hash, nil)},
	}, st)
	require.NoError(t, res.Err)
}

func TestLogPlane_OversizedFieldsRejected(t *testing.T) {
	st, signers := baseState(t, 2)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(50, 2, hash, nil)}, 50)

	tooBigHash := make([]byte, heightsync.MaxObservedBlockHashBytes+1)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(50, 2, tooBigHash, nil)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrBadFraming)

	vec := make([]*types.SyncVectorEntry, int(st.SlotsNum)+1)
	for i := range vec {
		vec[i] = &types.SyncVectorEntry{SlotId: uint32(i % 2), Status: types.AckStatus_MISSING}
	}
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(50, 2, hash, vec)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrBadFraming)

	ack := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	ack.PeerSeen = []byte{0xff, 0xff}
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrBadFraming)

	ack2 := signedAck(t, signers[0], 1, 0, 50, hash, types.SyncState_SYNCED)
	ack2.ObservedBlockHash = tooBigHash
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack2)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrBadFraming)
}
