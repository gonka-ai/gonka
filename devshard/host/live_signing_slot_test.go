package host

import (
	"common/chainoracle/blocks"
	"context"
	mockoracle "devshard/chainoracle/blocks/observer"
	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

func TestLiveSigningSlotTracksPartialRevoke(t *testing.T) {
	validator := testutil.MustGenerateKey(t)
	addr, err := blocks.AddressBytes(validator.CompressedPublicKeyBytes())
	require.NoError(t, err)
	source, err := mockoracle.NewMock(mockoracle.MockConfig{LightBlocks: true, ChainID: "live-slots", Validators: []mockoracle.MockValidator{{Signer: validator, Address: addr, Power: 10}}, InitialHeight: 100, Start: time.Now().Add(-10 * time.Second), BlockInterval: time.Second})
	require.NoError(t, err)
	anchor, err := source.AdvanceOne()
	require.NoError(t, err)
	cfg := testutil.DefaultConfig(3)
	cfg.MainnetAnchorHeight, cfg.MainnetAnchorHash = anchor.Height, anchor.BlockHash
	cold, peer, warm, user := testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)
	group := testutil.MakeGroup([]*signing.Secp256k1Signer{cold, cold, peer})
	shared := testutil.MustMemoryStore(t, "live-slots", user.Address(), cfg, group, 1_000_000)
	sm, err := state.NewStateMachine("live-slots", cfg, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(), shared, state.WithVersion("v7"), state.WithMainnetHeights(source), state.WithWarmKeyHeightChecker(func(c, w string, id uint64, _ int64, _ time.Time) (bool, error) {
		return c == cold.Address() && w == warm.Address() && (id == 1 || id == 2), nil
	}))
	require.NoError(t, err)
	apply := func(extra ...*types.DevshardTx) types.Diff {
		height, err := sm.BuildMainnetHeightTx(context.Background())
		require.NoError(t, err)
		nonce := sm.LatestNonce() + 1
		txs := append([]*types.DevshardTx{height}, extra...)
		root, err := sm.ApplyLocal(nonce, txs)
		require.NoError(t, err)
		diff := testutil.SignDiffWithRoot(t, user, "live-slots", nonce, txs, root)
		require.NoError(t, shared.AppendDiff("live-slots", types.DiffRecord{Diff: diff, StateHash: root}))
		return diff
	}
	apply(testutil.WarmKeyBindTx(0, warm.Address(), 1), testutil.WarmKeyBindTx(1, warm.Address(), 1))
	h, err := NewHost(sm, warm, stub.NewInferenceEngine(), "live-slots", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()), WithChainOracle(source))
	require.NoError(t, err)
	t.Cleanup(h.Close)

	queued := &types.MsgValidation{InferenceId: 99, ValidatorSlot: 0, Valid: true, EscrowId: "live-slots"}
	queued.ProposerSig, err = h.signProposer(queued)
	require.NoError(t, err)
	h.HostMempool().Add(MempoolEntry{Tx: &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: queued}}, ProposedAt: 1})
	apply(testutil.SignedWarmKeyRevokeTx(t, 0, warm, 1, "live-slots"))
	h.mu.Lock()
	blocked := h.hasMempoolValidationOrVote(99)
	queuedIDs := h.ownMempoolValidationIDs()
	h.mu.Unlock()
	require.False(t, blocked, "revoked queued signature must not block replacement through a live role")
	require.NotContains(t, queuedIDs, uint64(99))

	// Model a replica restarted while only slot 1 was bound to its warm key.
	h, err = NewHost(sm, warm, stub.NewInferenceEngine(), "live-slots", group, nil, WithStorage(shared), WithVerifier(signing.NewSecp256k1Verifier()), WithChainOracle(source))
	require.NoError(t, err)
	t.Cleanup(h.Close)
	require.Equal(t, map[uint32]bool{1: true}, h.slotIDs)

	slot, err := h.SigningSlot()
	require.NoError(t, err)
	require.Equal(t, uint32(1), slot)
	require.Equal(t, uint32(1), h.PrimarySlot(), "scheduling identity remains stable")
	require.False(t, h.CanSignSlot(0))
	require.True(t, h.CanSignSlot(1))
	slot, err = h.StateSigningSlot(sm.LatestNonce())
	require.NoError(t, err)
	require.Equal(t, uint32(1), slot)
	_, err = h.signProposer(&types.MsgValidation{InferenceId: 99, ValidatorSlot: 0, EscrowId: "live-slots"})
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed)
	_, err = h.signProposer(&types.MsgValidation{InferenceId: 99, ValidatorSlot: 1, EscrowId: "live-slots"})
	require.NoError(t, err)
	var diffs []types.Diff
	cp := sm.MainnetCheckpoint()
	for nonce := uint64(1); nonce <= 3; nonce++ {
		if heightsync.SlotForNonce(nonce, 3) < 2 {
			diffs = append(diffs, types.Diff{Nonce: nonce, Txs: []*types.DevshardTx{{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{ObservedHeight: uint64(cp.Height), ObservedBlockHash: cp.Hash}}}}})
		}
	}
	h.mu.Lock()
	h.maybeAckHeartbeatsLocked(diffs, &blocks.Header{Height: cp.Height, BlockHash: cp.Hash, Time: cp.Time}, nil)
	h.mu.Unlock()
	acks := 0
	for _, tx := range h.MempoolTxs() {
		if ack := tx.GetHeightAck(); ack != nil {
			acks++
			require.Equal(t, uint32(1), ack.SlotId, "fixed-slot acks cannot use the revoked role")
		}
	}
	require.Equal(t, 1, acks)
	apply(testutil.WarmKeyBindTx(0, warm.Address(), 2), testutil.StartTx(3))
	slot, err = h.SigningSlot()
	require.NoError(t, err)
	require.Zero(t, slot, "a new grant can explicitly authorize the slot again")
	records, err := shared.GetDiffs("live-slots", 3, 3)
	require.NoError(t, err)
	require.Len(t, records, 1)
	payload := &InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}
	response, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{records[0].Diff}, Nonce: 3, Payload: payload})
	require.NoError(t, err)
	require.NotEmpty(t, response.Receipt, "newly rebound executor slot must issue its receipt without another restart")
	require.NotNil(t, response.ExecutionJob)
	h.ReleaseExecution(3)
	receipt, _, err := h.ChallengeReceipt(context.Background(), 3, payload, []types.Diff{records[0].Diff})
	require.NoError(t, err)
	require.NotEmpty(t, receipt, "challenge ingress must accept the rebound executor slot too")
	require.Eventually(t, func() bool { return h.HostMempool().HasFinish(3) }, time.Second, time.Millisecond)
	apply()
	apply()
	heartbeat := apply(&types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum:          3,
		Reason:            string(heightsync.ReasonQuietSession),
		ObservedHeight:    uint64(cp.Height),
		ObservedBlockHash: cp.Hash,
	}}})
	require.True(t, sm.HeightSyncAckOutstanding(heartbeat.Nonce, 0))
	h.mu.Lock()
	h.maybeAckHeartbeatsLocked([]types.Diff{heartbeat}, &blocks.Header{Height: cp.Height, BlockHash: cp.Hash, Time: cp.Time}, nil)
	h.mu.Unlock()
	var reboundAck *types.DevshardTx
	txs := h.MempoolTxs()
	for _, tx := range txs {
		if ack := tx.GetHeightAck(); ack != nil && ack.SlotId == 0 && ack.RefNonce == heartbeat.Nonce {
			reboundAck = tx
		}
	}
	require.NotNil(t, reboundAck, "rebound slot must also resume HeightAck production")
	require.Contains(t, h.OutstandingLocalHeightAcks(txs), reboundAck, "an outstanding ACK from a rebound slot must remain deliverable")

	apply(testutil.SignedWarmKeyRevokeTx(t, 0, warm, 2, "live-slots"), testutil.SignedWarmKeyRevokeTx(t, 1, warm, 1, "live-slots"))
	_, err = h.SigningSlot()
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed)
	_, err = h.StateSigningSlot(sm.LatestNonce())
	require.ErrorIs(t, err, types.ErrWarmKeyUnconfirmed)
}
