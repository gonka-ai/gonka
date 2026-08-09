package inference

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"common/completionapi"
	devshardpkg "devshard"
	"devshard/observability"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestProcessExecutionHTTPResponseRejectsMissingStreamUsage(t *testing.T) {
	before := testutil.ToFloat64(observability.MissingUsageCounterForTest())

	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":null,"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":""},"logprobs":null,"finish_reason":"stop"}]}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	processed, err := processExecutionHTTPResponse(devshardpkg.ExecuteRequest{}, resp, "devshard-escrow-1")
	require.ErrorIs(t, err, completionapi.ErrStreamedUsageMissing)
	require.Nil(t, processed)
	require.Equal(t, before+1, testutil.ToFloat64(observability.MissingUsageCounterForTest()))
}

func TestProcessExecutionHTTPResponseRejectsZeroPromptTokensOnNonEmpty(t *testing.T) {
	before := testutil.ToFloat64(observability.MissingUsageCounterForTest())

	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":null,"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":0,"completion_tokens":1}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	processed, err := processExecutionHTTPResponse(devshardpkg.ExecuteRequest{}, resp, "devshard-escrow-2")
	require.ErrorIs(t, err, completionapi.ErrStreamedUsageMissing)
	require.Nil(t, processed)
	require.Equal(t, before+1, testutil.ToFloat64(observability.MissingUsageCounterForTest()))
}

func TestProcessExecutionHTTPResponseAcceptsUsageChunk(t *testing.T) {
	body := strings.Join([]string{
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[{"index":0,"delta":{"content":"Hi"},"logprobs":null,"finish_reason":null}]}`,
		`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"m","choices":[],"usage":{"prompt_tokens":31,"completion_tokens":1}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}

	processed, err := processExecutionHTTPResponse(devshardpkg.ExecuteRequest{}, resp, "devshard-escrow-3")
	require.NoError(t, err)
	require.NotNil(t, processed)
	require.Equal(t, uint64(31), processed.inputTokens)
	require.Equal(t, uint64(1), processed.outputTokens)
	require.NotEmpty(t, processed.responseHash)
	require.NotEmpty(t, processed.responseBody)
}
