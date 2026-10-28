package registry

import (
	"context"
	"crypto/sha256"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
	"devshard/user"
)

type servedVerdict struct {
	escrowID, model string
	nonce           uint64
	verdict         user.ServedBinding
}

// Test flow:
//  1. Publish an escrow with a served-binding observer and run one inference to its Finish on a real host.
//  2. Bind a stream that hashes to neither of the Finish's hashes, then let the Finish apply.
//  3. Assert the observer hears a mismatch for that nonce, named with the escrow and its model.
func TestTheRegistryHandsAServedStreamVerdictToItsObserver(t *testing.T) {
	var heard []servedVerdict
	escrow := publishLongResponseEscrow(t, func(escrowID, model, _ string, nonce uint64, verdict user.ServedBinding) {
		heard = append(heard, servedVerdict{escrowID: escrowID, model: model, nonce: nonce, verdict: verdict})
	})
	prepared, err := escrow.session.PrepareInferenceFn(func(user.HostBinding) (user.InferenceParams, bool, context.Context, error) {
		return user.InferenceParams{Model: "llama", Prompt: testutil.TestPrompt, InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000}, false, nil, nil
	})
	require.NoError(t, err)
	reply, err := escrow.session.SendOnly(context.Background(), prepared, io.Discard, nil)
	require.NoError(t, err)
	require.NoError(t, escrow.session.ProcessResponse(prepared.HostIdx(), reply, prepared.Nonce()))

	escrow.session.BindReceivedStream(prepared.Nonce(), [][32]byte{sha256.Sum256([]byte("tampered"))})
	require.NoError(t, escrow.session.SendPendingDiff(context.Background()))

	require.Equal(t, []servedVerdict{{escrowID: longResponseEscrowID, model: "llama", nonce: prepared.Nonce(), verdict: user.ServedBindingMismatch}}, heard,
		"the observer must hear the mismatch once, for the escrow's own model")
}
