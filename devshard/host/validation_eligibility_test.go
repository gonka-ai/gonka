package host

import (
	"context"
	"testing"

	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
)

func TestHost_QueuedValidationRechecksEligibility(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		status                            types.InferenceStatus
		absent, participated, ownExecutor bool
		mempool                           string
		wantCalls                         int
	}{
		{name: "still finished", status: types.StatusFinished, wantCalls: 1},
		{name: "became challenged", status: types.StatusChallenged, wantCalls: 1},
		{name: "absent", absent: true},
		{name: "pending", status: types.StatusPending},
		{name: "started", status: types.StatusStarted},
		{name: "validated", status: types.StatusValidated},
		{name: "invalidated", status: types.StatusInvalidated},
		{name: "timed out", status: types.StatusTimedOut},
		{name: "already participated", status: types.StatusFinished, participated: true},
		{name: "own execution", status: types.StatusFinished, ownExecutor: true},
		{name: "own validation pending", status: types.StatusFinished, mempool: "validation"},
		{name: "own vote pending", status: types.StatusChallenged, mempool: "vote"},
		{name: "other host validation pending", status: types.StatusFinished, mempool: "other", wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			validator := &trackingValidationEngine{valid: true}
			hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
			user := testutil.MustGenerateKey(t)
			h := newTestHost(t, 0, hosts, user, 100000, 10)
			h.validator = validator
			h.validationQueue = make(chan validateJob, 1)
			h.validating[1] = struct{}{}
			h.validationQueue <- validateJob{inferenceID: 1, validatorSlot: 0, escrowID: "escrow-1", model: "llama"}

			snapshot := h.sm.SnapshotState()
			snapshot.Inferences[1] = &types.InferenceRecord{Status: types.StatusFinished, ExecutorSlot: 1, Model: "llama"}
			if tc.absent {
				delete(snapshot.Inferences, 1)
			} else {
				rec := snapshot.Inferences[1]
				rec.Status = tc.status
				if tc.participated {
					rec.ValidatedBy.Set(0)
				}
				if tc.ownExecutor {
					rec.ExecutorSlot = 0
				}
			}
			h.sm.RestoreState(&snapshot)
			switch tc.mempool {
			case "validation", "other":
				slot := uint32(0)
				if tc.mempool == "other" {
					slot = 1
				}
				h.mempool.AddTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: slot, Valid: true}}})
			case "vote":
				h.mempool.AddTx(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: &types.MsgValidationVote{InferenceId: 1, VoterSlot: 0}}})
			}
			before := len(h.MempoolTxs())
			h.validateAsync(context.Background(), <-h.validationQueue)
			require.Len(t, validator.getCalls(), tc.wantCalls)
			require.NotContains(t, h.validating, uint64(1), "dequeued jobs must clear their in-flight marker")
			require.Len(t, h.MempoolTxs(), before+tc.wantCalls)
		})
	}
}

type v4CreditValidator struct {
	*trackingValidationEngine
	ready bool
}

func (v *v4CreditValidator) CanValidate(string) bool { return v.ready }
func TestV4CreditGateBeforeQueueAndWorker(t *testing.T) {
	hosts := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	h := newTestHost(t, 0, hosts, testutil.MustGenerateKey(t), 100000, 10)
	v := &v4CreditValidator{trackingValidationEngine: &trackingValidationEngine{valid: true}}
	h.validator = v
	h.validationQueue = make(chan validateJob, 10)
	snapshot := h.sm.SnapshotState()
	snapshot.Inferences[1] = &types.InferenceRecord{Status: types.StatusChallenged, ExecutorSlot: 1, Model: "m"}
	h.sm.RestoreState(&snapshot)
	require.Empty(t, h.collectValidationJobs())
	v.ready = true
	jobs := h.collectValidationJobs()
	require.Len(t, jobs, 1)
	v.ready = false
	h.validateAsync(context.Background(), jobs[0])
	require.Empty(t, v.getCalls())
	require.Empty(t, h.validating)
	require.False(t, devshard.CanValidate(v, "m"))
	v.ready = true
	require.Len(t, h.collectValidationJobs(), 1)
}
