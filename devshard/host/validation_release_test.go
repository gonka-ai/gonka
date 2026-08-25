package host

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/stub"
	"devshard/types"
)

type recordingLeaseRecorder struct {
	mu           sync.Mutex
	allowErr     error
	markErr      error
	releaseCalls int
	allowCalls   int
	markCalls    int
}

func (r *recordingLeaseRecorder) AllowValidationSubmit(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.allowCalls++
	return r.allowErr
}

func (r *recordingLeaseRecorder) MarkValidationSubmitted(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.markCalls++
	return r.markErr
}

func (r *recordingLeaseRecorder) ReleaseValidationLease(context.Context, string, uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	return nil
}

func (r *recordingLeaseRecorder) counts() (allow, mark, release int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.allowCalls, r.markCalls, r.releaseCalls
}

type scriptedValidationEngine struct {
	result *devshard.ValidateResult
	err    error
}

func (e *scriptedValidationEngine) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	if e.err != nil {
		return nil, e.err
	}
	if e.result != nil {
		return e.result, nil
	}
	return &devshard.ValidateResult{Valid: true}, nil
}

type errorSigner struct {
	addr string
	err  error
}

func (s errorSigner) Address() string             { return s.addr }
func (s errorSigner) Sign([]byte) ([]byte, error) { return nil, s.err }

func newLeaseReleaseHost(t *testing.T, validator devshard.ValidationEngine, rec *recordingLeaseRecorder) (*Host, []*signing.Secp256k1Signer, *signing.Secp256k1Signer) {
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
		ValidationRate:   10000,
	}
	verifier := signing.NewSecp256k1Verifier()
	sm, err := state.NewStateMachine("escrow-1", config, group, 100000, user.Address(), verifier, testutil.MustMemoryStore(t, "escrow-1", user.Address(), config, group, 100000))
	require.NoError(t, err)

	opts := []HostOption{WithGrace(10), WithValidator(validator), WithEpochID(1)}
	if rec != nil {
		opts = append(opts, WithValidationCompletionRecorder(rec))
	}
	h, err := NewHost(sm, hosts[0], stub.NewInferenceEngine(), "escrow-1", group, nil, opts...)
	require.NoError(t, err)
	return h, hosts, user
}

func applyInferenceTo(t *testing.T, h *Host, hosts []*signing.Secp256k1Signer, user *signing.Secp256k1Signer, status types.InferenceStatus) {
	t.Helper()
	engine := stub.NewInferenceEngine()
	nonce := uint64(1)
	diff1 := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{testutil.StartTx(1)})
	_, err := h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff1}})
	require.NoError(t, err)
	if status == types.StatusPending {
		return
	}

	nonce++
	execSig := testutil.SignExecutorReceipt(t, hosts[1], "escrow-1", 1, testutil.TestPromptHash[:], "llama", 100, 50, 1000, 2000)
	confirmTx := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId: 1, ExecutorSig: execSig, ConfirmedAt: 2000,
	}}}
	diff2 := testutil.SignDiff(t, user, "escrow-1", nonce, []*types.DevshardTx{confirmTx})
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff2}})
	require.NoError(t, err)
	if status == types.StatusStarted {
		return
	}

	nonce++
	finishMsg := &types.MsgFinishInference{
		InferenceId:  1,
		ResponseHash: engine.ResponseHash,
		InputTokens:  80,
		OutputTokens: 40,
		ExecutorSlot: 1,
		EscrowId:     "escrow-1",
	}
	finishMsg.ProposerSig = testutil.SignProposerTx(t, hosts[1], finishMsg)
	txs := []*types.DevshardTx{{Tx: &types.DevshardTx_FinishInference{FinishInference: finishMsg}}}
	if status == types.StatusChallenged {
		challengeMsg := &types.MsgValidation{
			InferenceId:   1,
			ValidatorSlot: 2,
			Valid:         false,
			EscrowId:      "escrow-1",
		}
		challengeMsg.ProposerSig = testutil.SignProposerTx(t, hosts[2], challengeMsg)
		txs = append(txs, &types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: challengeMsg}})
	}
	diff3 := testutil.SignDiff(t, user, "escrow-1", nonce, txs)
	_, err = h.HandleRequest(context.Background(), HostRequest{Diffs: []types.Diff{diff3}})
	require.NoError(t, err)
}

func testValidateJob() validateJob {
	return validateJob{
		inferenceID:     1,
		validatorSlot:   0,
		flow:            validationFlowShouldValidate,
		model:           "llama",
		escrowID:        "escrow-1",
		executorAddress: "executor",
		epochID:         1,
	}
}

func mempoolHasValidation(h *Host, infID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidation(); v != nil && v.InferenceId == infID {
			return true
		}
	}
	return false
}

func mempoolHasVote(h *Host, infID uint64) bool {
	for _, tx := range h.MempoolTxs() {
		if v := tx.GetValidationVote(); v != nil && v.InferenceId == infID {
			return true
		}
	}
	return false
}

func TestHost_ValidateAsync_ReleasesOnNonSubmitPaths(t *testing.T) {
	signFail := errors.New("sign failed")
	tests := []struct {
		name        string
		status      types.InferenceStatus
		skipApply   bool
		validator   scriptedValidationEngine
		allowErr    error
		markErr     error
		failSign    bool
		wantRelease int
		wantAllow   int
		wantMark    int
		wantVal     bool
		wantVote    bool
	}{
		{
			name:        "validate error",
			skipApply:   true,
			validator:   scriptedValidationEngine{err: errors.New("local ml 503")},
			wantRelease: 1,
		},
		{
			name:        "validation skipped",
			skipApply:   true,
			validator:   scriptedValidationEngine{err: devshard.ErrValidationSkipped},
			wantRelease: 1,
		},
		{
			name:      "already leased",
			skipApply: true,
			validator: scriptedValidationEngine{err: devshard.ErrValidationAlreadyLeased},
		},
		{
			name:        "inference disappeared",
			skipApply:   true,
			wantRelease: 1,
		},
		{
			name:        "status neither finished nor challenged",
			status:      types.StatusStarted,
			wantRelease: 1,
		},
		{
			name:        "sign fail finished",
			status:      types.StatusFinished,
			failSign:    true,
			wantRelease: 1,
		},
		{
			name:        "sign fail challenged",
			status:      types.StatusChallenged,
			failSign:    true,
			wantRelease: 1,
		},
		{
			name:        "allow submit refused",
			status:      types.StatusFinished,
			allowErr:    devshard.ErrValidationLeaseAbandoned,
			wantRelease: 1,
			wantAllow:   1,
		},
		{
			name:      "mark submitted failed after mempool add",
			status:    types.StatusFinished,
			markErr:   errors.New("db unavailable"),
			wantAllow: 1,
			wantMark:  1,
			wantVal:   true,
		},
		{
			name:      "success finished does not release",
			status:    types.StatusFinished,
			wantAllow: 1,
			wantMark:  1,
			wantVal:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingLeaseRecorder{allowErr: tt.allowErr, markErr: tt.markErr}
			validator := tt.validator
			h, hosts, user := newLeaseReleaseHost(t, &validator, rec)
			if !tt.skipApply {
				applyInferenceTo(t, h, hosts, user, tt.status)
			}
			if tt.failSign {
				h.signer = errorSigner{addr: hosts[0].Address(), err: signFail}
			}

			h.validateAsync(context.Background(), testValidateJob())

			allow, mark, release := rec.counts()
			require.Equal(t, tt.wantRelease, release)
			require.Equal(t, tt.wantAllow, allow)
			require.Equal(t, tt.wantMark, mark)
			require.Equal(t, tt.wantVal, mempoolHasValidation(h, 1))
			require.Equal(t, tt.wantVote, mempoolHasVote(h, 1))
		})
	}
}

func TestHost_ChallengedInferencePublishesValidationVote(t *testing.T) {
	rec := &recordingLeaseRecorder{}
	valEngine := &trackingValidationEngine{valid: true}
	h, hosts, user := newLeaseReleaseHost(t, valEngine, rec)
	h.Start()
	t.Cleanup(h.Close)

	applyInferenceTo(t, h, hosts, user, types.StatusChallenged)

	require.Eventually(t, func() bool {
		return mempoolHasVote(h, 1)
	}, 2*time.Second, 10*time.Millisecond, "MsgValidationVote should be in mempool")
	require.False(t, mempoolHasValidation(h, 1), "challenged inference must not publish MsgValidation")
	_, mark, release := rec.counts()
	require.Equal(t, 0, release)
	require.Equal(t, 1, mark)
}
