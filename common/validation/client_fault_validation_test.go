package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/require"
)

func streamedPayloadOf(t *testing.T, lines ...string) []byte {
	t.Helper()
	payload, err := json.Marshal(completionapi.SerializedStreamedResponse{Events: lines})
	require.NoError(t, err)
	return payload
}

var (
	jsonHeader   = http.Header{"Content-Type": []string{"application/json"}}
	streamHeader = http.Header{"Content-Type": []string{"text/event-stream"}}
)

func TestExecuteValidation_ClientFaultVerdicts(t *testing.T) {
	clientFault := streamedPayloadOf(t, `data: {"error":{"code":400,"message":"token_id 999999999 in logit_bias contains out-of-vocab token id","type":"BadRequestError"},"id":"devshard-1-1"}`, "data: [DONE]")
	engineFault := streamedPayloadOf(t, `data: {"error":{"code":500,"message":"EngineCore encountered an issue","type":"InternalServerError"},"id":"devshard-1-1"}`, "data: [DONE]")
	for _, tc := range []struct {
		name          string
		stored        []byte
		claimedOutput uint64
		status        int
		header        http.Header
		body          string
		wantValid     bool
		wantInvalid   bool
		wantAbstain   bool
		wantNoReplay  bool
	}{
		{name: "json bad request confirms", stored: clientFault, status: 400, header: jsonHeader, body: `{"error":{"code":400}}`, wantValid: true},
		{name: "json unprocessable confirms", stored: clientFault, status: 422, header: jsonHeader, body: `{"error":{"code":422}}`, wantValid: true},
		{name: "streamed client fault confirms", stored: clientFault, status: 200, header: streamHeader, body: "data: {\"error\":{\"code\":400,\"message\":\"bad\"}}\n\ndata: [DONE]\n\n", wantValid: true},
		{name: "json served prompt abstains", stored: clientFault, status: 200, header: jsonHeader, body: string(responsePayloadJSON("42", -0.1)), wantAbstain: true},
		{name: "streamed served prompt abstains", stored: clientFault, status: 200, header: streamHeader, body: "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\ndata: [DONE]\n\n", wantAbstain: true},
		{name: "output before a streamed fault abstains", stored: clientFault, status: 200, header: streamHeader, body: "data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\ndata: {\"error\":{\"code\":400}}\n\n", wantAbstain: true},
		{name: "unavailable validator is retried", stored: clientFault, status: 503, header: jsonHeader, body: `busy`},
		{name: "streamed engine fault is retried", stored: clientFault, status: 200, header: streamHeader, body: "data: {\"error\":{\"code\":500,\"message\":\"bad\"}}\n\n"},
		{name: "malformed stream is retried", stored: clientFault, status: 200, header: streamHeader, body: "data: {bad\n\n"},
		{name: "billed client fault is invalid", stored: clientFault, claimedOutput: 4096, wantInvalid: true, wantNoReplay: true},
		{name: "engine fault is invalid", stored: engineFault, wantInvalid: true, wantNoReplay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			replayed := false
			execute := func(_ context.Context, body []byte) (*http.Response, error) {
				replayed = true
				var request map[string]any
				require.NoError(t, json.Unmarshal(body, &request))
				require.NotContains(t, request, "enforced_tokens")
				return &http.Response{StatusCode: tc.status, Header: tc.header, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			}
			prompt := []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"enforced_tokens":{"tokens":[]}}`)

			result, err := ExecuteValidation(context.Background(), "1", prompt, tc.stored, execute, 0, tc.claimedOutput, "", 200064)

			require.Equal(t, !tc.wantNoReplay, replayed)
			switch {
			case tc.wantValid:
				require.NoError(t, err)
				require.True(t, result.IsSuccessful())
			case tc.wantInvalid:
				require.NoError(t, err)
				require.False(t, result.IsSuccessful())
			default:
				require.Error(t, err)
				require.Nil(t, result)
				require.Equal(t, tc.wantAbstain, errors.Is(err, ErrClientFaultReplayInconclusive))
			}
		})
	}
}

func TestExecuteValidation_ClientFaultReplaysTheExecutorRequestWithTheCoarseBound(t *testing.T) {
	prompt := `{"model":"m","messages":[{"role":"user","content":"hi"}],"stream":true,"stream_options":{"continuous_usage_stats":"invalid"},"logit_bias":{"300000":5,"99999999":5}}`
	executorRequest, err := completionapi.ModifyRequestBodyForVocabulary([]byte(prompt), validationReplaySeed("1"), "", 0)
	require.NoError(t, err)
	var replayed []byte
	execute := func(_ context.Context, body []byte) (*http.Response, error) {
		replayed = body
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":{"code":400}}`))}, nil
	}

	result, err := ExecuteValidation(context.Background(), "1", []byte(prompt), streamedPayloadOf(t, `data: {"error":{"code":400,"message":"bad"}}`), execute, 0, 0, "", 200064)

	require.NoError(t, err)
	require.True(t, result.IsSuccessful())
	require.JSONEq(t, string(executorRequest.NewBody), string(replayed))
}

func TestExecuteValidation_ReplayBoundsLogitBiasByVocabulary(t *testing.T) {
	responsePayload := responsePayloadJSON("42", -0.1)
	var replayed map[string]any
	execute := func(_ context.Context, body []byte) (*http.Response, error) {
		require.NoError(t, json.Unmarshal(body, &replayed))
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(responsePayload))}, nil
	}

	_, err := ExecuteValidation(context.Background(), "1", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"logit_bias":{"300000":5}}`), responsePayload, execute, 0, 0, "", 200064)

	require.NoError(t, err)
	require.NotContains(t, replayed, "logit_bias")
}

func TestExecuteValidation_ClientFaultReplayStopsReadingAtTheFirstOutput(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header http.Header
		body   io.Reader
	}{
		{name: "stream", header: streamHeader, body: io.MultiReader(strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"), failingReplayReader{})},
		{name: "json", header: jsonHeader, body: failingReplayReader{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execute := func(context.Context, []byte) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: tc.header, Body: io.NopCloser(tc.body)}, nil
			}

			result, err := ExecuteValidation(context.Background(), "1", []byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), streamedPayloadOf(t, `data: {"error":{"code":400,"message":"bad"}}`), execute, 0, 0, "", 0)

			require.ErrorIs(t, err, ErrClientFaultReplayInconclusive)
			require.Nil(t, result)
		})
	}
}

type failingReplayReader struct{}

func (failingReplayReader) Read([]byte) (int, error) {
	return 0, errors.New("replay body read past the first output")
}
