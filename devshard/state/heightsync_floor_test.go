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

// TestHeightSyncFloor_HostClaimSetsLogicalTimeAtAnyDistance is the consensus path
// for the raise rule after W_conf and Q were withdrawn (proposal §14).
//
// A host-signed claim above the floor becomes the escrow's logical time, however
// far above it sits, and no mark is recorded for the distance alone. Bounding it
// was an attempt to make the floor the defence against a fabricated height, and
// the floor cannot be that: it only ever sees heights that already reached the
// log through an admitted envelope, which is where |Δ| > D demands proof of the
// party that claimed it (§8/§15, L5a today). What the bound did accomplish was
// pinning the escrow's clock wherever the first stamp landed — the poison case
// below.
func TestHeightSyncFloor_HostClaimSetsLogicalTimeAtAnyDistance(t *testing.T) {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}
	good, far := []byte{0x02, 0x50}, []byte{0xff, 0xff}

	require.NoError(t, apply(1, divHeartbeatTx(0, nil)))
	require.NoError(t, apply(2,
		divAckTx(t, hosts[1], 1, 1, 1, 250, good, types.SyncState_SYNCED),
	), "the first host ack seeds F")
	require.NoError(t, apply(3,
		divAckTx(t, hosts[0], 1, 1, 0, math.MaxUint64/2, far, types.SyncState_SYNCED),
	))

	floor, hash, known := sm.HeightSyncFloorAsOf(4)
	require.True(t, known)
	require.Equal(t, uint64(math.MaxUint64/2), floor)
	require.Equal(t, far, hash)
	require.Empty(t, sm.HeightSyncMarks(),
		"distance is not evidence on this plane; the envelope that carried it is where it is judged")

	require.Equal(t, types.PhaseActive, sm.SnapshotState().Phase)
}

// TestHeightSyncFloor_PoisonedLowFloorIsRepairedByAnHonestHost is the case that
// retired the raise bound.
//
// One participant stamps H=1 into a fresh escrow — a real height with an honest
// hash, just ancient. F becomes 1. Under the old rule every honest host at the
// live tip was then more than W_conf above the floor and could not raise it, and
// nothing lowers a floor, so a single message pinned the escrow's logical time at
// 1 for the rest of the session: every later stamp had to be a lie or an omission,
// and no party could repair it. Now the next honest host stamp simply moves it.
func TestHeightSyncFloor_PoisonedLowFloorIsRepairedByAnHonestHost(t *testing.T) {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}
	genesis, live := []byte{0x00, 0x01}, []byte{0x27, 0x10}

	require.NoError(t, apply(1, divHeartbeatTx(0, nil)))
	require.NoError(t, apply(2, divAckTx(t, hosts[0], 1, 1, 0, 1, genesis, types.SyncState_SYNCED)))
	floor, _, _ := sm.HeightSyncFloorAsOf(3)
	require.Equal(t, uint64(1), floor)

	require.NoError(t, apply(3, divHeartbeatTx(1, genesis)), "the user carries F while it is wrong")
	require.NoError(t, apply(4, divAckTx(t, hosts[1], 2, 3, 1, 10_000, live, types.SyncState_SYNCED)))

	floor, hash, _ := sm.HeightSyncFloorAsOf(5)
	require.Equal(t, uint64(10_000), floor, "one honest host repairs the escrow's clock, alone")
	require.Equal(t, live, hash)
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
// Depth does not change any of this. A reorg past what used to be W_conf is the
// same story with a bigger number: carrying is still the honest answer, still
// attributable to the floor's author, and never an omission —
// host.TestHost_HeartbeatAck_CarriesAFloorFarAboveOwnTip.
func TestHeightSyncFloor_ReorgReturnsToTheLiveBranch(t *testing.T) {
	hosts := fourHosts(t)
	sm, user := newTestSM(t, hosts, 1_000_000)
	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}
	dead, live := []byte{0xde, 0xad}, []byte{0x11, 0xfe}

	// Turn 1 on the branch that is about to be orphaned.
	require.NoError(t, apply(1, divHeartbeatTx(250, dead)))
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
	require.NoError(t, apply(3, divHeartbeatTx(250, dead)))
	acks = acks[:0]
	for i := range hosts {
		acks = append(acks, divAckTx(t, hosts[i], 2, 3, uint32(i), 250, dead, types.SyncState_CATCHING_UP))
	}
	require.NoError(t, apply(4, acks...), "a roster on a shorter branch must not wedge the escrow")
	require.Empty(t, sm.HeightSyncMarks(),
		"following a reorg is not misbehaviour; the stale pair is settled by L6 against its author")

	// Recovery: the live branch passes the floor, so honest host stamps are their
	// own again and the floor moves onto it. The user still carries F — it is
	// never a height source (§10.3.1) — so the turn opens at the stale pair and
	// the acks answering it move the escrow onto the live branch.
	require.NoError(t, apply(5, divHeartbeatTx(250, dead)))
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
	hb := testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{divHeartbeatTx(250, good)})
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
		divHeartbeatTx(5, []byte{0x00, 0x05}),
	})
	_, edgeErr := edge.ApplyDiff(low)
	_, replayErr := replay.ApplyDiff(low)
	require.ErrorIs(t, edgeErr, heightsync.ErrHeightRegression)
	require.ErrorIs(t, replayErr, heightsync.ErrHeightRegression)
	require.Equal(t, edge.LatestNonce(), replay.LatestNonce())
}

// TestHeightSyncFloor_SequencerHeartbeatDoesNotRaise is spec §14 on the
// consensus path: a user-signed heartbeat (and start) above F does not move the
// floor on apply or on a second machine that ingested the same bytes with no
// envelope — and, since §10.3.1, is marked height_unbacked for having claimed a
// height at all. Both machines record it: the check reads F from the log, so it
// does not matter which path the diff arrived by.
func TestHeightSyncFloor_SequencerHeartbeatDoesNotRaise(t *testing.T) {
	hosts := fourHosts(t)
	user := testutil.MustGenerateKey(t)
	applySM := newFloorTestSM(t, hosts, user)
	gossipSM := newFloorTestSM(t, hosts, user)
	hash := []byte{0xaa}

	for _, sm := range []*StateMachine{applySM, gossipSM} {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 1, []*types.DevshardTx{
			divHeartbeatTx(100, hash),
		}))
		require.NoError(t, err)
		_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 2, []*types.DevshardTx{
			divAckTx(t, hosts[0], 1, 1, 0, 100, hash, types.SyncState_SYNCED),
		}))
		require.NoError(t, err)
		_, err = sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 3, []*types.DevshardTx{
			divHeartbeatTx(180, hash),
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

		marks := sm.HeightSyncMarks()
		require.Len(t, marks, 2, "the heartbeat and the start each invented a height")
		for _, m := range marks {
			require.Equal(t, heightsync.MarkHeightUnbacked, m.Kind)
		}
		require.Equal(t, uint64(3), marks[0].Nonce)
		require.Equal(t, uint64(4), marks[1].Nonce)
	}

	_, err := applySM.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", 5, []*types.DevshardTx{
		divAckTx(t, hosts[1], 2, 3, 1, 180, hash, types.SyncState_SYNCED),
	}))
	require.NoError(t, err)
	floor, _, _ := applySM.HeightSyncFloorAsOf(6)
	require.Equal(t, uint64(180), floor, "a host ack at that H does raise")
}
