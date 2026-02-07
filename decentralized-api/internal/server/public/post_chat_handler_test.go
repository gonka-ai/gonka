package public

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"decentralized-api/chainphase"
	"decentralized-api/payloadstorage"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

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
	tracker := chainphase.NewChainPhaseTracker()
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

// TestMaxResponseBodySizeWithinReasonableBounds verifies the constant is within acceptable range
func TestMaxResponseBodySizeWithinReasonableBounds(t *testing.T) {
	oneMB := 1024 * 1024
	hundredMB := 100 * 1024 * 1024
	require.Greater(t, MaxResponseBodySize, oneMB,
		"MaxResponseBodySize should be greater than 1 MB to allow realistic responses")
	require.Less(t, MaxResponseBodySize, hundredMB,
		"MaxResponseBodySize should be less than 100 MB to prevent memory exhaustion")
}

// TestMaxErrorResponseSizeWithinReasonableBounds verifies the constant is within acceptable range
func TestMaxErrorResponseSizeWithinReasonableBounds(t *testing.T) {
	oneKB := 1024
	tenMB := 10 * 1024 * 1024
	require.Greater(t, MaxErrorResponseSize, oneKB,
		"MaxErrorResponseSize should be greater than 1 KB to hold meaningful error messages")
	require.Less(t, MaxErrorResponseSize, tenMB,
		"MaxErrorResponseSize should be less than 10 MB to prevent memory exhaustion on errors")
	require.Less(t, MaxErrorResponseSize, MaxResponseBodySize,
		"MaxErrorResponseSize should be smaller than MaxResponseBodySize")
}

// TestGetInferenceErrorMessage_NormalSize tests that normal error messages are read
func TestGetInferenceErrorMessage_NormalSize(t *testing.T) {
	errorBody := []byte(`{"error": "something went wrong"}`)
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewReader(errorBody)),
	}

	msg := getInferenceErrorMessage(resp)
	require.Contains(t, msg, "code = 500")
	require.Contains(t, msg, "something went wrong")
}

// TestGetInferenceErrorMessage_OversizedResponse tests that oversized responses are truncated
func TestGetInferenceErrorMessage_OversizedResponse(t *testing.T) {
	// Create an error body larger than MaxErrorResponseSize (1 MB)
	oversizedBody := bytes.Repeat([]byte("x"), MaxErrorResponseSize+1000)
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewReader(oversizedBody)),
	}

	msg := getInferenceErrorMessage(resp)
	require.Contains(t, msg, "code = 500")
	// The message should be truncated to MaxErrorResponseSize
	// Check that we don't have the full oversized body
	require.LessOrEqual(t, len(msg), MaxErrorResponseSize+100, "Error message should be truncated")
}

// TestGetInferenceErrorMessage_EmptyBody tests handling of empty error body
func TestGetInferenceErrorMessage_EmptyBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 404,
		Body:       io.NopCloser(bytes.NewReader([]byte{})),
	}

	msg := getInferenceErrorMessage(resp)
	require.Contains(t, msg, "code = 404")
}

// TestGetInferenceErrorMessage_ExactlyAtLimit tests that a body at exactly the limit is fully included
func TestGetInferenceErrorMessage_ExactlyAtLimit(t *testing.T) {
	exactBody := bytes.Repeat([]byte("A"), MaxErrorResponseSize)
	resp := &http.Response{
		StatusCode: 502,
		Body:       io.NopCloser(bytes.NewReader(exactBody)),
	}

	msg := getInferenceErrorMessage(resp)
	require.Contains(t, msg, "code = 502")
	// The full body should be present since it's exactly at the limit
	require.Contains(t, msg, string(exactBody))
}

// TestGetInferenceErrorMessage_TruncatesOversizedBody verifies the handler's LimitReader
// integration actually caps how much data is read from the response body.
func TestGetInferenceErrorMessage_TruncatesOversizedBody(t *testing.T) {
	// Build a body that is 2x the error limit. The first half is 'A', the second half is 'B'.
	// After truncation, only 'A' bytes should appear.
	halfSize := MaxErrorResponseSize
	body := append(bytes.Repeat([]byte("A"), halfSize), bytes.Repeat([]byte("B"), halfSize)...)

	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(bytes.NewReader(body)),
	}

	msg := getInferenceErrorMessage(resp)
	// The message must NOT contain 'B' bytes, proving truncation happened
	require.NotContains(t, msg, "B",
		"Message should not contain bytes beyond MaxErrorResponseSize limit")
	require.Contains(t, msg, "code = 500")
}

// TestProxyJsonResponse_TruncatesOversizedBody verifies that proxyJsonResponse uses
// MaxResponseBodySize to cap how much data is read from the upstream response.
func TestProxyJsonResponse_TruncatesOversizedBody(t *testing.T) {
	// Create a response body that exceeds MaxResponseBodySize
	oversizedBody := bytes.Repeat([]byte("z"), MaxResponseBodySize+4096)
	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(oversizedBody)),
	}

	recorder := &fakeResponseWriter{header: http.Header{}, body: &bytes.Buffer{}}
	proxyJsonResponse(resp, recorder, nil, "test-inf-truncate")

	// The written body should be capped at MaxResponseBodySize
	require.Equal(t, MaxResponseBodySize, recorder.body.Len(),
		"proxyJsonResponse should truncate body to MaxResponseBodySize")
}

// fakeResponseWriter is a minimal http.ResponseWriter for testing proxy output.
type fakeResponseWriter struct {
	header     http.Header
	body       *bytes.Buffer
	statusCode int
}

func (f *fakeResponseWriter) Header() http.Header         { return f.header }
func (f *fakeResponseWriter) WriteHeader(statusCode int)   { f.statusCode = statusCode }
func (f *fakeResponseWriter) Write(b []byte) (int, error)  { return f.body.Write(b) }
