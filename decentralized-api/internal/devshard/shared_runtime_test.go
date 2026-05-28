package devshard

import (
	"encoding/json"
	"testing"

	"decentralized-api/completionapi"

	"github.com/stretchr/testify/require"
)

func enforcedOfLen(n int) completionapi.EnforcedTokens {
	return completionapi.EnforcedTokens{Tokens: make([]completionapi.EnforcedToken, n)}
}

func TestRewriteRequest_BumpsMaxTokensBelowHeadroom(t *testing.T) {
	enforced := enforcedOfLen(300)
	req := map[string]interface{}{
		"max_tokens":            float64(100),
		"max_completion_tokens": float64(100),
	}
	rewriteRequest(req, enforced, 0, 0)
	require.EqualValues(t, uint64(600), req["max_tokens"], "max_tokens must be bumped to 2× enforced")
	require.EqualValues(t, uint64(600), req["max_completion_tokens"], "max_completion_tokens must be bumped to 2× enforced")
}

func TestRewriteRequest_PreservesAdequateMaxTokens(t *testing.T) {
	enforced := enforcedOfLen(100)
	req := map[string]interface{}{
		"max_tokens":            float64(500),
		"max_completion_tokens": float64(500),
	}
	rewriteRequest(req, enforced, 0, 0)
	require.EqualValues(t, float64(500), req["max_tokens"], "adequate max_tokens must stay")
	require.EqualValues(t, float64(500), req["max_completion_tokens"], "adequate max_completion_tokens must stay")
}

func TestRewriteRequest_BumpsExactlyAtThreshold(t *testing.T) {
	enforced := enforcedOfLen(250)
	req := map[string]interface{}{
		"max_tokens": float64(500),
	}
	rewriteRequest(req, enforced, 0, 0)
	require.EqualValues(t, float64(500), req["max_tokens"], "equal headroom must stay (strict greater check)")
}

func TestRewriteRequest_IgnoresMissingFields(t *testing.T) {
	enforced := enforcedOfLen(50)
	req := map[string]interface{}{}
	rewriteRequest(req, enforced, 0, 0)
	_, hasMax := req["max_tokens"]
	_, hasMaxCompletion := req["max_completion_tokens"]
	require.False(t, hasMax, "must not invent max_tokens")
	require.False(t, hasMaxCompletion, "must not invent max_completion_tokens")
}

func TestRewriteRequest_IgnoresNonNumericValues(t *testing.T) {
	enforced := enforcedOfLen(300)
	req := map[string]interface{}{
		"max_tokens":            "not-a-number",
		"max_completion_tokens": nil,
	}
	rewriteRequest(req, enforced, 0, 0)
	require.Equal(t, "not-a-number", req["max_tokens"], "non-numeric must not be modified")
	require.Nil(t, req["max_completion_tokens"], "nil must not be modified")
}

func TestRewriteRequest_EmptyEnforcedNoOp(t *testing.T) {
	enforced := enforcedOfLen(0)
	req := map[string]interface{}{
		"max_tokens":            float64(100),
		"max_completion_tokens": float64(100),
	}
	rewriteRequest(req, enforced, 0, 0)
	require.EqualValues(t, float64(100), req["max_tokens"], "empty enforced => no bump")
	require.EqualValues(t, float64(100), req["max_completion_tokens"], "empty enforced => no bump")
}

func TestRewriteRequest_ParsedFromJSONFloat64(t *testing.T) {
	// Maps unmarshalled from JSON yield float64 for numeric fields. Verify the
	// helper path through JSONNumericUint64 handles JSON-loaded shapes.
	raw := []byte(`{"max_tokens": 200, "max_completion_tokens": 200}`)
	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &req))
	rewriteRequest(req, enforcedOfLen(300), 0, 0)
	require.EqualValues(t, uint64(600), req["max_tokens"])
	require.EqualValues(t, uint64(600), req["max_completion_tokens"])
}

func TestRewriteRequest_CapsAtContextWindow(t *testing.T) {
	// enforced=4000 wants 8000 target; context_window=10000, input=4000 →
	// cap = 10000 - 4000 - 8 = 5992 < target → uses cap.
	enforced := enforcedOfLen(4000)
	req := map[string]interface{}{
		"max_tokens": float64(5000),
	}
	rewriteRequest(req, enforced, 10000, 4000)
	require.EqualValues(t, uint64(5992), req["max_tokens"], "max_tokens must be capped at context_window - input - margin")
}

func TestRewriteRequest_NoBumpWhenCapBelowOriginal(t *testing.T) {
	// enforced=4000 wants 8000 target; context_window=10000, input=4000 →
	// cap=5992. Original max_tokens is 6000 (already above cap) → no bump.
	enforced := enforcedOfLen(4000)
	req := map[string]interface{}{
		"max_tokens": float64(6000),
	}
	rewriteRequest(req, enforced, 10000, 4000)
	require.EqualValues(t, float64(6000), req["max_tokens"], "must not lower max_tokens below original")
}

func TestRewriteRequest_NoCapWhenContextWindowUnknown(t *testing.T) {
	// contextWindow=0 indicates chain didn't report a value — apply 2× bump as before.
	enforced := enforcedOfLen(4000)
	req := map[string]interface{}{
		"max_tokens": float64(1000),
	}
	rewriteRequest(req, enforced, 0, 999999)
	require.EqualValues(t, uint64(8000), req["max_tokens"], "no cap when context_window=0")
}

func TestRewriteRequest_NoBumpWhenPromptExceedsContextWindow(t *testing.T) {
	// If chain context_window is smaller than the prompt+margin, abandon the
	// bump entirely — vLLM would reject anything > 0 here.
	enforced := enforcedOfLen(100)
	req := map[string]interface{}{
		"max_tokens": float64(50),
	}
	rewriteRequest(req, enforced, 100, 100)
	require.EqualValues(t, float64(50), req["max_tokens"], "no bump when prompt already exhausts context window")
}
