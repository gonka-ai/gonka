package state

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// newFloorTestSM builds a verifier for an escrow whose user key is chosen by the
// caller, so two independently constructed state machines can ingest the same
// signed diffs.
func newFloorTestSM(t *testing.T, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer) *StateMachine {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	sm, err := NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(),
		signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000))
	require.NoError(t, err)
	return sm
}

func fourHosts(t *testing.T) []*signing.Secp256k1Signer {
	t.Helper()
	return []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
}

// TestHeightSyncFloor_ImplausibleClaimIsMarkedAndIgnored checks the consensus
// path for the unaided raise bound (proposal §14): a lone host claim above
// W_conf is marked and does not become the escrow's floor.
//
// The claim is valid — it is above the floor, and L0 asks nothing else of it — so
// the diff applies and the escrow keeps serving. What it does not do is become
// the escrow's logical time: uncorroborated, it moves the floor nowhere, so the
// next honest party is not facing a bar no chain will reach for a century, and
// the attempt is on the record against the slot that signed it.
func TestHeightSyncFloor_ImplausibleClaimIsMarkedAndIgnored(t *testing.T) {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}
	good, poison := []byte{0x02, 0x50}, []byte{0xff, 0xff}

	require.NoError(t, apply(1, divHeartbeatTx(1, 250, good)))
	require.NoError(t, apply(2,
		divAckTx(t, hosts[1], 1, 1, 1, 250, good, types.SyncState_SYNCED),
	), "a host ack inside W_conf of an empty floor seeds F")
	require.NoError(t, apply(3,
		divAckTx(t, hosts[0], 1, 1, 0, math.MaxUint64/2, poison, types.SyncState_SYNCED),
	), "a height above the floor is valid; L0 has no plausibility to check")

	floor, hash, known := sm.HeightSyncFloorAsOf(4)
	require.True(t, known)
	require.Equal(t, uint64(250), floor, "one signer cannot make its own claim the escrow's logical time")
	require.Equal(t, good, hash)

	marks := sm.HeightSyncMarks()
	require.Len(t, marks, 1)
	require.Equal(t, heightsync.MarkFloorOutOfBand, marks[0].Kind)
	require.Equal(t, uint32(0), marks[0].Slot, "named at the moment of the damage, not by later forensics")
	require.Equal(t, uint64(3), marks[0].Nonce)

	// Liveness: the escrow neither halts nor loses its ability to advance.
	require.NoError(t, apply(4, divHeartbeatTx(2, 260, good)))
	floor, _, _ = sm.HeightSyncFloorAsOf(5)
	require.Equal(t, uint64(250), floor, "a sequencer heartbeat still does not raise F")
	require.NoError(t, apply(5, divAckTx(t, hosts[1], 2, 4, 1, 260, good, types.SyncState_SYNCED)))
	floor, _, _ = sm.HeightSyncFloorAsOf(6)
	require.Equal(t, uint64(260), floor, "the next honest host stamp still moves the floor")

	require.NoError(t, apply(6, divStartTx(6, 260, good)))
	require.NoError(t, apply(7, divConfirmTx(t, hosts[2], 6, 260, good)))
	require.Equal(t, types.PhaseActive, sm.SnapshotState().Phase)
}

// TestHeightSyncFloor_ReorgReturnsToTheLiveBranch defines what a reorg does to
// the escrow's logical time.
//
// The floor never falls. That is deliberate: a floor that could move backwards
// would need L0 to accept stamps below it — the tolerance band proposal §14
// rejects — and it is not needed, because a reorg resolves itself. While the
// live branch is below the floor, honest parties carry it: the pair is stale but
// it is the log's own value, and L6 attributes it to whoever established it, not
// to the carriers. Once the live branch passes the floor, own tips exceed F
// again and stamping is first-party once more, on the live branch, without the
// escrow needing a new session.
//
// The other branch is a reorg deeper than W_conf, where carrying stops:
// HeartbeatConfig.FloorOutOfReach has producers omit instead, covered in
// host.TestHost_HeartbeatAck_OmitsAStampWhenTheFloorIsOutOfReach.
func TestHeightSyncFloor_ReorgReturnsToTheLiveBranch(t *testing.T) {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}
	dead, live := []byte{0xde, 0xad}, []byte{0x11, 0xfe}

	// Turn 1 on the branch that is about to be orphaned.
	require.NoError(t, apply(1, divHeartbeatTx(1, 250, dead)))
	acks := make([]*types.DevshardTx, 0, len(hosts))
	for i := range hosts {
		acks = append(acks, divAckTx(t, hosts[i], 1, 1, uint32(i), 250, dead, types.SyncState_SYNCED))
	}
	require.NoError(t, apply(2, acks...))
	floor, hash, _ := sm.HeightSyncFloorAsOf(3)
	require.Equal(t, uint64(250), floor)
	require.Equal(t, dead, hash)

	// The reorg. Every follower in the roster is now at 240 on a different
	// branch, so no party has a first-party height that clears the floor. Each
	// carries F instead, and the escrow keeps applying diffs — the property that
	// matters, since a halt here would need an out-of-band restart.
	require.NoError(t, apply(3, divHeartbeatTx(2, 250, dead)))
	acks = acks[:0]
	for i := range hosts {
		acks = append(acks, divAckTx(t, hosts[i], 2, 3, uint32(i), 250, dead, types.SyncState_CATCHING_UP))
	}
	require.NoError(t, apply(4, acks...), "a roster on a shorter branch must not wedge the escrow")
	require.Empty(t, sm.HeightSyncMarks(),
		"following a reorg is not misbehaviour; the stale pair is settled by L6 against its author")

	// Recovery: the live branch passes the floor, so honest stamps are their own
	// again and the floor moves onto it.
	require.NoError(t, apply(5, divHeartbeatTx(3, 260, live)))
	acks = acks[:0]
	for i := range hosts {
		acks = append(acks, divAckTx(t, hosts[i], 3, 5, uint32(i), 260, live, types.SyncState_SYNCED))
	}
	require.NoError(t, apply(6, acks...))

	floor, hash, known := sm.HeightSyncFloorAsOf(7)
	require.True(t, known)
	require.Equal(t, uint64(260), floor)
	require.Equal(t, live, hash, "the escrow stamps the live branch again, with no new session")
	require.Equal(t, types.PhaseActive, sm.SnapshotState().Phase)
}

// TestHeightSyncFloor_AdmissionRefusalCannotSplitTheFloor: the floor advances
// from applied diffs and from nothing else (proposal §14).
//
// The two are easy to conflate because L5a lives one layer up. A receiver holding
// the envelope of a live exchange may refuse it when the claimed height is
// nowhere near its own tip — but the same diff arriving by catch-up or gossip has
// no envelope to judge, so the refusal cannot be part of any check that decides
// validity. If it fed the floor, the verifier that refused and the verifier that
// ingested would hold different floors and reach different L0 verdicts on every
// later diff, which is an escrow split produced by a check documented as
// replay-identical.
func TestHeightSyncFloor_AdmissionRefusalCannotSplitTheFloor(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	edge := newFloorTestSM(t, hosts, user)   // held the envelope and marked at admission
	replay := newFloorTestSM(t, hosts, user) // saw the same bytes by catch-up

	good := []byte{0x02, 0x50}
	hb := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{divHeartbeatTx(1, 250, good)})
	ackTxs := make([]*types.DevshardTx, 0, len(hosts))
	for i := range hosts {
		ackTxs = append(ackTxs, divAckTx(t, hosts[i], 1, 1, uint32(i), 250, good, types.SyncState_SYNCED))
	}
	ackDiff := testutil.SignDiff(t, user, "escrow-1", 2, ackTxs)

	// The edge verifier's own follower is thousands of blocks away, so L5a fires
	// for it and for nobody else. That is the whole asymmetry between the two.
	edgeMarks := heightsync.CheckEnvelopeBinding(heightsync.LogPlaneInput{
		Nonce: 2,
		Txs:   ackDiff.Txs,
		Sec: &heightsync.HeightSyncSection{
			ProofType:           heightsync.AnchorProofType,
			MainnetHeight:       250,
			MainnetBlockHashHex: "0250",
			Direction:           "response",
		},
		LocalAligned: 50_000,
	}, heightsync.DefaultHeartbeatConfig())
	require.NotEmpty(t, edgeMarks, "fixture must actually exercise an admission-time refusal")

	for _, sm := range []*StateMachine{edge, replay} {
		_, err := sm.ApplyDiff(hb)
		require.NoError(t, err)
		_, err = sm.ApplyDiff(ackDiff)
		require.NoError(t, err)
	}

	for _, m := range []uint64{1, 2, 3, 4} {
		wantH, wantHash, wantKnown := edge.HeightSyncFloorAsOf(m)
		gotH, gotHash, gotKnown := replay.HeightSyncFloorAsOf(m)
		require.Equalf(t, wantH, gotH, "F(%d) must not depend on which path the diff arrived by", m)
		require.Equal(t, wantHash, gotHash)
		require.Equal(t, wantKnown, gotKnown)
	}
	require.Equal(t, edge.HeightSyncMarks(), replay.HeightSyncMarks(),
		"an admission decision is local evidence: it must not enter consensus state")

	// The consequence that matters: identical verdicts from here on, including on
	// the stamp L0 is there to refuse.
	low := testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{
		divHeartbeatTx(2, 5, []byte{0x00, 0x05}),
	})
	_, edgeErr := edge.ApplyDiff(low)
	_, replayErr := replay.ApplyDiff(low)
	require.ErrorIs(t, edgeErr, heightsync.ErrHeightRegression)
	require.ErrorIs(t, replayErr, heightsync.ErrHeightRegression)
	require.Equal(t, edge.LatestNonce(), replay.LatestNonce())
}

// TestHeightSyncFloor_SequencerHeartbeatDoesNotRaise is spec §14 on the
// consensus path: a user-signed heartbeat (and start) above F does not move the
// floor on
// apply or on a second machine that ingested the same bytes with no envelope.
func TestHeightSyncFloor_SequencerHeartbeatDoesNotRaise(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	applySM := newFloorTestSM(t, hosts, user)
	gossipSM := newFloorTestSM(t, hosts, user)
	hash := []byte{0xaa}

	for _, sm := range []*StateMachine{applySM, gossipSM} {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{
			divHeartbeatTx(1, 100, hash),
		}))
		require.NoError(t, err)
		_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
			divAckTx(t, hosts[0], 1, 1, 0, 100, hash, types.SyncState_SYNCED),
		}))
		require.NoError(t, err)
		_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{
			divHeartbeatTx(2, 180, hash),
		}))
		require.NoError(t, err)
		_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 4, []*types.DevshardTx{
			divStartTx(4, 180, hash),
		}))
		require.NoError(t, err)
	}

	for _, sm := range []*StateMachine{applySM, gossipSM} {
		floor, _, known := sm.HeightSyncFloorAsOf(5)
		require.True(t, known)
		require.Equal(t, uint64(100), floor, "heartbeat and start above F must not raise")
	}

	_, err := applySM.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{
		divAckTx(t, hosts[1], 2, 3, 1, 180, hash, types.SyncState_SYNCED),
	}))
	require.NoError(t, err)
	floor, _, _ := applySM.HeightSyncFloorAsOf(6)
	require.Equal(t, uint64(180), floor, "a host ack at that H does raise")
}
