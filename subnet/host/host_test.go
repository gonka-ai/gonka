package host

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"subnet"
	"subnet/internal/testutil"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/stub"
	"subnet/types"
)

// --- Package-specific test helpers ---

// defaultPayload returns the InferencePayload matching testutil.StartTx defaults.
func defaultPayload() *InferencePayload {
	return &InferencePayload{
		Prompt:      testutil.TestPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
	}
}

func newTestHost(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, balance uint64, grace uint64) *Host {
	t.Helper()
	return newTestHostWithChecker(t, hostIdx, hosts, user, balance, grace, nil)
}

func newTestHostWithChecker(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, balance uint64, grace uint64, checker AcceptanceChecker) *Host {
	t.Helper()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	var opts []HostOption
	opts = append(opts, WithGrace(grace))
	h, err := NewHost(sm, hosts[hostIdx], engine, "escrow-1", group, checker, opts...)
	require.NoError(t, err)
	return h
}

// handleAndExecute calls HandleRequest and then RunExecution if there's a deferred job.
// Returns the response with updated mempool.
func handleAndExecute(t *testing.T, h *Host, ctx context.Context, req HostRequest) (*HostResponse, error) {
	t.Helper()
	resp, err := h.HandleRequest(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp.ExecutionJob != nil {
		_, execErr := h.RunExecution(ctx, resp.ExecutionJob)
		if execErr != nil {
			// Log but don't fail -- matches old executeAsync behavior.
			t.Logf("RunExecution error: %v", execErr)
		}
		resp.Mempool = h.MempoolTxs()
	}
	return resp, nil
}

// findMempoolTx returns the first mempool tx matching the given type.
func findMempoolFinish(txs []*types.SubnetTx) *types.SubnetTx {
	for _, tx := range txs {
		if tx.GetFinishInference() != nil {
			return tx
		}
	}
	return nil
}

// --- Tests ---

func TestHost_AppliesDiffs(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 0, hosts, user, 10000, 10)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp.Nonce)
}

func TestHost_SignsState(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 0, hosts, user, 10000, 10)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig)

	// Verify the signature recovers to host[0]'s address against StateSignatureContent.
	verifier := signing.NewSecp256k1Verifier()
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	sm2 := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	_, err = sm2.ApplyDiff(diff)
	require.NoError(t, err)
	root, err := sm2.ComputeStateRoot()
	require.NoError(t, err)

	sigContent := &types.StateSignatureContent{
		StateRoot: root,
		EscrowId:  "escrow-1",
		Nonce:     1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)

	addr, err := verifier.RecoverAddress(sigData, resp.StateSig)
	require.NoError(t, err)
	require.Equal(t, hosts[0].Address(), addr)
}

func TestHost_ExecutorReceipt(t *testing.T) {
	// 3 hosts. Inference 1: executor = group[1%3] = slot 1.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 10000, 10) // host at slot 1

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt, "executor should return receipt")

	// Verify receipt is a valid executor signature.
	require.NotZero(t, resp.ConfirmedAt, "executor should set confirmed_at")
	verifier := signing.NewSecp256k1Verifier()
	receiptContent := &types.ExecutorReceiptContent{
		InferenceId: 1,
		PromptHash:  testutil.TestPromptHash[:],
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   50,
		StartedAt:   1000,
		EscrowId:    "escrow-1",
		ConfirmedAt: resp.ConfirmedAt,
	}
	data, err := proto.Marshal(receiptContent)
	require.NoError(t, err)
	addr, err := verifier.RecoverAddress(data, resp.Receipt)
	require.NoError(t, err)
	require.Equal(t, hosts[1].Address(), addr)
}

func TestHost_NonExecutorNoReceipt(t *testing.T) {
	// 3 hosts. Inference 1: executor = slot 1. Host 0 is NOT executor.
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 0, hosts, user, 10000, 10) // host at slot 0

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Receipt, "non-executor should not return receipt")
}

func TestHost_ProducesMsgFinish(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 10000, 10) // executor for inference 1

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.Len(t, resp.Mempool, 2, "should have confirm_start + finish")

	fin := findMempoolFinish(resp.Mempool).GetFinishInference()
	require.NotNil(t, fin)
	require.Equal(t, uint64(1), fin.InferenceId)
	require.Equal(t, uint32(1), fin.ExecutorSlot)
	require.Equal(t, uint64(80), fin.InputTokens)
	require.Equal(t, uint64(40), fin.OutputTokens)
	require.NotNil(t, fin.ProposerSig)
}

func TestHost_WithholdsOnStaleTx(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 100000, 2) // grace=2

	// Nonce 1: start inference 1, executor=slot 1 -> produces mempool entry at nonce 1.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should sign at nonce 1 (not stale yet)")

	// Nonces 2,3: empty diffs, mempool entry proposed at 1, grace=2.
	// At nonce 3: 1+2=3, not < 3 -> still OK.
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, nil)
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, nil)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should sign at nonce 3 (1+2=3, not < 3)")

	// Nonce 4: 1+2=3 < 4 -> stale -> withhold.
	diff4 := testutil.SignDiff(t, user, "escrow-1", 4, nil)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff4}})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "should withhold at nonce 4 (stale)")
	require.Equal(t, uint64(4), resp.Nonce)
	require.Equal(t, 2, h.mempool.Len(), "mempool should have confirm_start + finish")
}

func TestHost_SignsAfterIncluded(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 100000, 2) // grace=2

	// Nonce 1: start inference 1 -> executor, mempool entry.
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff1}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)

	// Get the finish and confirm txs from mempool to include in later diffs.
	finishTx := findMempoolFinish(resp.Mempool)
	require.NotNil(t, finishTx, "mempool should contain MsgFinishInference")

	// Find confirm_start from mempool (put there by signReceipt).
	var confirmTx *types.SubnetTx
	for _, tx := range resp.Mempool {
		if tx.GetConfirmStart() != nil {
			confirmTx = tx
			break
		}
	}
	require.NotNil(t, confirmTx, "mempool should contain MsgConfirmStart")

	// Nonce 2: confirm start (needed for state machine to accept finish).
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.SubnetTx{confirmTx})

	// Nonce 3: empty (to push past grace).
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, nil)
	// Nonce 4: empty (stale at this point).
	diff4 := testutil.SignDiff(t, user, "escrow-1", 4, nil)

	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3, diff4}})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "should withhold (stale)")

	// Nonce 5: include the finish tx -> mempool cleared -> should sign.
	diff5 := testutil.SignDiff(t, user, "escrow-1", 5, []*types.SubnetTx{finishTx})
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff5}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should sign after inclusion")
	require.Empty(t, resp.Mempool)
}

func TestHost_NotInGroup(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	outsider := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, outsider.Address(), verifier)
	engine := stub.NewInferenceEngine()

	_, err := NewHost(sm, outsider, engine, "escrow-1", group, nil, WithGrace(10))
	require.ErrorIs(t, err, types.ErrHostNotInGroup)
}

// makeMultiSlotGroup builds a group where signers[dupIdx] occupies two slots.
// The extra slot is appended at the end.
func makeMultiSlotGroup(signers []*signing.Secp256k1Signer, dupIdx int) []types.SlotAssignment {
	group := testutil.MakeGroup(signers)
	// Add a second slot for signers[dupIdx].
	extra := types.SlotAssignment{
		SlotID:           uint32(len(signers)),
		ValidatorAddress: signers[dupIdx].Address(),
	}
	return append(group, extra)
}

func newMultiSlotHost(t *testing.T, hostIdx int, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, group []types.SlotAssignment, balance uint64, grace uint64) *Host {
	t.Helper()
	config := testutil.DefaultConfig(len(group))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, balance, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[hostIdx], engine, "escrow-1", group, nil, WithGrace(grace))
	require.NoError(t, err)
	return h
}

func TestHost_MultiSlotExecutor(t *testing.T) {
	// 3 signers, signer[0] holds slots 0 and 3 (4 slots total).
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := makeMultiSlotGroup(hosts, 0)
	// group has 4 slots: 0(hosts[0]), 1(hosts[1]), 2(hosts[2]), 3(hosts[0]).

	h := newMultiSlotHost(t, 0, hosts, user, group, 100000, 10)

	// Verify host holds both slots.
	require.True(t, h.slotIDs[0])
	require.True(t, h.slotIDs[3])
	require.Len(t, h.slotIDs, 2)

	// inference_id must equal nonce. Pick nonces that map to the right executor slots.
	// nonce 4: executor = group[4%4]=group[0] -> slot 0 -> hosts[0] executes.
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, nil) // empty diff to advance nonce
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, nil)
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, nil)
	diff4 := testutil.SignDiff(t, user, "escrow-1", 4, []*types.SubnetTx{testutil.StartTx(4)})
	resp, err := handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff1, diff2, diff3, diff4},
		Nonce: 4, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt, "host should execute for slot 0 (nonce 4)")
	require.Len(t, resp.Mempool, 2, "should have MsgConfirmStart + MsgFinishInference")
	fin4 := findMempoolFinish(resp.Mempool).GetFinishInference()
	require.NotNil(t, fin4)
	require.Equal(t, uint32(0), fin4.ExecutorSlot)

	// nonce 6: executor = group[6%4]=group[2] -> slot 2 -> hosts[2], NOT hosts[0].
	diff5 := testutil.SignDiff(t, user, "escrow-1", 5, nil)
	diff6 := testutil.SignDiff(t, user, "escrow-1", 6, []*types.SubnetTx{testutil.StartTx(6)})
	resp, err = h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff5, diff6}, Nonce: 6, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.Nil(t, resp.Receipt, "host should NOT execute for slot 2")

	// nonce 7: executor = group[7%4]=group[3] -> slot 3 -> hosts[0] again.
	diff7 := testutil.SignDiff(t, user, "escrow-1", 7, []*types.SubnetTx{testutil.StartTx(7)})
	resp, err = handleAndExecute(t, h, context.Background(), HostRequest{
		Diffs: []types.Diff{diff7}, Nonce: 7, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt, "host should execute for slot 3 (nonce 7)")
	require.Len(t, resp.Mempool, 4, "confirm+finish for inf 4 and inf 7")
	var fin7 *types.MsgFinishInference
	for _, tx := range resp.Mempool {
		if f := tx.GetFinishInference(); f != nil && f.InferenceId == 7 {
			fin7 = f
			break
		}
	}
	require.NotNil(t, fin7)
	require.Equal(t, uint32(3), fin7.ExecutorSlot)
}

// mockAcceptanceChecker blocks when blockFn returns true.
type mockAcceptanceChecker struct {
	blockFn func(types.EscrowState) bool
}

func (m *mockAcceptanceChecker) Check(st types.EscrowState, _ []*types.SubnetTx) error {
	if m.blockFn(st) {
		return fmt.Errorf("acceptance check failed")
	}
	return nil
}

func TestHost_WithholdsOnAcceptanceBlock(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)

	// Block whenever there's any inference in the state.
	checker := &mockAcceptanceChecker{
		blockFn: func(st types.EscrowState) bool {
			return len(st.Inferences) > 0
		},
	}
	h := newTestHostWithChecker(t, 0, hosts, user, 10000, 100, checker)

	// Empty diff: no inferences -> should sign.
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, nil)
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "should sign with no inferences")

	// Diff with start inference: checker blocks.
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.SubnetTx{testutil.StartTx(2)})
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "should withhold due to acceptance check")
}

func TestHost_AcceptanceBlockPersistsAcrossRounds(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)

	callCount := 0
	// Block for first 2 calls, then allow.
	checker := &mockAcceptanceChecker{
		blockFn: func(_ types.EscrowState) bool {
			callCount++
			return callCount <= 2
		},
	}
	h := newTestHostWithChecker(t, 0, hosts, user, 100000, 100, checker)

	// Round 1: blocked.
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "round 1: blocked")

	// Round 2: still blocked.
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, nil)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	require.Nil(t, resp.StateSig, "round 2: still blocked")

	// Round 3: checker allows.
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, nil)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff3}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig, "round 3: checker allows signing")
}

func TestHost_PayloadMismatch_PromptHash(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 10000, 10)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	badPayload := defaultPayload()
	badPayload.Prompt = []byte("wrong prompt")
	_, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: badPayload,
	})
	require.ErrorIs(t, err, types.ErrPromptHashMismatch)
}

func TestHost_PayloadMismatch_Params(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 10000, 10)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	badPayload := defaultPayload()
	badPayload.MaxTokens = 999
	_, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: badPayload,
	})
	require.ErrorIs(t, err, types.ErrPayloadMismatch)
}

func TestHost_StoresOwnSignature(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateSig)

	// Own sig should be stored in storage.
	sigs, err := store.GetSignatures("escrow-1", 1)
	require.NoError(t, err)
	require.Equal(t, resp.StateSig, sigs[0])
}

func TestHost_AccumulateGossipSig(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	// Apply a diff to create a backed nonce.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateHash)

	// Sign from host[1] for the same state.
	sigContent := &types.StateSignatureContent{
		StateRoot: resp.StateHash,
		EscrowId:  "escrow-1",
		Nonce:     1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	peerSig, err := hosts[1].Sign(sigData)
	require.NoError(t, err)

	err = h.AccumulateGossipSig(1, resp.StateHash, peerSig, 1)
	require.NoError(t, err)

	// Verify stored.
	sigs, err := store.GetSignatures("escrow-1", 1)
	require.NoError(t, err)
	require.Equal(t, peerSig, sigs[1])
}

func TestHost_AccumulateGossipSig_WrongSigner(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	// Sign with hosts[2] but claim slot 1 -> address mismatch.
	sigContent := &types.StateSignatureContent{
		StateRoot: resp.StateHash,
		EscrowId:  "escrow-1",
		Nonce:     1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	wrongSig, err := hosts[2].Sign(sigData)
	require.NoError(t, err)

	err = h.AccumulateGossipSig(1, resp.StateHash, wrongSig, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected")
}

func TestHost_GetSignatures(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	sigs, err := h.GetSignatures(1)
	require.NoError(t, err)
	require.NotEmpty(t, sigs)
	require.NotNil(t, sigs[0], "own sig at slot 0")
}

func TestHost_GetSignatures_NoStorage(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 0, hosts, user, 10000, 10)

	_, err := h.GetSignatures(1)
	require.Error(t, err)
}

func TestHost_FinalizationThreshold(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	// Apply a diff so nonce 1 exists.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateHash)

	// After HandleRequest, host[0] stores its own sig. 1 sig < threshold (2*3/3+1=3).
	last, err := h.LastFinalized()
	require.NoError(t, err)
	require.Equal(t, uint64(0), last, "1 sig should not finalize")

	// Accumulate sig from host[1].
	sigContent := &types.StateSignatureContent{
		StateRoot: resp.StateHash,
		EscrowId:  "escrow-1",
		Nonce:     1,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	sig1, err := hosts[1].Sign(sigData)
	require.NoError(t, err)
	err = h.AccumulateGossipSig(1, resp.StateHash, sig1, 1)
	require.NoError(t, err)

	// 2 sigs < 3 threshold.
	last, err = h.LastFinalized()
	require.NoError(t, err)
	require.Equal(t, uint64(0), last, "2 sigs should not finalize")

	// Accumulate sig from host[2] -> 3 sigs >= threshold.
	sig2, err := hosts[2].Sign(sigData)
	require.NoError(t, err)
	err = h.AccumulateGossipSig(1, resp.StateHash, sig2, 2)
	require.NoError(t, err)

	last, err = h.LastFinalized()
	require.NoError(t, err)
	require.Equal(t, uint64(1), last, "3 sigs should finalize nonce 1")
}

func TestHost_LatestNonce(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 0, hosts, user, 10000, 10)

	require.Equal(t, uint64(0), h.LatestNonce())

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff}})
	require.NoError(t, err)

	require.Equal(t, uint64(1), h.LatestNonce())
}

func TestHost_ExecuteFailure_ReturnsReceiptNoMempool(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)

	// Create host with a failing engine at slot 1 (executor for inference 1).
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := stub.NewFailingEngine(fmt.Errorf("GPU error"))
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err, "should not return error on engine failure")
	require.NotNil(t, resp.Receipt, "receipt should still be present")
	require.NotNil(t, resp.ExecutionJob, "should have deferred execution job")

	// RunExecution should fail but not crash.
	_, execErr := h.RunExecution(context.Background(), resp.ExecutionJob)
	require.Error(t, execErr, "engine failure should propagate")
	// Mempool has MsgConfirmStart (from signReceipt) but no MsgFinishInference.
	mptxs := h.MempoolTxs()
	require.Len(t, mptxs, 1, "mempool should have only MsgConfirmStart")
	require.NotNil(t, mptxs[0].GetConfirmStart())
}

// countingEngine wraps stub engine and counts Execute calls.
type countingEngine struct {
	inner *stub.InferenceEngine
	calls int
}

func (e *countingEngine) Execute(ctx context.Context, req subnet.ExecuteRequest) (*subnet.ExecuteResult, error) {
	e.calls++
	return e.inner.Execute(ctx, req)
}

func TestHost_SignReceipt_NoDuplicateExecution(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := &countingEngine{inner: stub.NewInferenceEngine()}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})

	// Simulate in-flight execution by pre-marking inference 1 as executing.
	h.mu.Lock()
	h.executing[1] = struct{}{}
	h.mu.Unlock()

	// Request returns receipt (proves executor alive) but skips execution.
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt, "should return receipt to prove executor alive")
	require.Nil(t, resp.ExecutionJob, "should not produce execution job (already executing)")
	require.Equal(t, 0, engine.calls, "engine should not be called (already executing)")
}

func TestHost_ExecutingCleanup(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := &countingEngine{inner: stub.NewInferenceEngine()}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)

	// Execute via RunExecution.
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)

	// After execute completes, executing map should be clean.
	h.mu.Lock()
	_, inMap := h.executing[1]
	h.mu.Unlock()
	require.False(t, inMap, "inference ID should be removed from executing after completion")
}

func TestHost_ChallengeReceipt_AlreadyExecuting(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := &countingEngine{inner: stub.NewInferenceEngine()}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	// First: normal request + execution.
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	if resp.ExecutionJob != nil {
		_, _ = h.RunExecution(context.Background(), resp.ExecutionJob)
	}

	// Simulate: manually mark inference as executing (it already completed above,
	// so we re-add it to test the guard).
	h.mu.Lock()
	h.executing[1] = struct{}{}
	h.mu.Unlock()

	// ChallengeReceipt returns receipt (proves executor is alive) but skips execution.
	receipt, _, err := h.ChallengeReceipt(context.Background(), 1, defaultPayload(), []types.Diff{diff})
	require.NoError(t, err)
	require.NotNil(t, receipt, "should return receipt to prove executor is alive")
	// Engine was called once from RunExecution, not again from ChallengeReceipt.
	require.Equal(t, 1, engine.calls, "engine should not be called again")
}

func TestHost_ChallengeReceipt_AlreadyFinished(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier)
	engine := &countingEngine{inner: stub.NewInferenceEngine()}
	h, err := NewHost(sm, hosts[1], engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})

	// Normal request: produces receipt + deferred execution job.
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.Receipt)
	require.NotNil(t, resp.ExecutionJob)

	// Run execution to populate mempool with MsgFinishInference.
	_, err = h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	require.Len(t, h.MempoolTxs(), 2, "should have MsgConfirmStart + MsgFinishInference in mempool")

	// ChallengeReceipt returns receipt (proves executor is alive) but skips execution.
	receipt, _, err := h.ChallengeReceipt(context.Background(), 1, defaultPayload(), []types.Diff{diff})
	require.NoError(t, err)
	require.NotNil(t, receipt, "should return receipt even when finish is in mempool")
	require.Equal(t, 1, engine.calls, "engine should not be called again")
}

func TestWarmKey_HostFindsSlotByWarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)
	executorIdx := 1 // inference 1 % 3 = 1

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[executorIdx].Address(), nil
	}

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, state.WithWarmKeyResolver(resolver))

	// Apply start + confirm with warm key to populate WarmKeys in state.
	nonce := uint64(1)
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.SubnetTx{testutil.StartTx(1)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 1000)
	nonce++
	confirmTx := &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	}}}
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.SubnetTx{confirmTx})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	// Verify warm key binding exists.
	warmKeys := sm.WarmKeys()
	require.Equal(t, warmSigner.Address(), warmKeys[uint32(executorIdx)])

	// Create Host with warmSigner (not in group as cold key).
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, warmSigner, engine, "escrow-1", group, nil, WithGrace(10))
	require.NoError(t, err)

	// Host should have found its slot via WarmKeys check.
	require.True(t, h.SlotIDs()[uint32(executorIdx)], "host should own executor slot via warm key")
	require.Len(t, h.SlotIDs(), 1)
}

// trackingValidationEngine records Validate calls for test assertions.
type trackingValidationEngine struct {
	mu    sync.Mutex
	calls []subnet.ValidateRequest
	valid bool
}

func (e *trackingValidationEngine) Validate(_ context.Context, req subnet.ValidateRequest) (*subnet.ValidateResult, error) {
	e.mu.Lock()
	e.calls = append(e.calls, req)
	e.mu.Unlock()
	return &subnet.ValidateResult{Valid: e.valid}, nil
}

func (e *trackingValidationEngine) getCalls() []subnet.ValidateRequest {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]subnet.ValidateRequest(nil), e.calls...)
}

func TestHost_ValidationTriggersOnFinishedInference(t *testing.T) {
	// 2 hosts. Host 0 is the validator, host 1 is executor for inference 1.
	// With 2 hosts and 100% ValidationRate, probability = 1/(2-1) = 1.0 (guaranteed).
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	group := testutil.MakeGroup(hosts)
	// Use 100% validation rate so ShouldValidate always returns true.
	config := types.SessionConfig{
		RefusalTimeout:   60,
		ExecutionTimeout: 1200,
		TokenPrice:       1,
		VoteThreshold:    1,
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier)

	valEngine := &trackingValidationEngine{valid: true}
	engine := stub.NewInferenceEngine()
	h, err := NewHost(sm, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithValidator(valEngine))
	require.NoError(t, err)

	// Nonce 1: StartInference (executor = slot 1, not host 0).
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)

	// No validation yet -- inference is only Pending/Started, not Finished.
	require.Empty(t, valEngine.getCalls(), "should not validate pending inference")

	// Nonce 2: ConfirmStart (to transition from Pending to Started).
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.SubnetTx{confirmTx})

	// Nonce 3: FinishInference from executor.
	finishMsg := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: engine.ResponseHash,
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: 1,
		EscrowId:     "escrow-1",
	}
	finishData, err := proto.Marshal(finishMsg)
	require.NoError(t, err)
	finishSig, err := hosts[1].Sign(finishData)
	require.NoError(t, err)
	finishMsg.ProposerSig = finishSig
	finishTx := &types.SubnetTx{Tx: &types.SubnetTx_FinishInference{FinishInference: finishMsg}}
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.SubnetTx{finishTx})

	resp, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2, diff3}})
	require.NoError(t, err)

	// Give async validation goroutine time to complete.
	// In real code this is fast since the stub returns immediately.
	require.Eventually(t, func() bool {
		return len(valEngine.getCalls()) > 0
	}, 2*time.Second, 10*time.Millisecond, "validation should have been triggered")

	require.Equal(t, uint64(1), valEngine.getCalls()[0].InferenceID)

	// MsgValidation should appear in mempool.
	require.Eventually(t, func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		for _, tx := range h.mempool.Txs() {
			if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
				return true
			}
		}
		return false
	}, 2*time.Second, 10*time.Millisecond, "MsgValidation should be in mempool")

	// Next HandleRequest should return mempool with validation.
	diff4 := testutil.SignDiff(t, user, "escrow-1", 4, nil)
	resp, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff4}})
	require.NoError(t, err)

	var foundValidation bool
	for _, tx := range resp.Mempool {
		if v := tx.GetValidation(); v != nil && v.InferenceId == 1 {
			foundValidation = true
			require.Equal(t, uint32(0), v.ValidatorSlot)
			require.True(t, v.Valid)
			require.NotNil(t, v.ProposerSig)
			break
		}
	}
	require.True(t, foundValidation, "MsgValidation should be in response mempool")
}

func TestHost_ResponseCache_Lifecycle(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	user := testutil.MustGenerateKey(t)
	h := newTestHost(t, 1, hosts, user, 100000, 100)

	// Execute inference 1 via HandleRequest + RunExecution.
	diff := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	resp, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.NotNil(t, resp.ExecutionJob)
	require.Nil(t, resp.CachedResponseBody, "first request should not have cached body")

	result, err := h.RunExecution(context.Background(), resp.ExecutionJob)
	require.NoError(t, err)
	require.NotEmpty(t, result.ResponseBody)

	// Verify cache populated.
	h.mu.Lock()
	cached, ok := h.completedResponses[1]
	h.mu.Unlock()
	require.True(t, ok, "response should be cached after execution")
	require.Equal(t, result.ResponseBody, cached)

	// Reconnect: same request again should return cached body, no execution job.
	resp2, err := h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1, Payload: defaultPayload(),
	})
	require.NoError(t, err)
	require.Nil(t, resp2.ExecutionJob, "reconnect should not trigger new execution")
	require.NotNil(t, resp2.CachedResponseBody, "reconnect should return cached body")
	require.Equal(t, result.ResponseBody, resp2.CachedResponseBody)

	// Evict: apply diff with MsgFinishInference.
	finishTx := findMempoolFinish(h.MempoolTxs())
	require.NotNil(t, finishTx)
	confirmTx := findMempoolConfirm(h.MempoolTxs())
	require.NotNil(t, confirmTx)

	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.SubnetTx{confirmTx})
	diff3 := testutil.SignDiff(t, user, "escrow-1", 3, []*types.SubnetTx{finishTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{
		Diffs: []types.Diff{diff2, diff3},
	})
	require.NoError(t, err)

	h.mu.Lock()
	_, stillCached := h.completedResponses[1]
	h.mu.Unlock()
	require.False(t, stillCached, "cache should be evicted after MsgFinishInference in diff")
}

func findMempoolConfirm(txs []*types.SubnetTx) *types.SubnetTx {
	for _, tx := range txs {
		if tx.GetConfirmStart() != nil {
			return tx
		}
	}
	return nil
}

func TestAccumulateGossipSig_WarmKey(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	warmSigner := testutil.MustGenerateKey(t)
	user := testutil.MustGenerateKey(t)

	resolver := func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warmSigner.Address() && coldAddr == hosts[1].Address(), nil
	}

	group := testutil.MakeGroup(hosts)
	config := testutil.DefaultConfig(len(hosts))
	verifier := signing.NewSecp256k1Verifier()
	store := storage.NewMemory()
	require.NoError(t, store.CreateSession(storage.CreateSessionParams{EscrowID: "escrow-1", Config: config, Group: group, InitialBalance: 10000}))

	sm := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, state.WithWarmKeyResolver(resolver))

	// Create warm key binding via confirm start.
	// inference 1 % 3 = 1, executor = slot 1.
	nonce := uint64(1)
	diff := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.SubnetTx{testutil.StartTx(1)})
	_, err := sm.ApplyDiff(diff)
	require.NoError(t, err)

	execSig := testutil.SignExecutorReceipt(t, warmSigner, "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 1000)
	nonce++
	confirmTx := &types.SubnetTx{Tx: &types.SubnetTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 1000,
	}}}
	diff = testutil.SignDiff(t, user, "escrow-1", nonce, []*types.SubnetTx{confirmTx})
	_, err = sm.ApplyDiff(diff)
	require.NoError(t, err)

	engine := stub.NewInferenceEngine()

	// Create a fresh SM+host for storage population.
	sm2 := state.NewStateMachine("escrow-1", config, group, 10000, user.Address(), verifier, state.WithWarmKeyResolver(resolver))
	diff1 := testutil.SignDiff(t, user, "escrow-1", 1, []*types.SubnetTx{testutil.StartTx(1)})
	diff2 := testutil.SignDiff(t, user, "escrow-1", 2, []*types.SubnetTx{confirmTx})

	h2, err := NewHost(sm2, hosts[0], engine, "escrow-1", group, nil,
		WithGrace(10), WithStorage(store), WithVerifier(verifier))
	require.NoError(t, err)

	resp, err := h2.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1, diff2}})
	require.NoError(t, err)
	require.NotNil(t, resp.StateHash)

	// Sign state with warm key (on behalf of slot 1).
	sigContent := &types.StateSignatureContent{
		StateRoot: resp.StateHash,
		EscrowId:  "escrow-1",
		Nonce:     2,
	}
	sigData, err := proto.Marshal(sigContent)
	require.NoError(t, err)
	warmSig, err := warmSigner.Sign(sigData)
	require.NoError(t, err)

	err = h2.AccumulateGossipSig(2, resp.StateHash, warmSig, 1)
	require.NoError(t, err, "warm key signature should be accepted")

	// Verify stored for slot 1.
	sigs, err := store.GetSignatures("escrow-1", 2)
	require.NoError(t, err)
	require.Equal(t, warmSig, sigs[1])
}
