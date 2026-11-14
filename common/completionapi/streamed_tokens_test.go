package completionapi

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEnforcedTokensIncludesEveryTokenInAChunk(t *testing.T) {
	var chunk StreamedResponse
	require.NoError(t, json.Unmarshal([]byte(`{"data":[{"choices":[{"logprobs":{"content":[{"token":"a","top_logprobs":[{"token":"A"}]},{"token":"b","top_logprobs":[{"token":"B"}]}]}}]}]}`), &chunk))
	r := &StreamedCompletionResponse{Resp: chunk}
	got, err := r.GetEnforcedTokens()
	require.NoError(t, err)
	require.Equal(t, []EnforcedToken{{Token: "a", TopTokens: []string{"A"}}, {Token: "b", TopTokens: []string{"B"}}}, got.Tokens)
}
