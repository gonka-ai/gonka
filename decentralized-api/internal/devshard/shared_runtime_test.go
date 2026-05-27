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
	rewriteRequest(req, enforced)
	require.EqualValues(t, uint64(600), req["max_tokens"], "max_tokens must be bumped to 2× enforced")
	require.EqualValues(t, uint64(600), req["max_completion_tokens"], "max_completion_tokens must be bumped to 2× enforced")
}

func TestRewriteRequest_PreservesAdequateMaxTokens(t *testing.T) {
	enforced := enforcedOfLen(100)
	req := map[string]interface{}{
		"max_tokens":            float64(500),
		"max_completion_tokens": float64(500),
	}
	rewriteRequest(req, enforced)
	require.EqualValues(t, float64(500), req["max_tokens"], "adequate max_tokens must stay")
	require.EqualValues(t, float64(500), req["max_completion_tokens"], "adequate max_completion_tokens must stay")
}

func TestRewriteRequest_BumpsExactlyAtThreshold(t *testing.T) {
	enforced := enforcedOfLen(250)
	req := map[string]interface{}{
		"max_tokens": float64(500),
	}
	rewriteRequest(req, enforced)
	require.EqualValues(t, float64(500), req["max_tokens"], "equal headroom must stay (strict greater check)")
}

func TestRewriteRequest_IgnoresMissingFields(t *testing.T) {
	enforced := enforcedOfLen(50)
	req := map[string]interface{}{}
	rewriteRequest(req, enforced)
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
	rewriteRequest(req, enforced)
	require.Equal(t, "not-a-number", req["max_tokens"], "non-numeric must not be modified")
	require.Nil(t, req["max_completion_tokens"], "nil must not be modified")
}

func TestRewriteRequest_EmptyEnforcedNoOp(t *testing.T) {
	enforced := enforcedOfLen(0)
	req := map[string]interface{}{
		"max_tokens":            float64(100),
		"max_completion_tokens": float64(100),
	}
	rewriteRequest(req, enforced)
	require.EqualValues(t, float64(100), req["max_tokens"], "empty enforced => no bump")
	require.EqualValues(t, float64(100), req["max_completion_tokens"], "empty enforced => no bump")
}

func TestRewriteRequest_ParsedFromJSONFloat64(t *testing.T) {
	// Maps unmarshalled from JSON yield float64 for numeric fields. Verify the
	// helper path through JSONNumericUint64 handles JSON-loaded shapes.
	raw := []byte(`{"max_tokens": 200, "max_completion_tokens": 200}`)
	var req map[string]interface{}
	require.NoError(t, json.Unmarshal(raw, &req))
	rewriteRequest(req, enforcedOfLen(300))
	require.EqualValues(t, uint64(600), req["max_tokens"])
	require.EqualValues(t, uint64(600), req["max_completion_tokens"])
}
