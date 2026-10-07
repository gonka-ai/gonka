package host

import (
	"context"
	"devshard"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
	"github.com/stretchr/testify/require"
	"testing"
	"time"
)

type epochValidator struct {
	current uint64
	calls   int
	run     func()
	err     error
}

func (v *epochValidator) CanValidateEpoch(epoch uint64) bool { return epoch != 0 && epoch == v.current }
func (v *epochValidator) Validate(context.Context, devshard.ValidateRequest) (*devshard.ValidateResult, error) {
	v.calls++
	if v.run != nil {
		v.run()
	}
	return &devshard.ValidateResult{Valid: true}, v.err
}

func TestValidationEpochAtQueueAndPublication(t *testing.T) {
	for _, boundary := range []string{"collection", "dequeue", "publication", "challenged during ML", "conflict cooldown", "conflict after close"} {
		t.Run(boundary, func(t *testing.T) {
			signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
			h := newTestHost(t, 0, signers, testutil.MustGenerateKey(t), 100000, 10)
			h.epochID = 416
			v := &epochValidator{current: 416}
			h.validator = v
			h.validationQueue = make(chan validateJob, 10)
			snapshot := h.sm.SnapshotState()
			snapshot.Inferences[5117] = &types.InferenceRecord{Status: types.StatusChallenged, ExecutorSlot: 1, Model: "m"}
			h.sm.RestoreState(&snapshot)
			if boundary == "collection" {
				v.current = 417
				require.Empty(t, h.collectValidationJobs())
				require.Zero(t, v.calls)
				return
			}
			jobs := h.collectValidationJobs()
			require.Len(t, jobs, 1)
			switch boundary {
			case "dequeue":
				v.current = 417
			case "publication":
				v.run = func() { v.current = 417 }
			case "challenged during ML":
				snapshot.Inferences[5117].Status = types.StatusFinished
				h.sm.RestoreState(&snapshot)
				v.run = func() {
					s := h.sm.SnapshotState()
					s.Inferences[5117].Status = types.StatusChallenged
					h.sm.RestoreState(&s)
				}
			case "conflict cooldown", "conflict after close":
				v.err = devshard.ErrValidationAlreadyLeased
				if boundary == "conflict after close" {
					v.run = h.Close
				}
			}
			h.validateAsync(context.Background(), jobs[0])
			require.Empty(t, h.validating)
			if boundary == "challenged during ML" {
				require.Len(t, h.MempoolTxs(), 1)
				require.NotNil(t, h.MempoolTxs()[0].GetValidationVote())
				return
			}
			require.Empty(t, h.MempoolTxs())
			if boundary == "dequeue" {
				require.Zero(t, v.calls)
			} else {
				require.Equal(t, 1, v.calls)
			}
			if boundary == "conflict after close" {
				require.Empty(t, h.validationRetryAt)
			}
			if boundary == "conflict cooldown" {
				require.Empty(t, h.collectValidationJobs())
				h.validationRetryAt[5117] = time.Now().Add(-time.Second)
				require.Len(t, h.collectValidationJobs(), 1)
			}
		})
	}
}
