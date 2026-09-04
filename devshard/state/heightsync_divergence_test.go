package state

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// A roster spread far wider than D = 2 and still inside W_conf, which is the
// band where carrying the floor is the honest answer for the party behind. Host
// 3 is 245 blocks back — two orders of magnitude past D, so every check that
// consults D fires, while the producer rule still asks it to lift rather than to
// omit (HeartbeatConfig.FloorOutOfReach draws that line).
var divergedTips = []struct {
	height uint64
	hash   []byte
}{
	{250, []byte{0x02, 0x50}},
	{248, []byte{0x02, 0x48}},
	{150, []byte{0x01, 0x50}},
	{5, []byte{0x00, 0x05}},
}

func divHeartbeatTx(height uint64, hash []byte) *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		ObservedHeight:    height,
		ObservedBlockHash: hash,
		SlotsNum:          uint64(len(divergedTips)),
		Reason:            string(heightsync.ReasonQuietSession),
	}}}
}

func divAckTx(t *testing.T, signer *signing.Secp256k1Signer, turnSeq, refNonce uint64, slot uint32,
	height uint64, hash []byte, st types.SyncState) *types.DevshardTx {
	t.Helper()
	ack := &types.MsgHeightAck{
		RefNonce:          refNonce,
		SlotId:            slot,
		ObservedHeight:    height,
		ObservedBlockHash: append([]byte(nil), hash...),
		SyncState:         st,
		PeerSeen:          []byte{0xff},
	}
	require.NoError(t, heightsync.SignAck(signer, ack))
	return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: ack}}
}

// divStartTx is the sequencer's leg: it carries the best height it knows, which
// for a courier is the roster maximum.
func divStartTx(id, height uint64, hash []byte) *types.DevshardTx {
	return txStart(&types.MsgStartInference{
		InferenceId: id, PromptHash: []byte("prompt"), Model: "llama",
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
		ObservedHeight: height, ObservedBlockHash: hash,
	})
}

// divConfirmTx is the executor's leg, stamped at whatever height the producer
// rule yields for it. The stamp is mirrored into ExecutorReceiptContent so the
// executor signature covers it (§10.5.1), which is what makes the height a host
// attestation rather than something the sequencer wrote.
func divConfirmTx(t *testing.T, executor *signing.Secp256k1Signer, id, height uint64, hash []byte) *types.DevshardTx {
	t.Helper()
	sig := testutil.SignExecutorReceipt(t, executor, "escrow-1", id, []byte("prompt"), "llama", 100, testutil.TestMaxTokens, 1000, 1000,
		testutil.ReceiptStamp{Height: height, Hash: hash})
	return txConfirm(&types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: sig, ConfirmedAt: 1000,
		ObservedHeight: height, ObservedBlockHash: hash,
	})
}

func divFinishTx(t *testing.T, executor *signing.Secp256k1Signer, id uint64, slot uint32, height uint64, hash []byte) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId: id, ResponseHash: []byte("response"),
		InputTokens: 80, OutputTokens: 40, ExecutorSlot: slot,
		EscrowId: "escrow-1", ObservedHeight: height, ObservedBlockHash: hash,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, executor, msg)
	return txFinish(msg)
}

// TestHeightSyncDivergence_InferenceFlowNeverBlocked drives a roster spread far
// beyond D through a full inference flow and asserts the thing height sync must
// never do: stop inferences because hosts disagree about the chain tip.
//
// Divergence this wide is the normal state of a real roster, not an attack, and
// the escrow has to keep serving through it. What makes that possible is that
// every Diff-resident height is a *reference* height with a floor already in the
// log: lifting to F(m) is always available, so no honest party — however far
// behind — is ever forced to author an invalid diff. The earlier design, which
// judged first-party tips against a shared floor, halted the session with HTTP
// 500s the moment any executor lagged.
func TestHeightSyncDivergence_InferenceFlowNeverBlocked(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 1_000_000)
	slots := uint64(len(hosts))
	top := divergedTips[0]

	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}

	// ---- Turn 1: a lagging roster carries logical time and says so -----------
	//
	// Host acks establish 1000 as the turn's reference height (the heartbeat is
	// a sequencer stamp and does not raise F), so every later producer owes at
	// least that. The two hosts hundreds of blocks behind lift to it and
	// label themselves CATCHING_UP: the height is shared logical time, the label
	// is where their real position is reported. Neither is misbehaviour.
	require.NoError(t, apply(1, divHeartbeatTx(top.height, top.hash)))

	honestStates := []types.SyncState{
		types.SyncState_SYNCED,      // own tip 250, exactly h_req
		types.SyncState_SYNCED,      // own tip 248, |Δ| = 2 = D
		types.SyncState_CATCHING_UP, // own tip 150, lifts to 250
		types.SyncState_CATCHING_UP, // own tip 5, lifts to 250
	}
	acks := make([]*types.DevshardTx, 0, len(hosts))
	for i := range divergedTips {
		acks = append(acks, divAckTx(t, hosts[i], 1, 1, uint32(i), top.height, top.hash, honestStates[i]))
	}
	require.NoError(t, apply(2, acks...), "a truthfully lagging roster must not fail the diff")
	require.Empty(t, sm.HeightSyncMarks(),
		"honest divergence carries no blame: a host saying CATCHING_UP is the protocol working")

	rec := sm.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnComplete, rec.State,
		"a turn certifies that Q slots were reachable, not that they agree on a height")
	for i := range divergedTips {
		require.Equal(t, top.height, rec.Acks[uint32(i)].Height,
			"slot %d carries the turn's reference height; its own view is in sync_state and the envelope", i)
	}
	require.Equal(t, types.SyncState_CATCHING_UP, rec.Acks[3].SyncState,
		"the divergence signal survives in the label, which is what the gateway monitors")

	// A stamp *below* the floor is the one thing that stays invalid, because the
	// producer held the log and could have carried it. (An ack of the first
	// heartbeat is judged at F(ref+1)=F(2), which is still empty: sequencer
	// stamps do not raise, so that producing nonce is the wrong place to show
	// this. A start at nonce 3 sees F(3) from the host acks.)
	err := apply(3, divStartTx(3, divergedTips[3].height, divergedTips[3].hash))
	require.ErrorIs(t, err, heightsync.ErrHeightRegression,
		"a raw low tip in Diff is authored misbehaviour, not honest lag")
	require.Equal(t, uint64(2), sm.LatestNonce(), "a rejected diff must not consume the nonce")

	// ---- Inference 3: the most-behind executor still serves ------------------
	//
	// F(3) is 250 from the host acks, so slot 3 — 245 blocks behind — carries the
	// floor rather than its own tip of 5, and the inference completes. This is
	// the liveness property the whole design exists for.
	require.NoError(t, apply(3, divStartTx(3, top.height, top.hash)))
	require.NoError(t, apply(4, divConfirmTx(t, hosts[3], 3, top.height, top.hash)),
		"lifting to F(m) keeps a far-behind executor serving")
	require.NoError(t, apply(5, divFinishTx(t, hosts[3], 3, 3, top.height, top.hash)))

	floor, _, known := sm.HeightSyncFloorAsOf(6)
	require.True(t, known)
	require.Equal(t, top.height, floor)

	// ---- Inference 6: a refused diff must not wedge the session --------------
	lagging := divergedTips[2]
	require.NoError(t, apply(6, divStartTx(6, top.height, top.hash)))

	err = apply(7, divConfirmTx(t, hosts[2], 6, lagging.height, lagging.hash))
	require.ErrorIs(t, err, heightsync.ErrHeightRegression,
		"below F(m) is authored misbehaviour: the producer had the log and could have carried it")
	require.Equal(t, uint64(6), sm.LatestNonce(), "a rejected diff must not consume the nonce")

	require.NoError(t, apply(7, divConfirmTx(t, hosts[2], 6, top.height, top.hash)),
		"carrying F(m) is always available, so the honest retry lands at the same nonce")
	require.NoError(t, apply(8, divFinishTx(t, hosts[2], 6, 2, top.height, top.hash)))

	// ---- Turn 2: the escrow keeps serving, and the labels keep the evidence --
	//
	// Slot 3 lifts to the floor as required but now labels itself SYNCED while
	// being 995 blocks out. Nothing in the log can refute that — divergence is
	// monitoring, and settling a false label needs the LightBlock proof Strong
	// owns — so the diff applies and the claim is retained verbatim.
	require.NoError(t, apply(9, divHeartbeatTx(top.height, top.hash)))
	require.NoError(t, apply(10,
		divAckTx(t, hosts[0], 2, 9, 0, top.height, top.hash, types.SyncState_SYNCED),
		divAckTx(t, hosts[1], 2, 9, 1, top.height, top.hash, types.SyncState_SYNCED),
		divAckTx(t, hosts[2], 2, 9, 2, top.height, top.hash, types.SyncState_CATCHING_UP),
		divAckTx(t, hosts[3], 2, 9, 3, top.height, top.hash, types.SyncState_SYNCED),
	), "a mislabelled ack is monitoring data, never a rejected diff")
	require.Empty(t, sm.HeightSyncMarks(),
		"the log plane has no divergence verdict to reach; L5a at the edge and the "+
			"gateway's collectors are where a false label surfaces")

	// ---- Inference 11: traffic continues -------------------------------------
	require.NoError(t, apply(11, divStartTx(11, top.height, top.hash)))
	require.NoError(t, apply(12, divConfirmTx(t, hosts[3], 11, top.height, top.hash)),
		"the most-behind host still serves by carrying the floor")
	require.NoError(t, apply(13, divFinishTx(t, hosts[3], 11, 3, top.height, top.hash)))

	require.Equal(t, uint64(13), sm.LatestNonce())
	require.Equal(t, types.PhaseActive, sm.SnapshotState().Phase,
		"wide divergence must leave the escrow serving")

	// The second turn opened at nonce 9, which is its identity.
	rec2 := sm.HeightSyncTurnRecord(9)
	require.NotNil(t, rec2)
	require.Len(t, rec2.Acks, int(slots), "every claim is retained for the dispute layer")
	require.Equal(t, types.SyncState_SYNCED, rec2.Acks[3].SyncState,
		"the false label is kept verbatim; a clamped record would destroy the evidence")
}

// TestHeightSyncDivergence_DeadOracleStillCarriesTime is the liveness bonus of
// making acks carry reference heights.
//
// A host whose follower is unreachable has no first-party height to report, but
// it can read F(m) out of the log it already applies. Under the earlier design
// it was forced into ORACLE_UNAVAILABLE with no usable stamp and became a hole
// in the roster's cadence; now it answers, the turn completes, and its
// uselessness as a height *witness* is recorded in the label instead — the
// slot contributes no envelope anchor for dispute / alignment evidence.
func TestHeightSyncDivergence_DeadOracleStillCarriesTime(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	sm, user := newTestSM(t, hosts, 1_000_000)
	top := divergedTips[0]

	apply := func(nonce uint64, txs ...*types.DevshardTx) error {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", nonce, txs))
		return err
	}

	require.NoError(t, apply(1, divHeartbeatTx(top.height, top.hash)))
	require.NoError(t, apply(2,
		divAckTx(t, hosts[0], 1, 1, 0, top.height, top.hash, types.SyncState_SYNCED),
		divAckTx(t, hosts[1], 1, 1, 1, top.height, top.hash, types.SyncState_SYNCED),
		divAckTx(t, hosts[2], 1, 1, 2, top.height, top.hash, types.SyncState_ORACLE_STALE),
		divAckTx(t, hosts[3], 1, 1, 3, top.height, top.hash, types.SyncState_ORACLE_UNAVAILABLE),
	))

	rec := sm.HeightSyncTurnRecord(1)
	require.NotNil(t, rec)
	require.Equal(t, heightsync.TurnComplete, rec.State,
		"a host with no oracle can still echo the floor, so it is no longer a hole in the cadence")
	require.Equal(t, top.height, rec.Acks[3].Height)
	require.Equal(t, types.SyncState_ORACLE_UNAVAILABLE, rec.Acks[3].SyncState,
		"it says plainly that it is not a height witness; the label is the whole signal")
	require.Empty(t, sm.HeightSyncMarks())
}
