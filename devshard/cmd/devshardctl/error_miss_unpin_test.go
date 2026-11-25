package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
	"devshard/user"
)

// Test flow:
//  1. Send one inference to its executor and keep the reply, which carries the executor's Finish.
//  2. Build an attempt that ended on a terminal error and retained no evidence: for `no_error_proof` its reply keeps the Finish, for `no_finish_artifact` its reply has the Finish taken out.
//  3. Assert `errorMissOutcome` refuses the claim for the reason the case is named after.
//  4. Run `voteTimeoutsForFailedRequest`, which processes the reply, pins the Finish, skips the claim and schedules the ordinary timeout for a deadline still ahead.
//  5. Deliver the Finish as a later reply would, for the case whose own reply lacked it, and send the pending diff.
//  6. Assert the inference is finished and no Finish for it is still queued.
func TestAnErrorAttemptWithoutAClaimReleasesItsPinnedFinish(t *testing.T) {
	for _, testCase := range []struct {
		skipReason     string
		replyHasFinish bool
	}{
		{skipReason: "no_error_proof", replyHasFinish: true},
		{skipReason: "no_finish_artifact", replyHasFinish: false},
	} {
		t.Run(testCase.skipReason, func(t *testing.T) {
			env := setupManualFloorProxy(t, 3, nil, true)
			params := defaultParams()
			params.StartedAt = time.Now().Unix()
			prepared, err := env.session.PrepareInference(context.Background(), params)
			require.NoError(t, err)
			nonce := prepared.Nonce()
			reply, err := env.session.SendOnly(context.Background(), prepared, nil, nil)
			require.NoError(t, err)
			finish, others := splitFinish(reply.Mempool, nonce)
			require.NotNil(t, finish, "the executor's reply must carry its Finish")
			attemptReply := *reply
			if !testCase.replyHasFinish {
				attemptReply.Mempool = others
			}
			done := make(chan struct{})
			close(done)
			redundancy := env.proxy.redundancy
			attempt := &inflight{
				escrowID:      redundancy.devshardID,
				nonce:         nonce,
				hostID:        "error-host",
				hostIdx:       prepared.HostIdx(),
				sendTime:      time.Now(),
				done:          done,
				resp:          &attemptReply,
				errorSource:   "error.InternalServerError",
				errorTerminal: true,
			}
			hasFinish, claimable := errorMissOutcome(attempt, env.session)
			require.Equal(t, testCase.replyHasFinish, hasFinish)
			require.False(t, claimable)

			redundancy.voteTimeoutsForFailedRequest(context.Background(), []*inflight{attempt}, params)
			if !testCase.replyHasFinish {
				laterReply := &host.HostResponse{Mempool: []*types.DevshardTx{finish}}
				require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), laterReply, nonce))
			}
			require.NoError(t, env.session.SendPendingDiff(context.Background()))

			record, tracked := env.sm.Inference(nonce)
			require.True(t, tracked)
			require.Equal(t, types.StatusFinished, record.Status, "the released Finish must be composed into a diff")
			require.Empty(t, env.session.FinishTxFor(nonce), "no Finish may stay queued once it is released")
		})
	}
}

func splitFinish(mempool []*types.DevshardTx, nonce uint64) (finish *types.DevshardTx, others []*types.DevshardTx) {
	for _, tx := range mempool {
		if len(user.MarshalFinishTx([]*types.DevshardTx{tx}, nonce)) > 0 {
			finish = tx
			continue
		}
		others = append(others, tx)
	}
	return finish, others
}
