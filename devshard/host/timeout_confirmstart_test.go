package host

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/types"
)

// verifyingSink is a TxSink that also verifies ConfirmStart, like *Host.
type verifyingSink struct {
	*Mempool
	valid map[string]bool // ExecutorSig -> verifies
}

func (v verifyingSink) VerifyConfirmStart(msg *types.MsgConfirmStart) error {
	if v.valid[string(msg.ExecutorSig)] {
		return nil
	}
	return errors.New("bad executor sig")
}

// A refusing executor answers ChallengeReceipt with one arbitrary byte and an
// empty mempool. If that vetoed the REFUSED timeout, the record would stay
// Pending and settleLiveRecordLocked would credit ReservedCost to the executor
// with Missed=0 (see TestDrainSettle_PendingCreditsHost).
func TestVerifyRefused_JunkReceiptAcceptsTimeout(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte{0x00}}
	sink := verifyingSink{Mempool: NewMempool()}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "a receipt without a verifiable ConfirmStart must not veto the timeout")
}

func TestVerifyRefused_ForgedConfirmStartAcceptsTimeout(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{
		challengeReceipt: []byte("receipt-sig"),
		challengeMempool: []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: []byte("forged"), ConfirmedAt: 1000,
		}}}},
	}
	sink := verifyingSink{Mempool: NewMempool()}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept)
	require.Nil(t, findMempoolConfirm(sink.Txs()), "unverified ConfirmStart must not enter the verifier pool")
}

func TestVerifyRefused_ValidConfirmStartRejectsTimeout(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{
		challengeReceipt: []byte("receipt-sig"),
		challengeMempool: []*types.DevshardTx{{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
			InferenceId: 1, ExecutorSig: []byte("receipt-sig"), ConfirmedAt: 1000,
		}}}},
	}
	sink := verifyingSink{Mempool: NewMempool(), valid: map[string]bool{"receipt-sig": true}}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept, "honest executor: ConfirmStart verifies, timeout rejected")
	require.NotNil(t, findMempoolConfirm(sink.Txs()))
}

func TestVerifyRefused_CopiesOnlyVerifiedConfirmStart(t *testing.T) {
	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{
		challengeReceipt: []byte("receipt-sig"),
		challengeMempool: []*types.DevshardTx{
			{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("forged"), ConfirmedAt: 1}}},
			{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte("receipt-sig"), ConfirmedAt: 1000}}},
		},
	}
	sink := verifyingSink{Mempool: NewMempool(), valid: map[string]bool{"receipt-sig": true}}

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept)
	for _, tx := range sink.Txs() {
		if cs := tx.GetConfirmStart(); cs != nil {
			require.Equal(t, []byte("receipt-sig"), cs.ExecutorSig, "unverified ConfirmStart copied")
		}
	}
}

// The executor's Finish for this inference is still copied next to a verified
// ConfirmStart: the user may have lost the executor and needs both to move the
// record to Finished. Without a verified ConfirmStart nothing is copied.
func TestVerifyRefused_KeepsFinishNextToVerifiedConfirmStart(t *testing.T) {
	finish := &types.DevshardTx{Tx: &types.DevshardTx_FinishInference{FinishInference: &types.MsgFinishInference{InferenceId: 1}}}
	cs := func(sig string) *types.DevshardTx {
		return &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{InferenceId: 1, ExecutorSig: []byte(sig), ConfirmedAt: 1000}}}
	}
	hasFinish := func(txs []*types.DevshardTx) bool {
		for _, tx := range txs {
			if tx.GetFinishInference() != nil {
				return true
			}
		}
		return false
	}

	st := stateWithPendingFull(1, 1)
	executor := &mockExecutorClient{challengeReceipt: []byte("receipt-sig"), challengeMempool: []*types.DevshardTx{cs("receipt-sig"), finish}}
	sink := verifyingSink{Mempool: NewMempool(), valid: map[string]bool{"receipt-sig": true}}
	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.False(t, accept)
	require.NotNil(t, findMempoolConfirm(sink.Txs()))
	require.True(t, hasFinish(sink.Txs()), "Finish must be copied with a verified ConfirmStart")

	st = stateWithPendingFull(1, 1)
	executor = &mockExecutorClient{challengeReceipt: []byte("receipt-sig"), challengeMempool: []*types.DevshardTx{cs("forged"), finish}}
	sink = verifyingSink{Mempool: NewMempool()}
	accept, err = VerifyRefusedTimeout(context.Background(), st, 1, testPayload(), nil, nil, executor, sink, st.Config, deadlinePassedRefused(st, 1))
	require.NoError(t, err)
	require.True(t, accept, "Finish without a verified ConfirmStart must not veto")
	require.Empty(t, sink.Txs())
}
