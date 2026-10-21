package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"common/completionapi"
	"common/validation"
	"devshard/testenv/mockopenai"
	"devshard/testenv/replay"

	"github.com/stretchr/testify/require"
)

// The recordings assume the gateway defaults (output limits, upstream streaming forced) and the
// chain's default logprobs mode.
const recordedLogprobsMode = "processed_logprobs"

// Test flow:
// 1. Send each recording's client request through the gateway pipeline and the executor rewrite.
// 2. Require the result to be the recorded request and the mock ML node to stream the recording.
// 3. Validate the stored response with the validator's replay and require it to pass.
func TestGatewayForwardedClientRequestHitsRecordedReplay(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	files, err := filepath.Glob("../../testenv/mockopenai/testdata/vllm_recorded_*.jsonl")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, file := range files {
		dataset, err := replay.LoadFile(file)
		require.NoError(t, err)
		srv, err := mockopenai.NewServerWithError(mockopenai.Config{ReplayFile: file})
		require.NoError(t, err)
		ml := httptest.NewServer(srv.Handler())
		t.Cleanup(ml.Close)
		build := strings.TrimSuffix(filepath.Base(file), ".jsonl")
		for i := 0; i < dataset.Len(); i++ {
			sample, ok := dataset.Sample(i)
			require.True(t, ok)
			t.Run(build+"/"+sample.Model+"/"+sample.Name, func(t *testing.T) {
				require.NotEmpty(t, sample.ClientRequest, "recording keeps no client request")
				forwarded, _, err := upstreamChatRequestPipeline().Normalize(sample.ClientRequest, false, defaultOutputTokenLimits(), sample.Model)
				require.NoError(t, err)
				executed, err := completionapi.ModifyRequestBodyWithLogprobsMode(forwarded, 77, recordedLogprobsMode)
				require.NoError(t, err)
				require.JSONEq(t, string(sample.Request), string(executed.NewBody))

				resp, err := http.Post(ml.URL+"/v1/chat/completions", "application/json", bytes.NewReader(executed.NewBody))
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)
				raw, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.NoError(t, resp.Body.Close())
				var want strings.Builder
				for _, chunk := range sample.Recorded.Stream {
					want.WriteString("data: " + string(chunk) + "\n\n")
				}
				want.WriteString("data: [DONE]\n\n")
				require.Equal(t, want.String(), string(raw))

				resp.Body = io.NopCloser(bytes.NewReader(raw))
				processor := completionapi.NewExecutorResponseProcessor("inference-recorded", true)
				require.NoError(t, completionapi.ProcessHTTPResponse(resp, processor))
				stored, err := processor.GetResponseBytes()
				require.NoError(t, err)
				var serialized completionapi.SerializedStreamedResponse
				require.NoError(t, json.Unmarshal(stored, &serialized))

				completion, err := completionapi.NewCompletionResponseFromLines(serialized.Events)
				require.NoError(t, err)
				usage, err := completion.GetUsage()
				require.NoError(t, err)
				result, err := validation.ExecuteValidation(
					context.Background(),
					"41",
					forwarded,
					stored,
					func(_ context.Context, body []byte) (*http.Response, error) {
						return http.Post(ml.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
					},
					usage.PromptTokens,
					usage.CompletionTokens,
					recordedLogprobsMode,
					0,
				)
				require.NoError(t, err)
				require.True(t, result.IsSuccessful(), "honest replay of a recording must validate: %#v", result)
			})
		}
	}
}
