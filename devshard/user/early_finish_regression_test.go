package user

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"
)

const (
	earlyFinishInputTokens  = 80
	earlyFinishOutputTokens = 40
	earlyFinishActualCost   = earlyFinishInputTokens + earlyFinishOutputTokens
	earlyFinishBalance      = 100000
)

func meteredFinishTx(t *testing.T, signer signing.Signer, inferenceID uint64, slot uint32, escrowID string) *types.DevshardTx {
	t.Helper()
	msg := &types.MsgFinishInference{
		InferenceId: inferenceID, EscrowId: escrowID, ExecutorSlot: slot,
		ResponseHash: testutil.TestResponseHash, ServedHash: testutil.TestServedHash,
		InputTokens: earlyFinishInputTokens, OutputTokens: earlyFinishOutputTokens,
	}
	msg.ProposerSig = testutil.SignProposerTx(t, signer, msg)
	return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: msg}}
}

type earlyFinishFixture struct {
	session *Session
	signers []*signing.Secp256k1Signer
	nonce   uint64
	rec     *types.InferenceRecord
	execIdx int
	payload *host.InferencePayload
}

func newEarlyFinishFixture(t *testing.T, numHosts int) *earlyFinishFixture {
	t.Helper()
	prev := TimeoutBuffer
	TimeoutBuffer = 0
	t.Cleanup(func() { TimeoutBuffer = prev })

	session, signers, _ := setupSession(t, numHosts, earlyFinishBalance, 100)
	params := InferenceParams{
		Model: "llama", Prompt: testutil.TestPrompt,
		InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000,
	}
	prepared, err := session.PrepareInference(context.Background(), params)
	require.NoError(t, err)
	nonce := prepared.diff.Nonce
	rec, ok := session.StateMachine().Inference(nonce)
	require.True(t, ok)
	require.Greater(t, rec.ReservedCost, uint64(earlyFinishActualCost))
	return &earlyFinishFixture{
		session: session,
		signers: signers,
		nonce:   nonce,
		rec:     rec,
		execIdx: int(rec.ExecutorSlot),
		payload: &host.InferencePayload{
			Prompt: params.Prompt, Model: params.Model, InputLength: params.InputLength,
			MaxTokens: params.MaxTokens, StartedAt: params.StartedAt,
		},
	}
}

func (f *earlyFinishFixture) finish(t *testing.T) *types.DevshardTx {
	return meteredFinishTx(t, f.signers[f.execIdx], f.nonce, f.rec.ExecutorSlot, "escrow-1")
}

func (f *earlyFinishFixture) confirm(t *testing.T, confirmedAt int64) *types.DevshardTx {
	return confirmTxFor(t, f.signers[f.execIdx], f.nonce, f.rec, confirmedAt)
}

func (f *earlyFinishFixture) record() types.InferenceRecord {
	return *f.session.StateMachine().SnapshotState().Inferences[f.nonce]
}

func (f *earlyFinishFixture) executorStats() types.HostStats {
	st := f.session.StateMachine().SnapshotState()
	if hs := st.HostStats[f.rec.ExecutorSlot]; hs != nil {
		return *hs
	}
	return types.HostStats{}
}

func (f *earlyFinishFixture) balance() uint64 {
	return f.session.StateMachine().SnapshotState().Balance
}

func (f *earlyFinishFixture) finishKeyApplied() bool {
	f.session.mu.Lock()
	defer f.session.mu.Unlock()
	_, ok := f.session.appliedTxKeys[fmt.Sprintf("finish:%d", f.nonce)]
	return ok
}

func (f *earlyFinishFixture) sendEarlyFinish(t *testing.T) {
	t.Helper()
	require.NoError(t, f.session.ProcessResponse(f.execIdx, &host.HostResponse{
		Mempool: []*types.DevshardTx{f.finish(t)},
	}, f.nonce))
	require.False(t, f.session.IsNonceFinished(f.nonce))
	require.NoError(t, f.session.SendPendingDiff(context.Background()))
	require.Equal(t, types.StatusPending, f.record().Status)
	require.False(t, f.finishKeyApplied())
	require.Nil(t, findRecoveryFinish(f.session.PendingTxs(), f.nonce))
}

func (f *earlyFinishFixture) voters(reject map[int][]*types.DevshardTx) *atomic.Int32 {
	var calls atomic.Int32
	for i, c := range f.session.clients {
		if wrapped, ok := c.(*timeoutVoteClient); ok {
			c = wrapped.HostClient
		}
		v := &mockTimeoutVerifier{
			accept: true, signer: f.signers[i], group: f.session.group, slotIdx: i,
			onVerify: func() { calls.Add(1) },
		}
		if txs, ok := reject[i]; ok {
			v.accept = false
			v.mempool = txs
		}
		f.session.clients[i] = &timeoutVoteClient{HostClient: c, mockTimeoutVerifier: v}
	}
	return &calls
}

func (f *earlyFinishFixture) handleTimeout(t *testing.T) (TimeoutResult, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return f.session.HandleTimeout(ctx, f.nonce, time.Unix(0, 0), f.payload)
}

func TestEarlyFinishWithoutReceiptRefundsReservation(t *testing.T) {
	f := newEarlyFinishFixture(t, 3)
	require.Equal(t, earlyFinishBalance-f.rec.ReservedCost, f.balance())

	f.sendEarlyFinish(t)

	f.voters(nil)
	result, err := f.handleTimeout(t)
	require.ErrorContains(t, err, "timed out")
	require.Equal(t, "refused", result.Reason)
	require.True(t, result.Applied, "%+v", result)

	require.Equal(t, types.StatusTimedOut, f.record().Status)
	require.Equal(t, uint64(earlyFinishBalance), f.balance())
	stats := f.executorStats()
	require.Zero(t, stats.Cost)
	require.Equal(t, uint32(1), stats.Missed)
}

func TestEarlyFinishThenReceiptPaysActualCost(t *testing.T) {
	f := newEarlyFinishFixture(t, 3)
	f.sendEarlyFinish(t)

	var diffs []types.Diff
	f.session.SetDiffObserver(func(d types.Diff) { diffs = append(diffs, d) })

	require.NoError(t, f.session.ProcessResponse(f.execIdx, &host.HostResponse{
		Receipt:     f.confirm(t, 1).GetConfirmStart().ExecutorSig,
		ConfirmedAt: 1,
		Mempool:     []*types.DevshardTx{f.finish(t)},
	}, f.nonce))
	require.True(t, f.session.IsNonceFinished(f.nonce))
	require.NoError(t, f.session.SendPendingDiff(context.Background()))

	require.Len(t, diffs, 1)
	require.Equal(t, 1, countRecoveryConfirmStart(diffs[0].Txs, f.nonce))
	require.Equal(t, 1, countRecoveryFinish(diffs[0].Txs, f.nonce))
	require.True(t, f.finishKeyApplied())

	rec := f.record()
	require.Equal(t, types.StatusFinished, rec.Status)
	require.Equal(t, uint64(earlyFinishActualCost), rec.ActualCost)
	require.Equal(t, uint64(earlyFinishActualCost), f.executorStats().Cost)
	require.Equal(t, uint64(earlyFinishBalance-earlyFinishActualCost), f.balance())

	require.NoError(t, f.session.Finalize(context.Background()))
	require.Equal(t, uint64(earlyFinishActualCost), f.executorStats().Cost)
	require.Equal(t, uint64(earlyFinishBalance-earlyFinishActualCost), f.balance())
}

func TestChallengeRecoveredFinishIsSequencedAtActualCost(t *testing.T) {
	for _, tc := range []struct {
		name    string
		reason  types.TimeoutReason
		started bool
	}{
		{name: "refused", reason: types.TimeoutReason_TIMEOUT_REASON_REFUSED},
		{name: "execution", reason: types.TimeoutReason_TIMEOUT_REASON_EXECUTION, started: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEarlyFinishFixture(t, 3)
			f.sendEarlyFinish(t)

			recovery := []*types.DevshardTx{f.finish(t)}
			if tc.started {
				require.NoError(t, f.session.ProcessResponse(f.execIdx, &host.HostResponse{
					Receipt:     f.confirm(t, 1).GetConfirmStart().ExecutorSig,
					ConfirmedAt: 1,
				}, f.nonce))
				require.NoError(t, f.session.SendPendingDiff(context.Background()))
				require.Equal(t, types.StatusStarted, f.record().Status)
				require.False(t, f.session.IsNonceFinished(f.nonce))
			} else {
				recovery = append([]*types.DevshardTx{f.confirm(t, 1)}, recovery...)
			}

			reject := make(map[int][]*types.DevshardTx, len(f.session.clients))
			for i := range f.session.clients {
				reject[i] = recovery
			}
			f.voters(reject)

			result, err := f.handleTimeout(t)
			require.NoError(t, err)
			require.Equal(t, tc.name, result.Reason)
			require.False(t, result.Applied, "%+v", result)

			rec := f.record()
			require.Equal(t, types.StatusFinished, rec.Status)
			require.Equal(t, uint64(earlyFinishActualCost), rec.ActualCost)
			stats := f.executorStats()
			require.Equal(t, uint64(earlyFinishActualCost), stats.Cost)
			require.Zero(t, stats.Missed)
			require.Equal(t, uint64(earlyFinishBalance-earlyFinishActualCost), f.balance())
		})
	}
}

func TestChallengeRecoveredUnapplicableFinishDoesNotVeto(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(f *earlyFinishFixture, t *testing.T) *types.DevshardTx
	}{
		{name: "id_only", build: func(f *earlyFinishFixture, _ *testing.T) *types.DevshardTx {
			return &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{
				InferenceId: f.nonce,
			}}}
		}},
		{name: "wrong_slot", build: func(f *earlyFinishFixture, t *testing.T) *types.DevshardTx {
			other := (f.execIdx + 1) % len(f.signers)
			return meteredFinishTx(t, f.signers[other], f.nonce, f.session.group[other].SlotID, "escrow-1")
		}},
		{name: "wrong_escrow", build: func(f *earlyFinishFixture, t *testing.T) *types.DevshardTx {
			return meteredFinishTx(t, f.signers[f.execIdx], f.nonce, f.rec.ExecutorSlot, "escrow-2")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newEarlyFinishFixture(t, 3)
			require.NoError(t, f.session.ProcessResponse(f.execIdx, &host.HostResponse{
				Receipt:     f.confirm(t, 1).GetConfirmStart().ExecutorSig,
				ConfirmedAt: 1,
			}, f.nonce))
			require.NoError(t, f.session.SendPendingDiff(context.Background()))
			require.Equal(t, types.StatusStarted, f.record().Status)

			bad := tc.build(f, t)
			require.NoError(t, f.session.ProcessResponse(f.execIdx, &host.HostResponse{
				Mempool: []*types.DevshardTx{bad},
			}, f.nonce))
			require.False(t, f.session.IsNonceFinished(f.nonce))
			require.Nil(t, findRecoveryFinish(f.session.PendingTxs(), f.nonce))

			var diffs []types.Diff
			f.session.SetDiffObserver(func(d types.Diff) { diffs = append(diffs, d) })

			reject := make(map[int][]*types.DevshardTx, len(f.session.clients))
			for i := range f.session.clients {
				if i != f.execIdx {
					reject[i] = []*types.DevshardTx{bad}
				}
			}
			calls := f.voters(reject)
			result, err := f.handleTimeout(t)
			require.ErrorContains(t, err, "insufficient votes")
			require.Equal(t, "execution", result.Reason)
			require.Equal(t, "insufficient_votes", result.Outcome)
			require.False(t, result.Applied, "%+v", result)
			require.Equal(t, int32(len(reject)), calls.Load())

			require.False(t, f.session.IsNonceFinished(f.nonce))
			require.Nil(t, findRecoveryFinish(f.session.PendingTxs(), f.nonce))
			require.Equal(t, types.StatusStarted, f.record().Status)
			require.Zero(t, f.executorStats().Cost)

			calls = f.voters(nil)
			result, err = f.handleTimeout(t)
			require.ErrorContains(t, err, "timed out")
			require.Equal(t, "execution", result.Reason)
			require.True(t, result.Applied, "%+v", result)
			require.Equal(t, int32(len(reject)), calls.Load())

			for _, d := range diffs {
				require.Zero(t, countRecoveryFinish(d.Txs, f.nonce))
			}
			require.Equal(t, types.StatusTimedOut, f.record().Status)
			stats := f.executorStats()
			require.Zero(t, stats.Cost)
			require.Equal(t, uint32(1), stats.Missed)
			require.Equal(t, uint64(earlyFinishBalance), f.balance())
		})
	}
}
