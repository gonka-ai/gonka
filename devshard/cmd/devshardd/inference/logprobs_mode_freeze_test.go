package inference

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	devshardpkg "devshard"

	"github.com/stretchr/testify/require"
)

type noopPayloadStore struct{}

func (noopPayloadStore) Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error {
	return nil
}

func captureExecutedLogprobsMode(t *testing.T, mode string) string {
	t.Helper()

	var captured []byte
	execute := func(ctx context.Context, model string, body []byte) (*http.Response, error) {
		captured = append([]byte(nil), body...)
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{}`)),
		}, nil
	}

	req := devshardpkg.ExecuteRequest{
		InferenceID:  1,
		Model:        "Qwen",
		Prompt:       []byte(`{"model":"Qwen","messages":[{"role":"user","content":"hi"}],"max_tokens":4}`),
		EscrowID:     "escrow-1",
		LogprobsMode: mode,
	}

	_, _ = executeInference(context.Background(), req, noopPayloadStore{}, 0, execute)
	require.NotEmpty(t, captured, "executor never received a request body")

	var sent map[string]any
	require.NoError(t, json.Unmarshal(captured, &sent))
	got, _ := sent["logprobs_mode"].(string)
	return got
}

func TestExecuteInference_UsesRequestFrozenLogprobsMode(t *testing.T) {
	require.Equal(t, "raw_logprobs", captureExecutedLogprobsMode(t, "raw_logprobs"))
	require.Equal(t, "processed_logprobs", captureExecutedLogprobsMode(t, "processed_logprobs"))
}
