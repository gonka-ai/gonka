package host

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

type timeoutTestVerifier struct{}

func (timeoutTestVerifier) VerifyFinishProposerSig(msg *types.MsgFinishInference) error {
	if bytes.Equal(msg.ProposerSig, []byte("valid-finish")) {
		return nil
	}
	return errors.New("invalid finish signature")
}

func (v timeoutTestVerifier) VerifyFinishInference(msg *types.MsgFinishInference) error {
	if len(msg.ResponseHash) != 32 || len(msg.ServedHash) != 32 {
		return errors.New("invalid finish hash")
	}
	return v.VerifyFinishProposerSig(msg)
}

func (timeoutTestVerifier) VerifyConfirmStart(msg *types.MsgConfirmStart) error {
	if bytes.Equal(msg.ExecutorSig, []byte("receipt-sig")) {
		return nil
	}
	return errors.New("invalid receipt signature")
}

// mockExecutorClient is a test double for ExecutorClient.
type mockExecutorClient struct {
	mempool    []*types.DevshardTx
	mempoolErr error

	challengeReceipt    []byte
	challengeMempool    []*types.DevshardTx
	challengeReceiptErr error
	challengePages      [][]types.Diff
}

func (m *mockExecutorClient) GetMempool(_ context.Context) ([]*types.DevshardTx, error) {
	return m.mempool, m.mempoolErr
}

func (m *mockExecutorClient) ChallengeReceipt(_ context.Context, _ uint64, _ *InferencePayload, diffs []types.Diff) ([]byte, []*types.DevshardTx, error) {
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
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1000, ExecutorSig: []byte("receipt-sig")}}},
	}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), mempool, nil, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: receipt in local mempool")
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

			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.True(t, accept, "challenge error should be treated as executor unreachable")
		})
	}
}

func TestVerifyRefused_ExecutorReturnsReceipt(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte("receipt-sig"), challengeMempool: []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1000, ExecutorSig: []byte("receipt-sig")}}}}}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: executor produced receipt via ChallengeReceipt")
}

func TestVerifyRefused_ChallengesOnceWithoutDiffs(t *testing.T) {
	for _, tc := range []struct {
		name    string
		receipt []byte
		accept  bool
	}{
		{name: "no receipt accepts", accept: true},
		{name: "receipt rejects", receipt: []byte("receipt-sig"), accept: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := stateWithPendingFull(1, 1)
			st.LatestNonce = 100_000
			executor := &mockExecutorClient{challengeReceipt: tc.receipt, challengeMempool: []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1000, ExecutorSig: []byte("receipt-sig")}}}}}

			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.Equal(t, tc.accept, accept)
			require.Equal(t, [][]types.Diff{nil}, executor.challengePages, "the executor answers from its own state")
		})
	}
}

func TestVerifyRefused_ExecutorReturnsEmptyReceipt(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	// Executor reachable but returns nil receipt (cannot produce one).
	executor := &mockExecutorClient{challengeReceipt: nil}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "should accept: executor returned no receipt")
}

func TestVerifyRefused_InferenceNotPending(t *testing.T) {
	st := stateWithStarted(1, 1) // started, not pending

	_, err := VerifyRefusedTimeout(context.Background(), st, 1, nil, nil, nil, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected pending")
}

func TestVerifyRefused_DeadlineNotPassed(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	// nowUnix is before the deadline.
	tooEarly := st.Inferences[1].StartedAt + st.Config.RefusalTimeout - 1

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, nil, timeoutTestVerifier{}, st.Config, tooEarly)
	require.NoError(t, err)
	require.False(t, accept, "should reject: deadline not passed")
}

func TestVerifyRefused_NilPayload_Rejects(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte("would-return-receipt")}

	// Nil payload -> error (reject).
	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, nil, nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
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

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, badPayload, nil, executor, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: payload mismatch")
}

// --- Execution timeout tests ---

func TestVerifyExecution_FinishInLocalMempool(t *testing.T) {
	st := stateWithStarted(1, 1)
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, ProposerSig: []byte("valid-finish")}}},
	}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, mempool, nil, timeoutTestVerifier{}, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: finish in local mempool")
}

func TestVerifyExecution_ExecutorHasFinish(t *testing.T) {
	st := stateWithStarted(1, 1)
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, ProposerSig: []byte("valid-finish"),
	}}}
	executor := &mockExecutorClient{
		mempool: []*types.DevshardTx{finish},
	}
	verifierPool := NewMempool()

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, timeoutTestVerifier{}, verifierPool, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: executor has finish")
	require.Equal(t, types.TxHash(finish), types.TxHash(findMempoolFinish(verifierPool.Txs())))
}

func TestVerifyExecution_InvalidFinishesCannotVeto(t *testing.T) {
	st := stateWithStarted(1, 1)
	for _, tc := range []struct {
		name   string
		finish *types.MsgFinishInference
	}{
		{"bad signature", &types.MsgFinishInference{InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, ProposerSig: []byte("garbage")}},
		{"wrong escrow", &types.MsgFinishInference{InferenceId: 1, EscrowId: "other", ExecutorSlot: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, ProposerSig: []byte("valid-finish")}},
		{"wrong slot", &types.MsgFinishInference{InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 0, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, ProposerSig: []byte("valid-finish")}},
		{"signed malformed hash", &types.MsgFinishInference{InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, ResponseHash: []byte("short"), ServedHash: testutil.TestServedHash, ProposerSig: []byte("valid-finish")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: tc.finish}}
			for _, local := range []bool{false, true} {
				var pool []*types.DevshardTx
				var executor ExecutorClient
				if local {
					pool = []*types.DevshardTx{tx}
				} else {
					executor = &mockExecutorClient{mempool: []*types.DevshardTx{tx}}
				}
				verifierPool := NewMempool()
				accept, err := VerifyExecutionTimeout(context.Background(), st, 1, pool, executor, timeoutTestVerifier{}, verifierPool, st.Config, deadlinePassedExecution(st, 1))
				require.NoError(t, err)
				require.True(t, accept, "invalid Finish must not veto an execution timeout")
				require.Empty(t, verifierPool.Txs(), "invalid Finish must not enter recovery")
			}
		})
	}
}

func TestVerifyExecution_InvalidLocalFinishDoesNotHideValidRemoteFinish(t *testing.T) {
	st := stateWithStarted(1, 1)
	invalid := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}}
	valid := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
		InferenceId: 1, EscrowId: st.EscrowID, ExecutorSlot: 1, ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash, ProposerSig: []byte("valid-finish"),
	}}}
	verifierPool := NewMempool()
	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, []*types.DevshardTx{invalid}, &mockExecutorClient{mempool: []*types.DevshardTx{valid}}, timeoutTestVerifier{}, verifierPool, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.False(t, accept)
	require.Equal(t, types.TxHash(valid), types.TxHash(findMempoolFinish(verifierPool.Txs())))
}

func TestVerifyExecution_ExecutorUnreachable_DeadlinePassed(t *testing.T) {
	st := stateWithStarted(1, 1)
	executor := &mockExecutorClient{mempoolErr: errors.New("unreachable")}

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, executor, timeoutTestVerifier{}, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "should accept: executor unreachable")
}

func TestVerifyExecution_InferenceNotStarted(t *testing.T) {
	st := stateWithPending(1, 1) // pending, not started

	_, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, timeoutTestVerifier{}, nil, st.Config, deadlinePassedExecution(st, 1))
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected started")
}

func TestVerifyExecution_NilExecutorClient(t *testing.T) {
	st := stateWithStarted(1, 1)

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, timeoutTestVerifier{}, nil, st.Config, deadlinePassedExecution(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "should accept: no executor client (unreachable)")
}

func TestVerifyRefused_FinishInMempool(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	mempool := []*types.DevshardTx{
		{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: 1}}},
	}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), mempool, nil, nil, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "a Finish without a receipt cannot block refusal timeout")
}

func TestVerifyRefused_ZeroTimeOrInvalidReceiptCannotVeto(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	for _, tc := range []struct {
		name string
		cs   *types.MsgConfirmStart
	}{
		{"zero time", &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 0, ExecutorSig: []byte("receipt-sig")}},
		{"bad signature", &types.MsgConfirmStart{InferenceId: 1, ConfirmedAt: 1000, ExecutorSig: []byte("garbage")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: tc.cs}}
			verifierPool := NewMempool()
			executor := &mockExecutorClient{challengeReceipt: tc.cs.ExecutorSig, challengeMempool: []*types.DevshardTx{tx}}
			accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), []*types.DevshardTx{tx}, executor, verifierPool, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
			require.NoError(t, err)
			require.True(t, accept)
			require.Empty(t, verifierPool.Txs())
		})
	}
}

func TestVerifyExecution_DeadlineNotPassed(t *testing.T) {
	st := stateWithStarted(1, 1)
	tooEarly := st.Inferences[1].StartedAt + st.Config.ExecutionTimeout - 1

	accept, err := VerifyExecutionTimeout(context.Background(), st, 1, nil, nil, timeoutTestVerifier{}, nil, st.Config, tooEarly)
	require.NoError(t, err)
	require.False(t, accept, "should reject: deadline not passed")
}

func TestVerifyRefused_CopiesChallengeMempool(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1,
		ExecutorSig: []byte("receipt-sig"),
		ConfirmedAt: 1000,
	}}}
	executor := &mockExecutorClient{
		challengeReceipt: []byte("receipt-sig"),
		challengeMempool: []*types.DevshardTx{
			confirm,
			{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
				InferenceId: 99,
				ExecutorSig: []byte("other-receipt"),
				ConfirmedAt: 1,
			}}},
			{},
		},
	}
	verifierPool := NewMempool()

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, verifierPool, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "should reject: executor produced receipt")

	got := findMempoolConfirm(verifierPool.Txs())
	require.NotNil(t, got, "verifier pool must copy MsgConfirmStart from challenge mempool")
	require.Equal(t, types.TxHash(confirm), types.TxHash(got))
	require.Equal(t, []byte("receipt-sig"), got.GetConfirmStart().ExecutorSig)
	for _, tx := range verifierPool.Txs() {
		if cs := tx.GetConfirmStart(); cs != nil {
			require.Equal(t, uint64(1), cs.InferenceId, "verifier must not copy ConfirmStart for other inferences")
		}
	}

	accept, err = VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, executor, verifierPool, timeoutTestVerifier{}, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept)
	var confirmCount int
	for _, tx := range verifierPool.Txs() {
		if tx.GetConfirmStart() != nil {
			confirmCount++
		}
	}
	require.Equal(t, 1, confirmCount, "second challenge must not stack a second ConfirmStart")
}

func TestRecoveryTxsFor_FiltersByInferenceID(t *testing.T) {
	confirm1 := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1}}}
	confirm2 := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 2}}}
	finish1 := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{ServedHash: testutil.TestServedHash, InferenceId: 1}}}
	empty := &types.DevshardTx{}

	got := RecoveryTxsFor([]*types.DevshardTx{nil, empty, confirm2, confirm1, finish1}, 1)
	require.Equal(t, []*types.DevshardTx{confirm1, finish1}, got)
}
