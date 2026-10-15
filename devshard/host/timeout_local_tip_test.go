package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

func TestTimeoutVoteNeedsTheLocalTipNotJustTheFloor(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	config.ExecutionTimeout = 1
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 1_000_000))
	require.NoError(t, err)
	oracle := &fakeOracle{}
	oracle.setHeight(100)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, WithChainOracle(oracle))
	require.NoError(t, err)

	hash := []byte{0xaa}
	heartbeat := &types.DevshardTx{Tx: &types.DevshardTx_Heartbeat{Heartbeat: &types.MsgHeartbeat{
		SlotsNum: uint64(len(hosts)), Reason: string(heightsync.ReasonQuietSession),
	}}}
	ack := func(ref, height uint64) *types.DevshardTx {
		msg := &types.MsgHeightAck{RefNonce: ref, SlotId: 2, ObservedHeight: height, ObservedBlockHash: hash,
			SyncState: types.SyncState_SYNCED, PeerSeen: []byte{0xff}}
		require.NoError(t, heightsync.SignAck(hosts[2], msg))
		return &types.DevshardTx{Tx: &types.DevshardTx_HeightAck{HeightAck: msg}}
	}
	const id uint64 = 4
	start := testutil.StartTx(id)
	start.GetStartInference().ObservedHeight = 100
	start.GetStartInference().ObservedBlockHash = hash
	receipt := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", id, testutil.TestPromptHash[:], "llama", 100, testutil.TestMaxTokens,
		testutil.ReceiptStamp{Height: 100, Hash: hash})
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: id, ExecutorSig: receipt, ObservedHeight: 100, ObservedBlockHash: hash,
	}}}
	for n, txs := range [][]*types.DevshardTx{{heartbeat}, {ack(1, 100)}, {heartbeat}, {start}, {confirm}, {heartbeat}, {ack(6, 1_000_000)}} {
		_, err := sm.ApplyDiff(testutil.SignDiff(t, user, "escrow-1", uint64(n+1), txs))
		require.NoError(t, err)
	}
	rec, ok := sm.GetInference(id)
	require.True(t, ok)
	require.Equal(t, types.StatusStarted, rec.Status)
	require.Equal(t, uint64(100), rec.ConfirmedAtHeight)

	require.NoError(t, sm.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), "one fabricated ack moves the floor")
	require.ErrorIs(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION), types.ErrTimeoutNotDue,
		"a verifier whose own chain is still at the anchor must not vote")

	oracle.setHeight(101)
	require.NoError(t, h.TimeoutDue(id, types.TimeoutReason_TIMEOUT_REASON_EXECUTION))
}
