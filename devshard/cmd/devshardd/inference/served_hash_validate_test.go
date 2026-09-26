package inference

import (
	"crypto/sha256"
	"strings"
	"testing"

	"common/completionapi"
	commonvalidation "common/validation"
	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
)

func validateRequestFor(t *testing.T, prompt, stored []byte) devshardpkg.ValidateRequest {
	t.Helper()
	stripped, err := completionapi.StripForGateway(stored)
	require.NoError(t, err)
	promptHash := sha256.Sum256(prompt)
	responseHash := sha256.Sum256(stored)
	servedHash := sha256.Sum256(stripped)
	return devshardpkg.ValidateRequest{PromptHash: promptHash[:], ResponseHash: responseHash[:], ServedHash: servedHash[:]}
}

// Test flow:
//  1. Execute an inference and build the validate request from its prompt, stored payload and served view.
//  2. Apply the case's mutation: a served hash of another answer, no served hash, or a response hash of another payload.
//  3. Assert the untouched request passes and every mutation fails as an executor payload hash mismatch.
func TestFetchedPayloadHashes(t *testing.T) {
	prompt := []byte(streamingPrompt)
	stored := runServedInference(t, streamingPrompt, "text/event-stream", true, answeredChunks...).stored
	servedAnotherAnswer := runServedInference(t, streamingPrompt, "text/event-stream", true,
		strings.Replace(answeredChunks[0], `"content":"Hi"`, `"content":"Ho"`, 1), answeredChunks[1], answeredChunks[2]).stored

	for _, testCase := range []struct {
		name      string
		mutate    func(*devshardpkg.ValidateRequest)
		wantError bool
	}{
		{name: "both hashes bind the stored payload"},
		{name: "the served hash is not a strip of the stored payload", wantError: true, mutate: func(request *devshardpkg.ValidateRequest) {
			request.ServedHash = validateRequestFor(t, prompt, servedAnotherAnswer).ServedHash
		}},
		{name: "the finish carries no served hash", wantError: true, mutate: func(request *devshardpkg.ValidateRequest) {
			request.ServedHash = nil
		}},
		{name: "the stored payload is not the one finished", wantError: true, mutate: func(request *devshardpkg.ValidateRequest) {
			request.ResponseHash = validateRequestFor(t, prompt, servedAnotherAnswer).ResponseHash
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := validateRequestFor(t, prompt, stored)
			if testCase.mutate != nil {
				testCase.mutate(&request)
			}
			err := verifyFetchedPayloadHashes(request, prompt, stored)
			if !testCase.wantError {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, commonvalidation.ErrHashMismatch)
			require.ErrorIs(t, err, errExecutorPayloadFault)
		})
	}
}

// Test flow:
//  1. Execute a streamed inference with the case's logprobs request and optimization setting, keeping the prompt, the stored payload and the hashes the executor signed.
//  2. Hand the validator the executor's own response_hash and served_hash, not ones recomputed from the payload.
//  3. Assert both hash checks pass, so an unstripped stored payload still re-derives the served_hash its executor signed.
func TestFetchedPayloadHashesPassWhateverTheExecutorForwarded(t *testing.T) {
	for _, testCase := range []struct {
		name                string
		prompt              string
		optimizationEnabled bool
	}{
		{name: "optimization off, gateway did not ask for logprobs", prompt: streamingPrompt},
		{name: "optimization off, gateway asked for logprobs", prompt: streamingPromptAskingLogprobs},
		{name: "optimization on, gateway did not ask for logprobs", prompt: streamingPrompt, optimizationEnabled: true},
		{name: "optimization on, gateway asked for logprobs", prompt: streamingPromptAskingLogprobs, optimizationEnabled: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			inference := runServedInference(t, testCase.prompt, "text/event-stream", testCase.optimizationEnabled, answeredChunks...)
			promptHash := sha256.Sum256([]byte(testCase.prompt))
			request := devshardpkg.ValidateRequest{
				PromptHash:   promptHash[:],
				ResponseHash: inference.result.ResponseHash,
				ServedHash:   inference.result.ServedHash,
			}

			require.NoError(t, verifyFetchedPayloadHashes(request, []byte(testCase.prompt), inference.stored))
		})
	}
}
