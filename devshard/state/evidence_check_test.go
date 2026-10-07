package state

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

// evidenceSM is a two-slot escrow-1 machine. Slot 1 is the executor in every
// case below; resolve decides which warm keys the chain authorizes.
func evidenceSM(t *testing.T, resolve WarmKeyResolver) (*StateMachine, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	var opts []SMOption
	if resolve != nil {
		opts = append(opts, WithWarmKeyResolver(resolve))
	}
	sm, err := NewStateMachine("escrow-1", config, group, 100000, user.Address(), signing.NewSecp256k1Verifier(),
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000), opts...)
	require.NoError(t, err)
	return sm, hosts
}

func evidenceRecord() *types.InferenceRecord {
	return &types.InferenceRecord{
		Status:       types.StatusPending,
		ExecutorSlot: 1,
		PromptHash:   testutil.TestPromptHash[:],
		Model:        "llama",
		InputLength:  100,
		MaxTokens:    testutil.TestMaxTokens,
		StartedAt:    1000,
	}
}

func evidenceConfirm(t *testing.T, signer signing.Signer, escrowID string, rec *types.InferenceRecord) *types.MsgConfirmStart {
	t.Helper()
	stamp := testutil.ReceiptStamp{Height: 42, Hash: []byte("block-42")}
	return &types.MsgConfirmStart{
		InferenceId:       1,
		ExecutorSig:       testutil.SignExecutorReceipt(t, signer, escrowID, 1, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, 2000, stamp),
		ConfirmedAt:       2000,
		ObservedHeight:    stamp.Height,
		ObservedBlockHash: stamp.Hash,
	}
}

func evidenceFinish(t *testing.T, signer signing.Signer, escrowID string, slot uint32) *types.MsgFinishInference {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  1,
		EscrowId:     escrowID,
		ExecutorSlot: slot,
		ResponseHash: testutil.TestResponseHash,
		ServedHash:   testutil.TestServedHash,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return msg
}

func TestCheckExecutorReceipt(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	executor := hosts[1]
	rec := evidenceRecord()

	require.NoError(t, sm.CheckExecutorReceipt(rec, evidenceConfirm(t, executor, "escrow-1", rec)))

	for name, tc := range map[string]struct {
		confirm *types.MsgConfirmStart
		rec     *types.InferenceRecord
	}{
		"other escrow":     {confirm: evidenceConfirm(t, executor, "escrow-2", rec), rec: rec},
		"non-executor key": {confirm: evidenceConfirm(t, hosts[0], "escrow-1", rec), rec: rec},
		"unsigned":         {confirm: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("forged"), ConfirmedAt: 2000}, rec: rec},
		"other inference id": {confirm: func() *types.MsgConfirmStart {
			c := evidenceConfirm(t, executor, "escrow-1", rec)
			c.InferenceId = 2
			return c
		}(), rec: rec},
		"moved confirmed_at": {confirm: func() *types.MsgConfirmStart {
			c := evidenceConfirm(t, executor, "escrow-1", rec)
			c.ConfirmedAt++
			return c
		}(), rec: rec},
		"moved stamp": {confirm: func() *types.MsgConfirmStart {
			c := evidenceConfirm(t, executor, "escrow-1", rec)
			c.ObservedHeight++
			return c
		}(), rec: rec},
		"other prompt": {confirm: evidenceConfirm(t, executor, "escrow-1", rec), rec: func() *types.InferenceRecord {
			r := evidenceRecord()
			r.PromptHash = []byte("other-prompt-hash-000000000000000")
			return r
		}()},
		"nil confirm": {rec: rec},
		"nil record":  {confirm: evidenceConfirm(t, executor, "escrow-1", rec)},
	} {
		t.Run(name, func(t *testing.T) {
			err := sm.CheckExecutorReceipt(tc.rec, tc.confirm)
			require.Error(t, err)
			require.True(t, errors.Is(err, types.ErrInvalidExecutorSig), "got %v", err)
		})
	}

	t.Run("unknown slot", func(t *testing.T) {
		r := evidenceRecord()
		r.ExecutorSlot = 7
		require.ErrorIs(t, sm.CheckExecutorReceipt(r, evidenceConfirm(t, executor, "escrow-1", r)), types.ErrSlotNotInGroup)
	})
}

func TestCheckFinishProposerSig(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	executor := hosts[1]

	require.NoError(t, sm.CheckFinishProposerSig(evidenceFinish(t, executor, "escrow-1", 1)))

	stub := &types.MsgFinishInference{InferenceId: 1, EscrowId: "escrow-1", ExecutorSlot: 1, ProposerSig: []byte("forged")}
	require.Error(t, sm.CheckFinishProposerSig(stub))
	require.ErrorIs(t, sm.CheckFinishProposerSig(evidenceFinish(t, hosts[0], "escrow-1", 1)), types.ErrInvalidProposerSig)
	require.ErrorIs(t, sm.CheckFinishProposerSig(evidenceFinish(t, executor, "escrow-1", 7)), types.ErrSlotNotInGroup)

	tampered := evidenceFinish(t, executor, "escrow-1", 1)
	tampered.OutputTokens++
	require.ErrorIs(t, sm.CheckFinishProposerSig(tampered), types.ErrInvalidProposerSig)
}

// Challenge evidence is never sequenced, so accepting a warm signature must not
// write WarmKeys: that map is part of the state root.
func TestCheckEvidence_WarmKeyAcceptedWithoutBinding(t *testing.T) {
	warm := testutil.MustGenerateKey(t)
	var resolverCalls int
	var executorAddr string
	sm, hosts := evidenceSM(t, func(warmAddr, coldAddr string) (bool, error) {
		resolverCalls++
		return warmAddr == warm.Address() && coldAddr == executorAddr, nil
	})
	executorAddr = hosts[1].Address()
	rec := evidenceRecord()
	rootBefore, err := sm.ComputeStateRoot()
	require.NoError(t, err)

	require.NoError(t, sm.CheckExecutorReceipt(rec, evidenceConfirm(t, warm, "escrow-1", rec)))
	require.NoError(t, sm.CheckFinishProposerSig(evidenceFinish(t, warm, "escrow-1", 1)))
	require.Equal(t, 2, resolverCalls)
	require.Empty(t, sm.WarmKeys())
	rootAfter, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter)

	other := testutil.MustGenerateKey(t)
	require.ErrorIs(t, sm.CheckExecutorReceipt(rec, evidenceConfirm(t, other, "escrow-1", rec)), types.ErrInvalidExecutorSig)
	require.ErrorIs(t, sm.CheckFinishProposerSig(evidenceFinish(t, other, "escrow-1", 1)), types.ErrInvalidProposerSig)
}

func TestCheckEvidence_BoundWarmKeyWins(t *testing.T) {
	bound := testutil.MustGenerateKey(t)
	rotated := testutil.MustGenerateKey(t)
	var resolverCalls int
	sm, _ := evidenceSM(t, func(warmAddr, _ string) (bool, error) {
		resolverCalls++
		return warmAddr == bound.Address() || warmAddr == rotated.Address(), nil
	})
	require.NoError(t, sm.VerifyFinishProposerSig(evidenceFinish(t, bound, "escrow-1", 1)), "sequenced finish binds the warm key")
	require.Equal(t, map[uint32]string{1: bound.Address()}, sm.WarmKeys())
	resolverCalls = 0
	rec := evidenceRecord()

	require.NoError(t, sm.CheckExecutorReceipt(rec, evidenceConfirm(t, bound, "escrow-1", rec)))
	require.NoError(t, sm.CheckFinishProposerSig(evidenceFinish(t, bound, "escrow-1", 1)))
	require.ErrorIs(t, sm.CheckFinishProposerSig(evidenceFinish(t, rotated, "escrow-1", 1)), types.ErrInvalidProposerSig,
		"a bound slot accepts only its bound warm key, as applyFinish does")
	require.Zero(t, resolverCalls, "a bound slot never asks the resolver")
}
