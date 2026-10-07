package host

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// evidenceGroup is a two-slot escrow-1 state machine. It is the
// EvidenceVerifier a host passes to timeout verification.
type evidenceGroup struct {
	sm      *state.StateMachine
	signers []*signing.Secp256k1Signer
}

func newEvidenceGroup(t *testing.T, opts ...state.SMOption) evidenceGroup {
	t.Helper()
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(signers)
	user := testutil.MustGenerateKey(t)
	config := testutil.DefaultConfig(len(group))
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), &signing.Secp256k1Verifier{},
		testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000), opts...)
	require.NoError(t, err)
	return evidenceGroup{sm: sm, signers: signers}
}

func signedConfirm(t *testing.T, signer signing.Signer, escrowID string, st types.EscrowState, inferenceID uint64, confirmedAt int64) (*types.DevshardTx, []byte) {
	t.Helper()
	rec := st.Inferences[inferenceID]
	sig := testutil.SignExecutorReceipt(t, signer, escrowID, inferenceID, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens, rec.StartedAt, confirmedAt)
	tx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: inferenceID,
		ExecutorSig: sig,
		ConfirmedAt: confirmedAt,
	}}}
	return tx, sig
}

func signedFinish(t *testing.T, signer signing.Signer, escrowID string, inferenceID uint64, slot uint32) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId:  inferenceID,
		EscrowId:     escrowID,
		ExecutorSlot: slot,
		ResponseHash: testutil.TestResponseHash,
		ServedHash:   testutil.TestServedHash,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

// mockExecutorClient is a test double for ExecutorClient.
type mockExecutorClient struct {
	mempool    []*types.DevshardTx
	mempoolErr error

	challengeReceipt    []byte
	challengeMempool    []*types.DevshardTx
	challengeReceiptErr error
	challengePayload    *InferencePayload
	challengeDiffs      []types.Diff
	challengePages      [][]types.Diff
}

func (m *mockExecutorClient) GetMempool(_ context.Context) ([]*types.DevshardTx, error) {
	return m.mempool, m.mempoolErr
}

func (m *mockExecutorClient) ChallengeReceipt(_ context.Context, _ uint64, payload *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
	m.challengePayload = payload
	m.challengeDiffs = diffs
	m.challengePages = append(m.challengePages, append([]types.Diff(nil), diffs...))
	return m.challengeReceipt, m.challengeMempool, m.challengeReceiptErr
}

var testPrompt = testutil.TestPrompt

func testPayload() *InferencePayload {
	return &InferencePayload{
		Prompt:      testPrompt,
		Model:       "llama",
		InputLength: 100,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   1000,
	}
}

func stateWithPending(inferenceID uint64, executorSlot uint32) types.EscrowState {
	return stateWithPendingAt(inferenceID, executorSlot, 1000)
}

func stateWithPendingAt(inferenceID uint64, executorSlot uint32, startedAt int64) types.EscrowState {
	return types.EscrowState{
		EscrowID: "escrow-1",
		Config:   types.SessionConfig{TokenPrice: 1, VoteThreshold: 1, RefusalTimeout: 60, ExecutionTimeout: 1200},
		Inferences: map[uint64]*types.InferenceRecord{
			inferenceID: {
				Status:       types.StatusPending,
				ExecutorSlot: executorSlot,
				ReservedCost: 150,
				StartedAt:    startedAt,
			},
		},
		HostStats: map[uint32]*types.HostStats{0: {}, 1: {}},
		Balance:   10000,
	}
}

// stateWithPendingFull returns a pending state with all fields needed for VerifyPayload.
func stateWithPendingFull(inferenceID uint64, executorSlot uint32) types.EscrowState {
	promptHash := testutil.TestPromptHash
	return types.EscrowState{
		EscrowID: "escrow-1",
		Config:   types.SessionConfig{TokenPrice: 1, VoteThreshold: 1, RefusalTimeout: 60, ExecutionTimeout: 1200},
		Inferences: map[uint64]*types.InferenceRecord{
			inferenceID: {
				Status:       types.StatusPending,
				ExecutorSlot: executorSlot,
				ReservedCost: 150,
				StartedAt:    1000,
				PromptHash:   promptHash[:],
				Model:        "llama",
				InputLength:  100,
				MaxTokens:    testutil.TestMaxTokens,
			},
		},
		HostStats: map[uint32]*types.HostStats{0: {}, 1: {}},
		Balance:   10000,
	}
}

func stateWithStarted(inferenceID uint64, executorSlot uint32) types.EscrowState {
	return stateWithStartedAt(inferenceID, executorSlot, 1000)
}

func stateWithStartedAt(inferenceID uint64, executorSlot uint32, startedAt int64) types.EscrowState {
	return types.EscrowState{
		EscrowID: "escrow-1",
		Config:   types.SessionConfig{TokenPrice: 1, VoteThreshold: 1, RefusalTimeout: 60, ExecutionTimeout: 1200},
		Inferences: map[uint64]*types.InferenceRecord{
			inferenceID: {
				Status:       types.StatusStarted,
				ExecutorSlot: executorSlot,
				ReservedCost: 150,
				StartedAt:    startedAt,
				ConfirmedAt:  startedAt, // executor confirmation anchors execution timeout
			},
		},
		HostStats: map[uint32]*types.HostStats{0: {}, 1: {}},
		Balance:   10000,
	}
}

// deadlinePassedRefused returns a nowUnix that is past the refusal timeout.
func deadlinePassedRefused(st types.EscrowState, inferenceID uint64) int64 {
	return st.Inferences[inferenceID].StartedAt + st.Config.RefusalTimeout + 1
}

// deadlinePassedExecution returns a nowUnix that is past the execution timeout.
func deadlinePassedExecution(st types.EscrowState, inferenceID uint64) int64 {
	return st.Inferences[inferenceID].ConfirmedAt + st.Config.ExecutionTimeout + 1
}

// --- Refused timeout tests ---

func TestVerifyRefused_ReceiptInLocalMempool(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	confirm, _ := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), []*types.DevshardTx{confirm}, nil, nil, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: verified receipt in local mempool")
}

func TestVerifyRefused_UnsignedLocalEvidenceDoesNotReject(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("receipt-sig")}}},
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1}}},
	}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), mempool, nil, nil, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "unverified local txs must not reject the timeout")
}

func TestVerifyRefused_ChallengeErrorAcceptsTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "executor unreachable", err: errors.New("unreachable")},
		{name: "challenge timeout", err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := stateWithPendingFull(1, 1)
			executor := &mockExecutorClient{challengeReceiptErr: tc.err}

			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, newEvidenceGroup(t).sm, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.True(t, accept, "challenge error should be treated as executor unreachable")
		})
	}
}

func TestVerifyRefused_ChallengesOnceWithoutDiffs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt []byte
		accept  bool
	}{
		{name: "no receipt accepts", accept: true},
		{name: "unsigned receipt accepts", receipt: []byte("receipt-sig"), accept: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := stateWithPendingFull(1, 1)
			st.LatestNonce = 100_000
			executor := &mockExecutorClient{challengeReceipt: tc.receipt}

			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, newEvidenceGroup(t).sm, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.Equal(t, tc.accept, accept)
			require.Equal(t, [][]types.Diff{nil}, executor.challengePages, "the executor answers from its own state")
		})
	}
}

func TestVerifyRefused_ReceiptWithoutQueuedConfirmDoesNotReject(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	_, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	executor := &mockExecutorClient{challengeReceipt: sig}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "a receipt is checked against the ConfirmStart it signs")
}

func TestVerifyRefused_InferenceNotPending(t *testing.T) {
	st := stateWithStarted(1, 1) // started, not pending

	_, err := VerifyRefusedTimeout(context.Background(), st, 1, nil, nil, nil, nil, nil, st.Config, deadlinePassedRefused(st, 1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected pending")
}

func TestVerifyRefused_DeadlineNotPassed(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	// nowUnix is before the deadline.
	tooEarly := st.Inferences[1].StartedAt + st.Config.RefusalTimeout - 1

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, nil, nil, st.Config, tooEarly)
	require.NoError(t, err)
	require.False(t, accept, "should reject: deadline not passed")
}

func TestVerifyRefused_NilPayload_Rejects(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte("would-return-receipt")}

	// Nil payload -> error (reject).
	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, nil, nil, executor, nil, nil, st.Config, deadlinePassedRefused(st, 1))
	require.Error(t, err)
	require.False(t, accept, "should reject: nil payload")
	require.Contains(t, err.Error(), "no payload")
}

func TestVerifyRefused_PayloadMismatch_Rejects(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte("would-return-receipt")}

	// Payload with wrong model -> reject (accept=false, no error).
	badPayload := testPayload()
	badPayload.Model = "wrong-model"

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, badPayload, nil, executor, nil, nil, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: payload mismatch")
}

func TestVerifyRefused_FinishInMempool(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	mempool := []*types.DevshardTx{signedFinish(t, g.signers[1], st.EscrowID, 1, 1)}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), mempool, nil, nil, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: verified MsgFinishInference in local mempool")
}

func TestVerifyRefused_CopiesVerifiedReceipt(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	confirm, sig := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	finish := signedFinish(t, g.signers[1], st.EscrowID, 1, 1)
	stub := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}}
	other := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 99,
		ExecutorSig: []byte("other-receipt"),
		ConfirmedAt: 1,
	}}}
	executor := &mockExecutorClient{
		challengeReceipt: sig,
		challengeMempool: []*types.DevshardTx{confirm, other, stub, finish, {}},
	}
	verifierPool := NewMempool()

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, verifierPool, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "a receipt signed by the executor for this escrow must reject the timeout")

	got := findMempoolConfirm(verifierPool.Txs())
	require.NotNil(t, got, "verifier pool must copy the verified MsgConfirmStart")
	require.Equal(t, types.TxHash(confirm), types.TxHash(got))
	require.Equal(t, sig, got.GetConfirmStart().ExecutorSig)
	var copied []uint64
	for _, tx := range verifierPool.Txs() {
		copied = append(copied, types.TxHash(tx))
	}
	require.ElementsMatch(t, []uint64{types.TxHash(confirm), types.TxHash(finish)}, copied,
		"only the verified ConfirmStart and Finish are copied")

	accept, err = VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, verifierPool, g.sm, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept)
	require.Len(t, verifierPool.Txs(), 2, "second challenge must not stack copies")
}

func TestVerifyRefused_ReceiptForOtherEscrowOrSignerDoesNotReject(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	g := newEvidenceGroup(t)
	for name, tc := range map[string]struct {
		signer signing.Signer
		escrow string
	}{
		"other escrow":     {signer: g.signers[1], escrow: "other-escrow"},
		"non-executor key": {signer: g.signers[0], escrow: st.EscrowID},
	} {
		t.Run(name, func(t *testing.T) {
			confirm, sig := signedConfirm(t, tc.signer, tc.escrow, st, 1, 2000)
			executor := &mockExecutorClient{challengeReceipt: sig, challengeMempool: []*types.DevshardTx{confirm}}
			pool := NewMempool()

			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, pool, g.sm, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.True(t, accept)
			require.Empty(t, pool.Txs())
		})
	}
}

// --- Execution timeout tests ---

func TestVerifyExecution_FinishInLocalMempool(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	mempool := []*types.DevshardTx{signedFinish(t, g.signers[1], st.EscrowID, 1, 1)}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, mempool, nil, nil, g.sm, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: verified finish in local mempool")
}

func TestVerifyExecution_StubFinishDoesNotReject(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	stub := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}}
	executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{stub}}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, []*types.DevshardTx{stub}, executor, nil, g.sm, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "a finish with only InferenceId must not reject the timeout")
	require.Nil(t, executor.challengePayload, "execution timeout must not ask the executor to run")
	require.Nil(t, executor.challengeDiffs)
	require.Len(t, executor.challengePages, 1)
}

func TestVerifyExecution_SignedFinishRejects(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	finish := signedFinish(t, g.signers[1], st.EscrowID, 1, 1)
	confirm, _ := signedConfirm(t, g.signers[1], st.EscrowID, st, 1, 2000)
	stub := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}}
	executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{confirm, stub, finish}}
	pool := NewMempool()

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, pool, g.sm, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "a finish signed by the executor for this escrow must reject the timeout")
	got := pool.Txs()
	require.Len(t, got, 1, "only the verified finish is copied into the verifier mempool")
	require.Equal(t, types.TxHash(finish), types.TxHash(got[0]))
	require.Nil(t, findMempoolConfirm(got))
}

func TestVerifyExecution_FinishForOtherEscrowOrSlotDoesNotReject(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	for name, finish := range map[string]*types.DevshardTx{
		"other escrow":       signedFinish(t, g.signers[1], "other-escrow", 1, 1),
		"other slot":         signedFinish(t, g.signers[0], st.EscrowID, 1, 0),
		"slot 1, wrong key":  signedFinish(t, g.signers[0], st.EscrowID, 1, 1),
		"other inference id": signedFinish(t, g.signers[1], st.EscrowID, 2, 1),
	} {
		t.Run(name, func(t *testing.T) {
			executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{finish}}
			accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, nil, g.sm, st.Config, deadlinePassedExecution(st, 1))
			require.NoError(t, err)
			require.True(t, accept)
		})
	}
}

// A flood of matching but unsigned finishes costs at most maxEvidenceChecks
// recoveries, and a valid finish behind them is not reached.
func TestVerifyExecution_BoundsSignatureChecks(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	pool := make([]*types.DevshardTx, 0, maxEvidenceChecks+1)
	for i := 0; i < maxEvidenceChecks; i++ {
		pool = append(pool, &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
			InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, OutputTokens: uint64(i), ProposerSig: []byte("bad"),
		}}})
	}
	pool = append(pool, signedFinish(t, g.signers[1], st.EscrowID, 1, 1))
	counter := &countingEvidence{EvidenceVerifier: g.sm}
	executor := &mockExecutorClient{challengeMempool: pool}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, nil, counter, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept)
	require.Equal(t, maxEvidenceChecks, counter.finishes)
}

func TestVerifyExecution_ShortHashFinishDoesNotReject(t *testing.T) {
	st := stateWithStarted(1, 1)
	g := newEvidenceGroup(t)
	msg := &types.MsgFinishInference{
		InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1,
		ResponseHash: []byte("short"), ServedHash: testutil.TestServedHash,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, g.signers[1], msg)
	executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}},
	}}
	pool := NewMempool()

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, pool, g.sm, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "a signed finish that apply would reject must not block the timeout")
	require.Empty(t, pool.Txs())
}

func TestVerifyExecution_OverflowingTokenCostDoesNotReject(t *testing.T) {
	g := newEvidenceGroup(t)
	for name, mutate := range map[string]func(st *types.EscrowState, msg *types.MsgFinishInference){
		"token sum overflows": func(_ *types.EscrowState, msg *types.MsgFinishInference) {
			msg.InputTokens = 1
			msg.OutputTokens = math.MaxUint64
		},
		"token cost overflows": func(st *types.EscrowState, msg *types.MsgFinishInference) {
			st.Config.TokenPrice = math.MaxUint64
			msg.InputTokens = 2
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := stateWithStarted(1, 1)
			msg := &types.MsgFinishInference{
				InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1,
				ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
			}
			mutate(&st, msg)
			msg.ProposerSig = testutil.SignProposerTx(t, g.signers[1], msg)
			executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{
				{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}},
			}}
			pool := NewMempool()

			accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, pool, g.sm, st.Config, deadlinePassedExecution(st, 1))
			require.NoError(t, err)
			require.True(t, accept, "a signed finish apply would reject for cost overflow must not block the timeout")
			require.Empty(t, pool.Txs())
		})
	}
}

func TestVerifyExecution_WarmKeyFinishRejectsWithoutBinding(t *testing.T) {
	st := stateWithStarted(1, 1)
	warm := testutil.MustGenerateKey(t)
	var executorAddr string
	g := newEvidenceGroup(t, state.WithWarmKeyResolver(func(warmAddr, coldAddr string) (bool, error) {
		return warmAddr == warm.Address() && coldAddr == executorAddr, nil
	}))
	executorAddr = g.signers[1].Address()
	executor := &mockExecutorClient{challengeMempool: []*types.DevshardTx{signedFinish(t, warm, st.EscrowID, 1, 1)}}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, nil, g.sm, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "a finish signed by the executor's warm key rejects the timeout")
	require.Empty(t, g.sm.WarmKeys(), "challenge evidence must not write a warm binding into hashed state")
}

func TestVerifyExecution_ChallengesOnceWithoutDiffs(t *testing.T) {
	st := stateWithStarted(1, 1)
	executor := &mockExecutorClient{}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, nil, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept)
	require.Nil(t, executor.challengePayload)
	require.Nil(t, executor.challengeDiffs, "execution timeout must not send the journal")
	require.Len(t, executor.challengePages, 1)
}

func TestVerifyExecution_ExecutorUnreachable_DeadlinePassed(t *testing.T) {
	st := stateWithStarted(1, 1)
	executor := &mockExecutorClient{challengeReceiptErr: errors.New("unreachable")}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, nil, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "should accept: executor unreachable")
}

func TestVerifyExecution_InferenceNotStarted(t *testing.T) {
	st := stateWithPending(1, 1) // pending, not started

	_, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, nil, nil, st.Config, deadlinePassedExecution(st, 1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected started")
}

func TestVerifyExecution_NilExecutorClient(t *testing.T) {
	st := stateWithStarted(1, 1)

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, nil, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "should accept: no executor client (unreachable)")
}

func TestVerifyExecution_DeadlineNotPassed(t *testing.T) {
	st := stateWithStarted(1, 1)
	tooEarly := st.Inferences[1].StartedAt + st.Config.ExecutionTimeout - 1

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, nil, nil, st.Config, tooEarly)
	require.NoError(t, err)
	require.False(t, accept, "should reject: deadline not passed")
}

type countingEvidence struct {
	EvidenceVerifier
	finishes int
}

func (c *countingEvidence) CheckFinishProposerSig(msg *types.MsgFinishInference) error {
	c.finishes++
	return c.EvidenceVerifier.CheckFinishProposerSig(msg)
}

func TestRecoveryTxsFor_FiltersByInferenceID(t *testing.T) {
	confirm1 := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}}
	confirm2 := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 2}}}
	finish1 := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: 1}}}
	empty := &types.DevshardTx{}

	got := RecoveryTxsFor([]*types.DevshardTx{nil, empty, confirm2, confirm1, finish1}, 1)
	require.Equal(t, []*types.DevshardTx{confirm1, finish1}, got)
}
