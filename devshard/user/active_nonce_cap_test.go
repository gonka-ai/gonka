package user

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/types"
)

func capAtCurrentNonce(session *Session) {
	session.SetChainMaxNonce(uint32(session.Nonce() + types.FinalizeNonceReserve(len(session.group))))
}

// Test flow:
//  1. Tell the session the chain's max_nonce leaves no room for active work past its current nonce.
//  2. Queue a timeout vote and send the pending diff.
//  3. Assert the send is refused with ErrNonceLimitExceeded, no nonce is spent, and the vote is dropped, because every host would refuse a diff carrying it.
func TestSendPendingDiffSpendsNoNonceOnActiveWorkPastTheActiveCap(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	before := session.Nonce()
	session.AddPendingTimeoutTx(1, types.TimeoutReason_TIMEOUT_REASON_REFUSED, nil)

	err := session.SendPendingDiff(context.Background())

	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
	require.Equal(t, before, session.Nonce())
	require.Empty(t, session.PendingTxs())
}

// Test flow:
//  1. Tell the session the chain's max_nonce leaves no room for active work past its current nonce.
//  2. Prepare an inference.
//  3. Assert it is refused with ErrNonceLimitExceeded and no nonce is spent.
func TestPrepareInferenceIsRefusedPastTheActiveCap(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	before := session.Nonce()

	_, err := session.PrepareInference(context.Background(), InferenceParams{
		Prompt:      []byte(`{"messages":[{"role":"user","content":"x"}]}`),
		Model:       "llama",
		InputLength: 1,
		MaxTokens:   testutil.TestMaxTokens,
		StartedAt:   time.Now().Unix(),
	})

	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
	require.Equal(t, before, session.Nonce())
}

// Test flow:
//  1. Tell the session the chain's max_nonce leaves no room for active work past its current nonce, and queue a receipt that carries no active work.
//  2. Send the pending diff.
//  3. Assert no nonce is spent and the receipt stays queued, because past the cap the hosts keep their remaining nonces for finalization.
func TestSendPendingDiffKeepsProtocolTxsForFinalizationPastTheActiveCap(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	before := session.Nonce()
	receipt := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("sig")}}}
	session.mu.Lock()
	session.addPendingTx(receipt)
	session.mu.Unlock()

	err := session.SendPendingDiff(context.Background())

	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
	require.Equal(t, before, session.Nonce())
	require.Equal(t, []*types.DevshardTx{receipt}, session.PendingTxs())
}

// Test flow:
//  1. Tell the session the chain's max_nonce leaves no room for active work past its current nonce.
//  2. Send the finalize round that opens finalization.
//  3. Assert it is composed at the next nonce, because the hosts keep exactly that reserve for the finalize rounds.
func TestTheFinalizeRoundIsComposedPastTheActiveCap(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	before := session.Nonce()

	err := session.sendDiffRound(context.Background(), []*types.DevshardTx{finalizeRoundTx()}, true)

	require.NoError(t, err)
	require.Equal(t, before+1, session.Nonce())
}

// Test flow:
//  1. Open finalization with its first round, then queue a receipt.
//  2. Send the pending diff, which is not a finalize round.
//  3. Assert it is refused without spending a nonce, so nothing but the finalize rounds spends the reserve the hosts keep for them.
func TestOnlyFinalizeRoundsSpendNoncesWhileFinalizing(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	require.NoError(t, session.sendDiffRound(context.Background(), []*types.DevshardTx{finalizeRoundTx()}, true))
	before := session.Nonce()
	session.mu.Lock()
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("sig")}}})
	session.mu.Unlock()

	err := session.SendPendingDiff(context.Background())

	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
	require.Equal(t, before, session.Nonce())
}

// Test flow:
//  1. Prepare an inference, then tell the session the chain's max_nonce leaves no room for active work past its current nonce.
//  2. Vote its refusal timeout.
//  3. Assert the vote is skipped as nonce_cap_reached with no error, before any verifier is asked: no diff could carry it.
func TestATimeoutVotePastTheActiveCapIsSkipped(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	nonce := prepareNonce(t, session)
	capAtCurrentNonce(session)

	result, needsWait, err := session.VoteTimeout(context.Background(), nonce, time.Now().Add(-time.Hour), nil, types.TimeoutReason_TIMEOUT_REASON_REFUSED)

	require.NoError(t, err)
	require.False(t, needsWait)
	require.Equal(t, "nonce_cap_reached", result.DetailReason)
}

func finalizeRoundTx() *types.DevshardTx {
	return &types.DevshardTx{Tx: &types.DevshardTx_FinalizeRound{FinalizeRound: &types.MsgFinalizeRound{}}}
}

// Test flow:
//  1. Tell the session the chain's max_nonce leaves no room for active work past its current nonce, and queue a receipt so a host sync has a diff to send.
//  2. Sync the hosts.
//  3. Assert the sync is refused without spending a nonce, so an admin sync cannot use up the nonces the hosts keep for finalization.
func TestSyncHostsSpendsNoFinalizationReserve(t *testing.T) {
	session, _, _ := setupSession(t, 3, 1_000_000, 0)
	capAtCurrentNonce(session)
	before := session.Nonce()
	session.mu.Lock()
	session.addPendingTx(&types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("sig")}}})
	session.mu.Unlock()

	err := session.SyncHosts(context.Background())

	require.ErrorIs(t, err, types.ErrNonceLimitExceeded)
	require.Equal(t, before, session.Nonce())
}
