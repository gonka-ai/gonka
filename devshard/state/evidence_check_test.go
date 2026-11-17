package state

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/heightsync"
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

func evidenceRecord(status types.InferenceStatus) *types.InferenceRecord {
	return &types.InferenceRecord{
		Status:       status,
		ExecutorSlot: 1,
		PromptHash:   testutil.TestPromptHash[:],
		Model:        "llama",
		InputLength:  100,
		MaxTokens:    testutil.TestMaxTokens,
		ReservedCost: 1000,
		StartedAt:    1000,
	}
}

func evidenceConfirmFor(t *testing.T, signer signing.Signer, escrowID string, id uint64, rec *types.InferenceRecord, stamp testutil.ReceiptStamp) *types.DevshardTx {
	t.Helper()
	return txConfirm(&types.MsgConfirmStart{
		InferenceId:       id,
		ExecutorSig:       testutil.SignExecutorReceipt(t, signer, escrowID, id, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, 2000, stamp),
		ConfirmedAt:       2000,
		ObservedHeight:    stamp.Height,
		ObservedBlockHash: stamp.Hash,
	})
}

func evidenceConfirm(t *testing.T, signer signing.Signer, escrowID string, rec *types.InferenceRecord) *types.DevshardTx {
	return evidenceConfirmFor(t, signer, escrowID, 1, rec, testutil.ReceiptStamp{Height: 42, Hash: []byte("block-42")})
}

func signedEvidenceFinish(t *testing.T, signer signing.Signer, msg *types.MsgFinishInference) *types.DevshardTx {
	t.Helper()
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return txFinish(msg)
}

func evidenceFinishMsg(escrowID string, slot uint32) *types.MsgFinishInference {
	return &types.MsgFinishInference{
		InferenceId:  1,
		EscrowId:     escrowID,
		ExecutorSlot: slot,
		ResponseHash: testutil.TestResponseHash,
		ServedHash:   testutil.TestServedHash,
	}
}

func evidenceFinish(t *testing.T, signer signing.Signer, escrowID string, slot uint32) *types.DevshardTx {
	return signedEvidenceFinish(t, signer, evidenceFinishMsg(escrowID, slot))
}

func TestCheckEvidence_Confirm(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	executor := hosts[1]
	rec := evidenceRecord(types.StatusPending)

	require.NoError(t, sm.CheckEvidence(rec, evidenceConfirm(t, executor, "escrow-1", rec)))

	moved := func(edit func(*types.MsgConfirmStart)) *types.DevshardTx {
		tx := evidenceConfirm(t, executor, "escrow-1", rec)
		edit(tx.GetConfirmStart())
		return tx
	}
	for name, tc := range map[string]struct {
		confirm *types.DevshardTx
		rec     *types.InferenceRecord
	}{
		"other escrow":       {confirm: evidenceConfirm(t, executor, "escrow-2", rec), rec: rec},
		"non-executor key":   {confirm: evidenceConfirm(t, hosts[0], "escrow-1", rec), rec: rec},
		"unsigned":           {confirm: txConfirm(&types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("forged"), ConfirmedAt: 2000}), rec: rec},
		"other inference id": {confirm: moved(func(c *types.MsgConfirmStart) { c.InferenceId = 2 }), rec: rec},
		"moved confirmed_at": {confirm: moved(func(c *types.MsgConfirmStart) { c.ConfirmedAt++ }), rec: rec},
		"moved stamp":        {confirm: moved(func(c *types.MsgConfirmStart) { c.ObservedHeight++ }), rec: rec},
		"other prompt": {confirm: evidenceConfirm(t, executor, "escrow-1", rec), rec: func() *types.InferenceRecord {
			r := evidenceRecord(types.StatusPending)
			r.PromptHash = []byte("other-prompt-hash-000000000000000")
			return r
		}()},
	} {
		t.Run(name, func(t *testing.T) {
			err := sm.CheckEvidence(tc.rec, tc.confirm)
			require.True(t, errors.Is(err, types.ErrInvalidExecutorSig), "got %v", err)
		})
	}

	t.Run("nil record", func(t *testing.T) {
		require.ErrorIs(t, sm.CheckEvidence(nil, evidenceConfirm(t, executor, "escrow-1", rec)), types.ErrInvalidTransition)
	})
	t.Run("no tx", func(t *testing.T) {
		require.ErrorIs(t, sm.CheckEvidence(rec), types.ErrInvalidTransition)
	})
	t.Run("record already started", func(t *testing.T) {
		require.ErrorIs(t, sm.CheckEvidence(evidenceRecord(types.StatusStarted), evidenceConfirm(t, executor, "escrow-1", rec)), types.ErrInvalidTransition)
	})
	t.Run("unknown slot", func(t *testing.T) {
		r := evidenceRecord(types.StatusPending)
		r.ExecutorSlot = 7
		require.ErrorIs(t, sm.CheckEvidence(r, evidenceConfirm(t, executor, "escrow-1", r)), types.ErrSlotNotInGroup)
	})
}

// Each case is a finish applyFinishInference rejects, so none may stand as
// evidence against a timeout.
func TestCheckEvidence_Finish(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	executor := hosts[1]
	rec := evidenceRecord(types.StatusStarted)

	require.NoError(t, sm.CheckEvidence(rec, evidenceFinish(t, executor, "escrow-1", 1)))

	edited := func(edit func(*types.MsgFinishInference)) *types.DevshardTx {
		msg := evidenceFinishMsg("escrow-1", 1)
		edit(msg)
		return signedEvidenceFinish(t, executor, msg)
	}
	tampered := evidenceFinish(t, executor, "escrow-1", 1)
	tampered.GetFinishInference().OutputTokens++

	for name, tc := range map[string]struct {
		finish *types.DevshardTx
		want   error
	}{
		"stub": {finish: txFinish(&types.MsgFinishInference{InferenceId: 1, EscrowId: "escrow-1", ExecutorSlot: 1, ProposerSig: []byte("forged")}),
			want: types.ErrInvalidFinishHash},
		"non-executor key": {finish: evidenceFinish(t, hosts[0], "escrow-1", 1), want: types.ErrInvalidProposerSig},
		"other slot":       {finish: evidenceFinish(t, executor, "escrow-1", 0), want: types.ErrWrongExecutorSlot},
		"tampered":         {finish: tampered, want: types.ErrInvalidProposerSig},
		"short hash":       {finish: edited(func(m *types.MsgFinishInference) { m.ResponseHash = []byte("short") }), want: types.ErrInvalidFinishHash},
		"other escrow":     {finish: evidenceFinish(t, executor, "escrow-2", 1), want: types.ErrEscrowIDMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			require.ErrorIs(t, sm.CheckEvidence(rec, tc.finish), tc.want)
		})
	}

	t.Run("overflowing token cost", func(t *testing.T) {
		require.Error(t, sm.CheckEvidence(rec, edited(func(m *types.MsgFinishInference) {
			m.InputTokens, m.OutputTokens = math.MaxUint64, 1
		})))
	})
	t.Run("record still pending", func(t *testing.T) {
		require.ErrorIs(t, sm.CheckEvidence(evidenceRecord(types.StatusPending), evidenceFinish(t, executor, "escrow-1", 1)), types.ErrInvalidTransition,
			"a finish cannot apply before its ConfirmStart")
	})
}

func TestCheckEvidence_ConfirmThenFinish(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	executor := hosts[1]
	rec := evidenceRecord(types.StatusPending)
	confirm := evidenceConfirm(t, executor, "escrow-1", rec)

	require.NoError(t, sm.CheckEvidence(rec, confirm, evidenceFinish(t, executor, "escrow-1", 1)))
	require.Equal(t, types.StatusPending, rec.Status, "the record is not modified")

	other := evidenceFinishMsg("escrow-1", 1)
	other.InferenceId = 3
	require.ErrorIs(t, sm.CheckEvidence(rec, confirm, signedEvidenceFinish(t, executor, other)), types.ErrInvalidTransition)

	t.Run("finish stamped below its confirm", func(t *testing.T) {
		low := evidenceFinishMsg("escrow-1", 1)
		low.ObservedHeight, low.ObservedBlockHash = 10, []byte("block-10")
		require.ErrorIs(t, sm.CheckEvidence(rec, confirm, signedEvidenceFinish(t, executor, low)), heightsync.ErrHeightRegression,
			"compose drops a finish stamped below the confirm in the same diff")
	})
}

// A stamp below the height floor at the inference id is signed and well
// formed, but compose drops it. It must not count as evidence.
func TestCheckEvidence_StampBelowHeightFloor(t *testing.T) {
	sm, hosts := evidenceSM(t, nil)
	_, _, err := sm.ApplyLocalBestEffort(1, []*types.DevshardTx{testutil.StartTx(1)})
	require.NoError(t, err)
	first, ok := sm.Inference(1)
	require.True(t, ok)
	seed := evidenceConfirmFor(t, hosts[first.ExecutorSlot], "escrow-1", 1, first, testutil.ReceiptStamp{Height: 80, Hash: []byte("block-80")})
	_, applied, err := sm.ApplyLocalBestEffort(2, []*types.DevshardTx{seed})
	require.NoError(t, err)
	require.Len(t, applied, 1, "a host-signed stamp at 80 raises the floor")
	_, _, err = sm.ApplyLocalBestEffort(3, []*types.DevshardTx{testutil.StartTx(3)})
	require.NoError(t, err)

	rec, ok := sm.Inference(3)
	require.True(t, ok)
	executor := hosts[rec.ExecutorSlot]

	low := evidenceConfirmFor(t, executor, "escrow-1", 3, rec, testutil.ReceiptStamp{Height: 50, Hash: []byte("block-50")})
	require.ErrorIs(t, sm.CheckEvidence(rec, low), heightsync.ErrHeightRegression)
	_, applied, err = sm.ApplyLocalBestEffort(4, []*types.DevshardTx{low, testutil.StartTx(4)})
	require.NoError(t, err)
	require.Len(t, applied, 1, "compose drops the same confirm")

	atFloor := evidenceConfirmFor(t, executor, "escrow-1", 3, rec, testutil.ReceiptStamp{Height: 80, Hash: []byte("block-80")})
	require.NoError(t, sm.CheckEvidence(rec, atFloor))
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
	pending := evidenceRecord(types.StatusPending)
	started := evidenceRecord(types.StatusStarted)
	rootBefore, err := sm.ComputeStateRoot()
	require.NoError(t, err)

	require.NoError(t, sm.CheckEvidence(pending, evidenceConfirm(t, warm, "escrow-1", pending)))
	require.NoError(t, sm.CheckEvidence(started, evidenceFinish(t, warm, "escrow-1", 1)))
	require.Equal(t, 2, resolverCalls)
	require.Empty(t, sm.WarmKeys())
	rootAfter, err := sm.ComputeStateRoot()
	require.NoError(t, err)
	require.Equal(t, rootBefore, rootAfter)

	other := testutil.MustGenerateKey(t)
	require.ErrorIs(t, sm.CheckEvidence(pending, evidenceConfirm(t, other, "escrow-1", pending)), types.ErrInvalidExecutorSig)
	require.ErrorIs(t, sm.CheckEvidence(started, evidenceFinish(t, other, "escrow-1", 1)), types.ErrInvalidProposerSig)

	resolverCalls = 0
	require.ErrorIs(t, sm.CheckEvidenceCached(started, evidenceFinish(t, warm, "escrow-1", 1)), types.ErrInvalidProposerSig,
		"an unbound warm key is undecidable from state, and the cached check counts that as a failure")
	require.NoError(t, sm.CheckEvidenceCached(started, evidenceFinish(t, hosts[1], "escrow-1", 1)))
	require.Zero(t, resolverCalls)
}

func TestCheckEvidence_BoundWarmKeyWins(t *testing.T) {
	bound := testutil.MustGenerateKey(t)
	rotated := testutil.MustGenerateKey(t)
	var resolverCalls int
	sm, _ := evidenceSM(t, func(warmAddr, _ string) (bool, error) {
		resolverCalls++
		return warmAddr == bound.Address() || warmAddr == rotated.Address(), nil
	})
	require.NoError(t, sm.VerifyFinishProposerSig(evidenceFinish(t, bound, "escrow-1", 1).GetFinishInference()), "sequenced finish binds the warm key")
	require.Equal(t, map[uint32]string{1: bound.Address()}, sm.WarmKeys())
	resolverCalls = 0
	pending := evidenceRecord(types.StatusPending)
	started := evidenceRecord(types.StatusStarted)

	require.NoError(t, sm.CheckEvidence(pending, evidenceConfirm(t, bound, "escrow-1", pending)))
	require.NoError(t, sm.CheckEvidenceCached(started, evidenceFinish(t, bound, "escrow-1", 1)))
	require.ErrorIs(t, sm.CheckEvidence(started, evidenceFinish(t, rotated, "escrow-1", 1)), types.ErrInvalidProposerSig,
		"a bound slot accepts only its bound warm key, as applyFinish does")
	require.Zero(t, resolverCalls, "a bound slot never asks the resolver")
}
