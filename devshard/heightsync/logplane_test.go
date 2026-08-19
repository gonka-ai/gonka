package heightsync_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/chainoracle/blocks"
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

func signedAck(t *testing.T, signer *signing.Secp256k1Signer, turn, ref uint64, slot uint32, height uint64, hash []byte, st types.SyncState) *types.MsgHeightAck {
	t.Helper()
	ack := &types.MsgHeightAck{
		TurnSeq:           turn,
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

func hbTx(turn, height, slots uint64, hash []byte, vec []*types.SyncVectorEntry) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		TurnSeq:           turn,
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
		Cfg:      cfg,
		EscrowID: "escrow-1",
	}, signers
}

func TestLogPlane_FabricatedAckRejected(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)}, 50)

	fake := signedAck(t, signers[0], 1, 1, 0, 50, hash, types.SyncState_SYNCED)
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
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)}, 50)

	ack := signedAck(t, signers[0], 1, 99, 0, 50, hash, types.SyncState_SYNCED)
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrAckCausality)
	require.Equal(t, "ack_causality", res.Reason)

	ack2 := signedAck(t, signers[0], 2, 1, 0, 50, hash, types.SyncState_SYNCED)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{signedAckTx(ack2)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrAckCausality)
}

func TestLogPlane_HeightRegressionAcrossNonces(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	st.MaxStampHeight = 80
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)},
	}, st)
	require.ErrorIs(t, res.Err, heightsync.ErrHeightRegression)
	require.Equal(t, "height_regression", res.Reason)
}

func TestLogPlane_UnstampedLegIsNotRegression(t *testing.T) {
	st, _ := baseState(t, 3)
	st.MaxStampHeight = 80
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   []*types.DevshardTx{hbTx(1, 50, 3, nil, nil)},
	}, st)
	require.NoError(t, res.Err)
}

func TestLogPlane_NoEnvelopeSkipsCrossPlaneChecks(t *testing.T) {
	st, signers := baseState(t, 3)
	hash := []byte{0xaa}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)}, 50)
	ack := signedAck(t, signers[0], 1, 1, 0, 50, hash, types.SyncState_SYNCED)
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
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)}, 50)
	ack := signedAck(t, signers[0], 1, 1, 0, 50, hash, types.SyncState_SYNCED)
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
		Txs:    []*types.DevshardTx{hbTx(1, 50, 3, wrong, nil)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err, "L6 must not INVALID the diff")
	require.NotEmpty(t, res.DeferredFails)
	require.Equal(t, heightsync.MarkDeferredFail, res.DeferredFails[0].Kind)
}

func TestHeightAck_FalseSyncedDeferredFail(t *testing.T) {
	st, signers := baseState(t, 3)
	goodHash := []byte{0xaa}
	badHash := []byte{0xbb}
	st.Tracker.Observe(1, []*types.DevshardTx{hbTx(1, 50, 3, goodHash, nil)}, 50)

	falseSynced := signedAck(t, signers[0], 1, 1, 0, 50, badHash, types.SyncState_SYNCED)
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

	honestStale := signedAck(t, signers[1], 1, 1, 1, 50, goodHash, types.SyncState_ORACLE_STALE)
	res = heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce:  3,
		Txs:    []*types.DevshardTx{signedAckTx(honestStale)},
		Oracle: or,
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkDeferredFail))
}

func TestSyncVector_AckedContradictsLog(t *testing.T) {
	st, _ := baseState(t, 3)
	hash := []byte{0xaa}
	vec := []*types.SyncVectorEntry{{
		SlotId: 0, Status: types.AckStatus_ACKED, ObservedHeight: 40, AckNonce: 9,
	}}
	res := heightsync.CheckDiffLogPlane(context.Background(), heightsync.LogPlaneInput{
		Nonce: 1,
		Txs:   []*types.DevshardTx{hbTx(2, 50, 3, hash, vec)},
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
		Txs:   []*types.DevshardTx{hbTx(2, 50, 3, hash, vec)},
	}, st)
	require.NoError(t, res.Err)
	require.False(t, hasMark(res.Marks, heightsync.MarkVectorContradiction))
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

func TestConfirm_TurnRule(t *testing.T) {
	tr := heightsync.NewTurnTracker(4, 3, heightsync.DefaultHeartbeatConfig())
	tr.Observe(10, []*types.DevshardTx{hbTx(1, 500, 4, []byte{1}, nil)}, 500)
	idx := heightsync.NewConfirmationIndex(heightsync.ConfirmationConfig{
		Roster: []string{"h1", "h2", "h3", "h4"},
		Quorum: 3,
		Rule:   heightsync.RuleTurn,
		Turns:  tr,
		Oracle: &mapOracle{latest: &blocks.Header{Height: 500, BlockHash: []byte{1}}},
	})
	require.Equal(t, heightsync.ConfirmPending, idx.IsStrictlyConfirmed(500))

	tr.Observe(14, []*types.DevshardTx{
		signedAckTx(&types.MsgHeightAck{TurnSeq: 1, RefNonce: 10, SlotId: 0, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
		signedAckTx(&types.MsgHeightAck{TurnSeq: 1, RefNonce: 10, SlotId: 1, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
		signedAckTx(&types.MsgHeightAck{TurnSeq: 1, RefNonce: 10, SlotId: 2, ObservedHeight: 500, SyncState: types.SyncState_SYNCED}),
	}, 500)
	require.Equal(t, heightsync.ConfirmConfirmed, idx.IsStrictlyConfirmed(500))
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
		Txs:   []*types.DevshardTx{hbTx(1, 50, 3, hash, nil)},
		Sec:   sec,
	}, heightsync.DefaultHeartbeatConfig())
	require.True(t, hasMark(marks, heightsync.MarkDisputeCarrier))
}

func TestMarks_RequestLegEvidenceVerifiesOffline(t *testing.T) {
	user := testutil.MustGenerateKey(t)
	body := []byte(`{"nonce":1,"height_sync":{"mainnet_height":40}}`)
	const ts int64 = 1_700_000_000 // 2023 — years outside the ±30s admission window
	msg := heightsync.CanonicalRequestLegBytes("escrow-1", body, ts)
	sig, err := user.Sign(msg)
	require.NoError(t, err)

	mark := heightsync.AttributableMark{
		Kind:      heightsync.MarkDisputeCarrier,
		Nonce:     1,
		Blob:      body,
		Sig:       sig,
		EscrowID:  "escrow-1",
		Timestamp: ts,
		Detail:    "heartbeat height 50 != request section 40",
	}
	addr, err := heightsync.VerifyRequestLegOffline(signing.NewSecp256k1Verifier(), heightsync.RequestLegEvidence{
		Body:      mark.Blob,
		Sig:       mark.Sig,
		Timestamp: mark.Timestamp,
		EscrowID:  mark.EscrowID,
	})
	require.NoError(t, err)
	require.Equal(t, user.Address(), addr)
	require.Contains(t, mark.Detail, "50")
	require.Contains(t, mark.Detail, "40")
}
