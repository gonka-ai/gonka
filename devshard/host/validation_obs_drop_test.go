package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/stub"
	"devshard/types"
)

// blockingRecordObsStore blocks every RecordValidationsAppliedOnce on a channel
// so a test can hold an obs write "in flight" and observe that HandleRequest
// does not wait on it.
type blockingRecordObsStore struct {
	*storage.Memory
	entered chan struct{}
	release chan struct{}
}

func (b *blockingRecordObsStore) RecordValidationsAppliedOnce(escrowID string, entries []storage.ValidationObsEntry) error {
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	return b.Memory.RecordValidationsAppliedOnce(escrowID, entries)
}

// setupObsHost builds a host backed by store with a 3-member group. The signer
// is slot 0, so it can author slot-0 validations.
func setupObsHost(t *testing.T, store storage.Storage) (*Host, *signing.Secp256k1Signer, []*signing.Secp256k1Signer) {
	t.Helper()
	hosts := []*signing.Secp256k1Signer{
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
		testutil.MustGenerateKey(t),
	}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   0,
	}
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{
		EscrowID: "escrow-1", EpochID: 1, Version: testutil.RuntimeTestVersion,
		CreatorAddr: user.Address(), Config: config, Group: group, InitialBalance: 1_000_000,
	}))
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 1_000_000, user.Address(), verifier, store)
	require.NoError(t, err)
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil,
		WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)
	return h, user, hosts
}

// driveToValidation drives inference 1 through start/confirm/finish and returns
// the validation tx (slot 0) plus the next nonce to apply it at.
func driveToValidation(t *testing.T, h *Host, user *signing.Secp256k1Signer, hosts []*signing.Secp256k1Signer) (*types.DevshardTx, uint64) {
	t.Helper()
	const inferenceID = uint64(1)
	const executorSlot = uint32(1)
	engine := stub.NewInferenceEngine()

	apply := func(nonce uint64, txs []*types.DevshardTx) {
		diff := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
		require.NoError(t, err)
	}

	apply(1, []*types.DevshardTx{testutil.StartTx(inferenceID)})

	execSig := testutil.SignExecutorReceipt(t, hosts[executorSlot], "escrow-1", inferenceID,
		testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	apply(2, []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}})

	finishMsg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		ResponseHash: engine.ResponseHash,
		InputTokens:  engine.InputTokens,
		OutputTokens: engine.OutputTokens,
		ExecutorSlot: executorSlot,
		EscrowId:     "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[executorSlot], finishMsg)
	apply(3, []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}})

	valMsg := &types.MsgValidation{InferenceId: inferenceID, ValidatorSlot: 0, Valid: true, EscrowId: "escrow-1"}
	valMsg.ProposerSig = testutil.SignProposerTx(t, hosts[0], valMsg)
	valTx := &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: valMsg}}
	return valTx, 4
}

// TestHost_ValidationObs_HandleRequestDoesNotBlockOnSlowStore proves the normal
// async path does not couple HandleRequest to the obs write: a blocked store
// write must not delay the validation diff's HandleRequest.
func TestHost_ValidationObs_HandleRequestDoesNotBlockOnSlowStore(t *testing.T) {
	store := &blockingRecordObsStore{
		Memory:  storage.NewMemory(),
		entered: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	defer close(store.release)

	h, user, hosts := setupObsHost(t, store)
	valTx, nonce := driveToValidation(t, h, user, hosts)

	valDiff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{valTx})
	done := make(chan struct{})
	go func() {
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{valDiff}})
		require.NoError(t, err)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRequest blocked on a slow obs store write")
	}

	// The async writer should have entered the (blocked) store call.
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("expected the async obs writer to invoke the store")
	}
}

// TestHost_ValidationObs_DropsWhenSaturated proves that once the in-flight cap
// is reached the obs batch is dropped (not written synchronously under h.mu):
// HandleRequest returns promptly and no row is recorded.
func TestHost_ValidationObs_DropsWhenSaturated(t *testing.T) {
	store := storage.NewMemory()
	h, user, hosts := setupObsHost(t, store)
	valTx, nonce := driveToValidation(t, h, user, hosts)

	// Saturate the async writer budget so the next record hits the drop branch.
	h.validationObsInFlight.Store(validationObsInFlightCap)

	valDiff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{valTx})
	done := make(chan struct{})
	go func() {
		_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{valDiff}})
		require.NoError(t, err)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("HandleRequest blocked under obs saturation instead of dropping")
	}

	// Dropped: in-flight counter returns to the cap (Add(1) then Add(-1)), and no
	// obs row is written for slot 0.
	require.Equal(t, int32(validationObsInFlightCap), h.validationObsInFlight.Load())
	require.Equal(t, uint32(0), obsCompletedForSlot(t, store, "escrow-1", 0))
}
