package public

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"

	"github.com/labstack/echo/v4"
)

// createTestRequest creates a test HTTP request with the given body
func createTestRequest(body []byte) *http.Request {
	req, _ := http.NewRequest(http.MethodPost, "/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	return req
}

type mockPayloadStorage struct {
	stored         map[string]struct{ prompt, response []byte }
	storeErr       error
	retrieveErr    error
	retrieveCalled bool
}

func newMockPayloadStorage() *mockPayloadStorage {
	return &mockPayloadStorage{
		stored: make(map[string]struct{ prompt, response []byte }),
	}
}

func (m *mockPayloadStorage) Store(ctx context.Context, inferenceId string, epochId uint64, promptPayload, responsePayload []byte) error {
	if m.storeErr != nil {
		return m.storeErr
	}
	m.stored[inferenceId] = struct{ prompt, response []byte }{promptPayload, responsePayload}
	return nil
}

func (m *mockPayloadStorage) Retrieve(ctx context.Context, inferenceId string, epochId uint64) ([]byte, []byte, error) {
	m.retrieveCalled = true
	if m.retrieveErr != nil {
		return nil, nil, m.retrieveErr
	}
	data, ok := m.stored[inferenceId]
	if !ok {
		return nil, nil, payloadstorage.ErrNotFound
	}
	return data.prompt, data.response, nil
}

func (m *mockPayloadStorage) PruneEpoch(ctx context.Context, epochId uint64) error {
	return nil
}

func newTestPhaseTracker(epochIndex uint64) *chainphase.ChainPhaseTracker {
	tracker := &chainphase.ChainPhaseTracker{}
	epoch := types.Epoch{Index: epochIndex}
	params := types.EpochParams{
		EpochLength:      200,
		PocStageDuration: 50,
	}
	tracker.Update(
		chainphase.BlockInfo{Height: 100, Hash: "abc"},
		&epoch,
		&params,
		true,
		nil,
	)
	return tracker
}

func TestStorePayloadsToStorage_Success(t *testing.T) {
	storage := newMockPayloadStorage()
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":123,"messages":[{"role":"user","content":"hello"}]}`)
	responsePayload := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-1", promptPayload, responsePayload)

	require.Len(t, storage.stored, 1)
	stored := storage.stored["inf-1"]
	require.Equal(t, promptPayload, stored.prompt)
	require.Equal(t, responsePayload, stored.response)
}

func TestStorePayloadsToStorage_NilStorage(t *testing.T) {
	s := &Server{
		payloadStorage: nil,
		phaseTracker:   newTestPhaseTracker(5),
	}

	// Should not panic with nil storage
	s.storePayloadsToStorage(context.Background(), "inf-1", []byte("prompt"), []byte("response"))
}

func TestStorePayloadsToStorage_NilPhaseTracker(t *testing.T) {
	storage := newMockPayloadStorage()
	s := &Server{
		payloadStorage: storage,
		phaseTracker:   nil,
	}

	// Should not panic with nil phase tracker
	s.storePayloadsToStorage(context.Background(), "inf-1", []byte("prompt"), []byte("response"))
	require.Len(t, storage.stored, 0)
}

func TestStorePayloadsToStorage_Retrieval(t *testing.T) {
	storage := newMockPayloadStorage()
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":123}`)
	responsePayload := []byte(`{"id":"inf-1","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-1", promptPayload, responsePayload)

	// Verify the stored payload can be retrieved
	storedPrompt, storedResponse, err := storage.Retrieve(context.Background(), "inf-1", 5)
	require.NoError(t, err)
	require.Equal(t, promptPayload, storedPrompt)
	require.Equal(t, responsePayload, storedResponse)
}

func TestFileStorageIntegration(t *testing.T) {
	dir := t.TempDir()
	storage := payloadstorage.NewFileStorage(dir)
	tracker := newTestPhaseTracker(5)

	s := &Server{
		payloadStorage: storage,
		phaseTracker:   tracker,
	}

	promptPayload := []byte(`{"model":"test","seed":42,"messages":[{"role":"user","content":"test"}]}`)
	responsePayload := []byte(`{"id":"inf-123","choices":[{"index":0,"message":{"role":"assistant","content":"response"}}]}`)

	s.storePayloadsToStorage(context.Background(), "inf-123", promptPayload, responsePayload)

	storedPrompt, storedResponse, err := storage.Retrieve(context.Background(), "inf-123", 5)
	require.NoError(t, err)
	require.Equal(t, promptPayload, storedPrompt)
	require.Equal(t, responsePayload, storedResponse)
}

func TestEmptyButParseableResponsePayload_EnforcedTokensEmptySlice(t *testing.T) {
	resp := emptyButParseableResponsePayload("inf-empty", "test-model", 1)
	require.NotNil(t, resp)

	enforcedTokens, err := resp.GetEnforcedTokens()
	require.NoError(t, err)

	b, err := json.Marshal(enforcedTokens)
	require.NoError(t, err)
	t.Logf("enforcedTokens=%s", string(b))

	// With our synthetic logprobs, enforced tokens should be present and parseable.
	require.NotEmpty(t, enforcedTokens.Tokens)
}

// TestReadRequestBody_NormalSize tests that normal-sized requests are read successfully
func TestReadRequestBody_NormalSize(t *testing.T) {
	body := []byte(`{"model": "test", "messages": [{"role": "user", "content": "Hello"}]}`)
	req := createTestRequest(body)

	result, err := readRequestBody(req, nil)
	require.NoError(t, err)
	require.Equal(t, body, result)
}

// TestReadRequestBody_ExceedsMaxSize tests that oversized requests are rejected
func TestReadRequestBody_ExceedsMaxSize(t *testing.T) {
	// Create a body larger than MaxRequestBodySize (10 MB)
	oversizedBody := make([]byte, MaxRequestBodySize+1)
	for i := range oversizedBody {
		oversizedBody[i] = 'a'
	}
	req := createTestRequest(oversizedBody)

	_, err := readRequestBody(req, nil)
	require.Error(t, err)
	// http.MaxBytesReader returns an error when limit is exceeded
}

// TestReadRequestBody_ExactlyMaxSize tests that requests at exactly max size work
func TestReadRequestBody_ExactlyMaxSize(t *testing.T) {
	// Create a body exactly at MaxRequestBodySize
	exactBody := make([]byte, MaxRequestBodySize)
	for i := range exactBody {
		exactBody[i] = 'b'
	}
	req := createTestRequest(exactBody)

	result, err := readRequestBody(req, nil)
	require.NoError(t, err)
	require.Len(t, result, MaxRequestBodySize)
}

// TestReadRequestBody_EmptyBody tests that empty bodies work
func TestReadRequestBody_EmptyBody(t *testing.T) {
	req := createTestRequest([]byte{})

	result, err := readRequestBody(req, nil)
	require.NoError(t, err)
	require.Empty(t, result)
}

// TestMaxRequestBodySizeConstant verifies the constant is set to expected value
func TestMaxRequestBodySizeConstant(t *testing.T) {
	// MaxRequestBodySize should be 10 MB
	expectedSize := 10 * 1024 * 1024
	require.Equal(t, expectedSize, MaxRequestBodySize, "MaxRequestBodySize should be 10 MB")
}

func TestMapRequestBodyReadError(t *testing.T) {
	testCases := []struct {
		name         string
		inputErr     error
		expectedCode int
		expectedMsg  string
	}{
		{
			name:         "max bytes exceeded",
			inputErr:     &http.MaxBytesError{Limit: MaxRequestBodySize},
			expectedCode: http.StatusRequestEntityTooLarge,
			expectedMsg:  "request body too large",
		},
		{
			name:         "unexpected EOF",
			inputErr:     io.ErrUnexpectedEOF,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "malformed request body",
		},
		{
			name:         "read timeout",
			inputErr:     context.DeadlineExceeded,
			expectedCode: http.StatusRequestTimeout,
			expectedMsg:  "request body read timeout",
		},
		{
			name:         "read canceled",
			inputErr:     context.Canceled,
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "request body read cancelled",
		},
		{
			name:         "generic read error",
			inputErr:     errors.New("transport failed"),
			expectedCode: http.StatusBadRequest,
			expectedMsg:  "failed to read request body",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapRequestBodyReadError(tc.inputErr)

			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, tc.expectedCode, httpErr.Code)
			require.Equal(t, tc.expectedMsg, httpErr.Message)
		})
	}
}

func TestMapExecutorCompletionsUnsupportedError(t *testing.T) {
	testCases := []struct {
		name            string
		forwardPath     string
		statusCode      int
		expectErr       bool
		expectedCode    int
		expectedMessage string
	}{
		{
			name:            "completions endpoint not found",
			forwardPath:     completionsPath,
			statusCode:      http.StatusNotFound,
			expectErr:       true,
			expectedCode:    http.StatusServiceUnavailable,
			expectedMessage: executorCompletionsUnsupportedMsg,
		},
		{
			name:            "completions method not allowed",
			forwardPath:     completionsPath,
			statusCode:      http.StatusMethodNotAllowed,
			expectErr:       true,
			expectedCode:    http.StatusServiceUnavailable,
			expectedMessage: executorCompletionsUnsupportedMsg,
		},
		{
			name:            "completions not implemented",
			forwardPath:     completionsPath,
			statusCode:      http.StatusNotImplemented,
			expectErr:       true,
			expectedCode:    http.StatusServiceUnavailable,
			expectedMessage: executorCompletionsUnsupportedMsg,
		},
		{
			name:        "chat path keeps original status handling",
			forwardPath: chatCompletionsPath,
			statusCode:  http.StatusNotFound,
			expectErr:   false,
		},
		{
			name:        "completions other status is not remapped",
			forwardPath: completionsPath,
			statusCode:  http.StatusBadGateway,
			expectErr:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			err := mapExecutorCompletionsUnsupportedError(tc.forwardPath, tc.statusCode)

			if !tc.expectErr {
				require.NoError(t, err)
				return
			}

			var httpErr *echo.HTTPError
			require.ErrorAs(t, err, &httpErr)
			require.Equal(t, tc.expectedCode, httpErr.Code)
			require.Equal(t, tc.expectedMessage, httpErr.Message)
		})
	}
}

func TestReadRequest_AcceptsMultipartContent(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":[{"type":"text","text":"Hello"},{"type":"image_url","image_url":{"url":"https://example.com/cat.png"}},{"type":"text","text":" world"}]}]}`)
	req := createTestRequest(body)

	chatRequest, err := readRequest(req, "transfer-agent", body, "", chatCompletionsPath, body)
	require.NoError(t, err)
	require.Equal(t, "test", chatRequest.OpenAiRequest.Model)

	promptText, ignoredParts := FlattenMessagesText(chatRequest.OpenAiRequest.Messages)
	require.Equal(t, "Hello world\n", promptText)
	require.Equal(t, 1, ignoredParts)
}

func TestMultipartContent_RoundTrip(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":[{"type":"text","text":"describe this"},{"type":"image_url","image_url":{"url":"https://example.com/cat.png","detail":"high"}}]}]}`)
	req := createTestRequest(body)

	chatRequest, err := readRequest(req, "transfer-agent", body, "", chatCompletionsPath, body)
	require.NoError(t, err)

	roundTripped, err := json.Marshal(chatRequest.OpenAiRequest)
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(roundTripped, &result))
	messages := result["messages"].([]interface{})
	content := messages[0].(map[string]interface{})["content"].([]interface{})

	imgPart := content[1].(map[string]interface{})
	require.Equal(t, "image_url", imgPart["type"])
	imgURL := imgPart["image_url"].(map[string]interface{})
	require.Equal(t, "https://example.com/cat.png", imgURL["url"])
	require.Equal(t, "high", imgURL["detail"])
}

func TestReadRequest_AcceptsMissingMessageContent(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"assistant"}]}`)
	req := createTestRequest(body)

	chatRequest, err := readRequest(req, "transfer-agent", body, "", chatCompletionsPath, body)
	require.NoError(t, err, "missing content is valid for assistant/tool-calling messages")
	require.Equal(t, "test", chatRequest.OpenAiRequest.Model)

	text, ignored := FlattenMessagesText(chatRequest.OpenAiRequest.Messages)
	require.Equal(t, "\n", text)
	require.Equal(t, 0, ignored)
}

func TestReadRequest_AcceptsToolCallingPayload(t *testing.T) {
	body := []byte(`{
		"model":"test",
		"messages":[
			{"role":"user","content":"What is the weather in NYC?"},
			{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"location\":\"NYC\"}"}}]},
			{"role":"tool","content":"72°F and sunny","tool_call_id":"call_1"}
		]
	}`)
	req := createTestRequest(body)

	chatRequest, err := readRequest(req, "transfer-agent", body, "", chatCompletionsPath, body)
	require.NoError(t, err)
	require.Equal(t, "test", chatRequest.OpenAiRequest.Model)
	require.Len(t, chatRequest.OpenAiRequest.Messages, 3)

	require.NotNil(t, chatRequest.OpenAiRequest.Messages[0].Content.Text)
	require.Nil(t, chatRequest.OpenAiRequest.Messages[1].Content.Text)
	require.Nil(t, chatRequest.OpenAiRequest.Messages[1].Content.Parts)
	require.NotNil(t, chatRequest.OpenAiRequest.Messages[2].Content.Text)

	roundTripped, err := json.Marshal(chatRequest.OpenAiRequest)
	require.NoError(t, err)

	var result map[string]interface{}
	require.NoError(t, json.Unmarshal(roundTripped, &result))
	messages := result["messages"].([]interface{})
	assistantMsg := messages[1].(map[string]interface{})
	require.Nil(t, assistantMsg["content"], "assistant content should marshal as null")
}

func TestReadRequest_RejectsUnsupportedContentType(t *testing.T) {
	body := []byte(`{"model":"test","messages":[{"role":"user","content":123}]}`)
	req := createTestRequest(body)

	_, err := readRequest(req, "transfer-agent", body, "", chatCompletionsPath, body)
	require.Error(t, err)

	var httpErr *echo.HTTPError
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusBadRequest, httpErr.Code)
	require.Contains(t, httpErr.Message, "invalid chat completion request")
}
