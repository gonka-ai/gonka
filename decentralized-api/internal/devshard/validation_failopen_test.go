package devshard

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"devshard/bridge"
	devshardpkg "devshard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression tests for the two devshard validation fail-opens in
// EvaluateValidationResponse. Each asserts the SECURE (post-fix) behavior and
// fails against the unpatched code:
//   - empty logprobs in the executor's stored response must be rejected
//     (unpatched: CompareLogits([],x) == 1.0 similarity -> Valid:true)
//   - a 4xx from the validator's own re-execution must be rejected
//     (unpatched: returned Valid:true before any content check)
// A legitimate matching response must still validate (no false-reject regression).
func newResolver(req devshardpkg.ValidateRequest) *ValidationThresholdResolver {
	return cachedThresholdResolver(req, &bridge.Decimal{Value: 90, Exponent: -2}) // 0.90
}

func okResp(body string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}

const logprobBody = `{"id":"v","object":"chat.completion","model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"hello world"},"logprobs":{"content":[{"token":"hello","logprob":-0.10,"top_logprobs":[{"token":"hello","logprob":-0.10}]},{"token":"world","logprob":-0.05,"top_logprobs":[{"token":"world","logprob":-0.05}]}]},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":200,"total_tokens":250}}`

const emptyLogprobBody = `{"id":"o","object":"chat.completion","model":"model-a","choices":[{"index":0,"message":{"role":"assistant","content":"hello world"},"logprobs":{"content":[]},"finish_reason":"stop"}],"usage":{"prompt_tokens":50,"completion_tokens":200,"total_tokens":250}}`

func TestEvaluateValidationResponse_EmptyOriginalLogits_IsInvalid(t *testing.T) {
	req := devshardpkg.ValidateRequest{EscrowID: "e1", EpochID: 7, Model: "model-a"}
	res, err := EvaluateValidationResponse(context.Background(), okResp(logprobBody), req, "id1", "[t]", []byte(emptyLogprobBody), newResolver(req))
	require.NoError(t, err)
	assert.False(t, res.Valid, "executor response with no logprobs must be rejected, not auto-passed")
}

func TestEvaluateValidationResponse_4xxReExec_IsInvalid(t *testing.T) {
	req := devshardpkg.ValidateRequest{EscrowID: "e2", EpochID: 7, Model: "model-a"}
	for _, status := range []int{http.StatusBadRequest, http.StatusUnprocessableEntity} {
		resp := &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"rejected"}`)), Header: make(http.Header)}
		res, err := EvaluateValidationResponse(context.Background(), resp, req, "id2", "[t]", []byte(logprobBody), newResolver(req))
		require.NoError(t, err)
		assert.False(t, res.Valid, "validator re-exec %d must not auto-approve", status)
	}
}

func TestEvaluateValidationResponse_MatchingLogits_StillValid(t *testing.T) {
	// No-regression: a genuine response with matching logprobs must still pass.
	req := devshardpkg.ValidateRequest{EscrowID: "e3", EpochID: 7, Model: "model-a"}
	res, err := EvaluateValidationResponse(context.Background(), okResp(logprobBody), req, "id3", "[t]", []byte(logprobBody), newResolver(req))
	require.NoError(t, err)
	assert.True(t, res.Valid, "legitimate matching response must remain valid after the fix")
}

func TestEvaluateValidationResponse_BothEmpty_StaysValid(t *testing.T) {
	// No-regression for legitimate reasoning-burn empties (Kimi-K2.6, see PR #1233):
	// when the validator's own re-execution ALSO yields no logprobs, both-empty is
	// a genuine match and must not be rejected. The attacker cannot force this case
	// because they don't control the validator's independent re-execution.
	req := devshardpkg.ValidateRequest{EscrowID: "e4", EpochID: 7, Model: "model-a"}
	res, err := EvaluateValidationResponse(context.Background(), okResp(emptyLogprobBody), req, "id4", "[t]", []byte(emptyLogprobBody), newResolver(req))
	require.NoError(t, err)
	assert.True(t, res.Valid, "legitimate both-empty (reasoning-burn) must remain valid")
}
