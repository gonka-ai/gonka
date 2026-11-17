package host

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/state"
	"devshard/types"
)

func TestVerifyRefused_FutureConfirmVotesForTimeout(t *testing.T) {
	wall := time.UnixMilli(1_700_000_000_000)
	g := newEvidenceGroup(t, state.WithStampWallClock(func() time.Time { return wall }))
	st := stateWithPendingFull(1, 1)
	rec := st.Inferences[1]
	future := wall.Add(10 * time.Minute).UnixMilli()
	sig := testutil.SignExecutorReceipt(t, g.signers[1], st.EscrowID, 1, rec.PromptHash, rec.Model, rec.InputLength, rec.MaxTokens,
		testutil.ReceiptStamp{Height: 10, Hash: []byte{0xab}, TimeMs: future})
	confirm := &types.DevshardTx{Tx: &types.DevshardTx_ConfirmStart{ConfirmStart: &types.MsgConfirmStart{
		InferenceId:       1,
		ExecutorSig:       sig,
		ObservedHeight:    10,
		ObservedBlockHash: []byte{0xab},
		ObservedTimeMs:    future,
	}}}

	require.ErrorIs(t, g.sm.CheckEvidence(rec, confirm), types.ErrStampTimeInFuture)

	accept, err := VerifyRefusedTimeout(context.Background(), st, 1, rec, testPayload(), []*types.DevshardTx{confirm}, nil, nil, due(g.sm))
	require.NoError(t, err)
	require.True(t, accept, "a confirm dated ahead of the verifier clock is not evidence, so the timeout stands")
}
