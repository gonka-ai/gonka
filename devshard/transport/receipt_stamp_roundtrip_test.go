package transport

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"devshard/host"
	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/types"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Exercise host signing, the real SSE writer and client parser, including the
// early receipt callback before mempool recovery can mask a missing wire field.
func TestSSEReceiptPreservesSignedObservedTime(t *testing.T) {
	client, _, userSigner, _, executor, executorSigner := setupClientTestEnvWithHostSigner(t)
	start := testutil.StartTx(1)
	start.GetStartInference().StartedAt = 1000
	diff := testutil.SignDiff(t, userSigner, "escrow-1", 1, []*types.DevshardTx{start})
	var callback *host.HostResponse
	response, err := client.Send(context.Background(), host.HostRequest{
		Diffs: []types.Diff{diff}, Nonce: 1,
		Payload: &host.InferencePayload{Prompt: testutil.TestPrompt, Model: "llama", InputLength: 100, MaxTokens: testutil.TestMaxTokens, StartedAt: 1000},
	}, nil, func(receipt *host.HostResponse) {
		copy := *receipt
		callback = &copy
	})
	require.NoError(t, err)
	require.NotNil(t, callback)
	require.Empty(t, callback.Mempool, "the immediate receipt must verify before the final mempool arrives")
	var signed *types.MsgConfirmStart
	for _, tx := range response.Mempool {
		if confirm := tx.GetConfirmStart(); confirm != nil && confirm.InferenceId == 1 {
			signed = confirm
		}
	}
	require.NotNil(t, signed)
	require.Positive(t, signed.ObservedTimeMs, "fixture must sign a nonzero header timestamp")
	record := executor.SnapshotState().Inferences[1]
	require.NotNil(t, record)
	for _, receipt := range []*host.HostResponse{callback, response} {
		content := &types.ExecutorReceiptContent{
			InferenceId: 1, PromptHash: record.PromptHash, Model: record.Model,
			InputLength: record.InputLength, MaxTokens: record.MaxTokens, EscrowId: "escrow-1",
			StartedAt: record.StartedAt, ConfirmedAt: receipt.ConfirmedAt,
			ObservedHeight: receipt.ObservedHeight, ObservedBlockHash: receipt.ObservedBlockHash, ObservedTimeMs: receipt.ObservedTimeMs,
		}
		encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(content)
		require.NoError(t, err)
		recovered, err := signing.NewSecp256k1Verifier().RecoverAddress(encoded, receipt.Receipt)
		require.NoError(t, err)
		require.Equal(t, executorSigner.Address(), recovered, "SSE must preserve the exact signed receipt preimage")
		require.Equal(t, signed.ObservedTimeMs, receipt.ObservedTimeMs)
	}
}

func TestSSEReceiptMissingObservedTimeRemainsCompatible(t *testing.T) {
	client := &HTTPClient{config: allowRetiredHTTP(DefaultClientConfig())}
	response, err := client.parseSSEResponse(context.Background(), strings.NewReader(receiptOnlySSE), nil, nil)
	require.NoError(t, err)
	require.NotNil(t, response)
	require.Zero(t, response.ObservedTimeMs)
	encoded, err := json.Marshal(DevshardReceiptEvent{})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "observed_time_ms", "zero timestamps retain the legacy wire shape")
}
