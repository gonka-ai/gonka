package host

import (
	"context"
	"testing"

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
			h, hosts, user := newTwoHostValidationHost(t, validator)
			applyInferenceTo(t, h, hosts, user, types.StatusFinished)
			// Collect a valid job without starting workers, then change state while it waits.
			h.validationQueue = make(chan validateJob, 1)
			jobs := collectValidationJobsLocked(h)
			require.Len(t, jobs, 1)
			h.enqueueValidation(jobs[0])

			snapshot := h.sm.SnapshotState()
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
			require.NoError(t, h.sm.RestoreState(&snapshot))
			switch tc.mempool {
			case "validation", "other":
				slot := uint32(0)
				if tc.mempool == "other" {
					slot = 1
				}
				h.AddTx(&types.DevshardTx{Tx: &types.DevshardTx_Validation{Validation: &types.MsgValidation{InferenceId: 1, ValidatorSlot: slot, Valid: true}}})
			case "vote":
				h.AddTx(&types.DevshardTx{Tx: &types.DevshardTx_ValidationVote{ValidationVote: &types.MsgValidationVote{InferenceId: 1, VoterSlot: 0}}})
			}
			before := len(h.MempoolTxs())
			h.validateAsync(context.Background(), <-h.validationQueue)
			require.Len(t, validator.getCalls(), tc.wantCalls)
			require.NotContains(t, h.validating, uint64(1), "dequeued jobs must clear their in-flight marker")
			require.Empty(t, h.validationCooldown, "obsolete work needs no retry cooldown")
			require.Len(t, h.MempoolTxs(), before+tc.wantCalls)
		})
	}
}
