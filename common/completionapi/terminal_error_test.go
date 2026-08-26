package completionapi

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

type terminalErrorFixture struct {
	Name        string   `json:"name"`
	WantOK      bool     `json:"want_ok"`
	WantType    string   `json:"want_type"`
	WantCode    string   `json:"want_code"`
	WantMessage string   `json:"want_message"`
	Events      []string `json:"events"`
}

func payloadFromEvents(t *testing.T, events []string) []byte {
	t.Helper()
	b, err := json.Marshal(SerializedStreamedResponse{Events: events})
	require.NoError(t, err)
	return b
}

func loadTerminalErrorFixtures(t *testing.T) []terminalErrorFixture {
	t.Helper()
	path := filepath.Join("testdata", "terminal_error_fixtures.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixtures []terminalErrorFixture
	require.NoError(t, json.Unmarshal(raw, &fixtures))
	require.NotEmpty(t, fixtures)
	return fixtures
}

func TestIsTerminalErrorResponse_EngineCore(t *testing.T) {
	payload := payloadFromEvents(t, []string{
		`data: {"error":{"code":500,"message":"EngineCore encountered an issue. See stack trace (above) for the root cause.","param":null,"type":"InternalServerError"},"id":"devshard-57577-89"}`,
		`data: [DONE]`,
	})
	details, ok := IsTerminalErrorResponse(payload)
	require.True(t, ok)
	require.Equal(t, "InternalServerError", details.Type)
	require.Equal(t, "500", details.Code)
	require.Equal(t, "EngineCore encountered an issue. See stack trace (above) for the root cause.", details.Message)
}

func TestIsTerminalErrorResponse_ContentThenError(t *testing.T) {
	payload := payloadFromEvents(t, []string{
		`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"hello"}}]}`,
		`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: [DONE]`,
	})
	details, ok := IsTerminalErrorResponse(payload)
	require.False(t, ok)
	require.Equal(t, "InternalServerError", details.Type)
}

func TestIsTerminalErrorResponse_ErrorWithCompletionTokens(t *testing.T) {
	payload := payloadFromEvents(t, []string{
		`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":12,"completion_tokens":7}}`,
		`data: [DONE]`,
	})
	_, ok := IsTerminalErrorResponse(payload)
	require.False(t, ok)
}

func TestIsTerminalErrorResponse_EmptyStream(t *testing.T) {
	payload := payloadFromEvents(t, []string{
		`data: {"id":"devshard-1-1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}]}`,
		`data: [DONE]`,
	})
	_, ok := IsTerminalErrorResponse(payload)
	require.False(t, ok)
}

func TestIsTerminalErrorResponse_UnparseableThenError(t *testing.T) {
	payload := payloadFromEvents(t, []string{
		`data: {not json`,
		`data: {"error":{"code":500,"message":"boom","type":"InternalServerError"},"id":"devshard-1-1"}`,
		`data: [DONE]`,
	})
	details, ok := IsTerminalErrorResponse(payload)
	require.False(t, ok)
	require.Equal(t, ErrorDetails{}, details, "parse failure must not classify a miss")
}

func TestIsTerminalErrorResponse_GoldenFixtures(t *testing.T) {
	fixtures := loadTerminalErrorFixtures(t)
	seen := map[string]bool{}
	for _, fx := range fixtures {
		if seen[fx.Name] {
			t.Fatalf("duplicate fixture name %q", fx.Name)
		}
		seen[fx.Name] = true
		t.Run(fx.Name, func(t *testing.T) {
			payload := payloadFromEvents(t, fx.Events)
			details, ok := IsTerminalErrorResponse(payload)
			require.Equal(t, fx.WantOK, ok)
			if fx.WantType != "" {
				require.Equal(t, fx.WantType, details.Type)
			}
			if fx.WantCode != "" {
				require.Equal(t, fx.WantCode, details.Code)
			}
			if fx.WantMessage != "" {
				require.Equal(t, fx.WantMessage, details.Message)
			}
		})
	}
	required := []string{
		"enginecore",
		"content_then_error",
		"error_with_completion_tokens",
		"empty_stream_role_done",
		"unparseable_then_error",
	}
	for _, name := range required {
		if !seen[name] {
			t.Fatalf("golden corpus missing required fixture %q", name)
		}
	}
}

func TestIsTerminalErrorResponse_EmptyPayload(t *testing.T) {
	_, ok := IsTerminalErrorResponse(nil)
	require.False(t, ok)
	_, ok = IsTerminalErrorResponse([]byte{})
	require.False(t, ok)
}

func TestIsTerminalErrorResponse_MalformedPayload(t *testing.T) {
	_, ok := IsTerminalErrorResponse([]byte(`not json`))
	require.False(t, ok)
}

func TestIsTerminalErrorResponse_JSONErrorBodyOutOfScope(t *testing.T) {
	body := []byte(`{"error":{"code":400,"message":"bad request","type":"BadRequestError"}}`)
	_, ok := IsTerminalErrorResponse(body)
	require.False(t, ok, "non-streamed JSON error bodies are out of scope")
}

func TestJsonErrorBodyGetUsageFails(t *testing.T) {
	body := []byte(`{"error":{"code":400,"message":"bad request","type":"BadRequestError"}}`)
	resp, err := NewCompletionResponseFromBytes(body)
	require.NoError(t, err)
	_, err = resp.GetUsage()
	require.Error(t, err, "client-fault JSON errors must fail GetUsage so no Finish is signed")
}
