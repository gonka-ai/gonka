package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"common/completionapi"
	"devshard/internal/testutil"
	"devshard/types"
)

var refusedWithLogprobsEvents = []string{
	`data: {"id":"x","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant"},"logprobs":null}]}`,
	`data: {"error":{"code":400,"message":"context length exceeded","type":"BadRequestError"},"id":"x"}`,
	`data: [DONE]`,
}

// Test flow:
//  1. Process a refused stream with the optimization on for a caller that did not ask for logprobs, so the wire loses the logprobs key.
//  2. Sign a Finish carrying both the stored and the served hash.
//  3. Verify the error miss with the envelope the gateway rebuilt from the wire.
//  4. Assert it is accepted and the vote still binds the stored hash the state machine checks.
func TestVerifyErrorMiss_AcceptsTheServedViewAndVotesOnTheStoredHash(t *testing.T) {
	environment := newErrorTimeoutEnv(t)
	processor := completionapi.NewExecutorResponseProcessor("devshard-1-1", false)
	processor.SetLogprobsOptimization(nil, true)
	var received []string
	for _, event := range refusedWithLogprobsEvents {
		forwarded, err := processor.ProcessStreamedResponse(event)
		require.NoError(t, err)
		received = append(received, forwarded)
	}
	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	servedHash, err := processor.GetServedHash()
	require.NoError(t, err)
	gatewayPayload := streamedPayload(t, received)
	require.NotEqual(t, payloadSHA256(stored), payloadSHA256(gatewayPayload), "the wire lost the logprobs key")

	finish := &types.MsgFinishInference{
		InferenceId: 1, ResponseHash: payloadSHA256(stored), ServedHash: servedHash[:],
		ExecutorSlot: 1, EscrowId: "escrow-1",
	}
	finish.ProposerSig = testutil.SignProposerTx(t, environment.hosts[1], finish)

	accept, votedHash, cause, err := VerifyErrorMiss(environment.st, 1, marshalFinishTx(t, finish), gatewayPayload, nil, environment.sm)
	require.NoError(t, err)
	require.True(t, accept, "reject cause: %s", cause)
	require.Equal(t, finish.ResponseHash, votedHash, "the vote still binds the stored hash the state machine checks")
}
