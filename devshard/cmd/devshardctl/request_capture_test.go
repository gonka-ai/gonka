package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"devshard/host"
	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestPrepareChatRequestBodyCapturesFilterRejectedRequest(t *testing.T) {
	captureDir := t.TempDir()
	setRequestCaptureStore(&requestCaptureStore{dir: captureDir})
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{
		"model": "Qwen/Test",
		"temperature": 0.7,
		"unsupported_field": true,
		"messages": [{"role": "user", "content": "hello"}]
	}`))
	ctx, requestID := ensureRequestLogContext(req.Context())
	req = req.WithContext(ctx)

	_, _, err := prepareChatRequestBody(req)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported_field")

	record := requireSingleCapturedRequest(t, captureDir, "filter_rejected")
	require.Equal(t, requestID, record.RequestID)
	require.Equal(t, "filter_rejected", record.Kind)
	require.Equal(t, "Qwen/Test", record.Model)
	require.Equal(t, "/v1/chat/completions", record.Path)
	require.Contains(t, string(record.Body), `"unsupported_field": true`)
	require.Empty(t, record.BodyBase64)
}

func TestCaptureAllAttemptsFailedRequestWritesSeparateFile(t *testing.T) {
	captureDir := t.TempDir()
	setRequestCaptureStore(&requestCaptureStore{dir: captureDir})
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	ctx, requestID := ensureRequestLogContext(t.Context())
	captureAllAttemptsFailedRequest(ctx, "escrow-7", user.InferenceParams{
		Model:       "Qwen/Test",
		Prompt:      []byte(`{"model":"Qwen/Test","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		InputLength: 81,
		MaxTokens:   256,
		StartedAt:   time.Now().Unix(),
		Stream:      true,
	}, errTestAllAttemptsFailed{})

	record := requireSingleCapturedRequest(t, captureDir, "all_attempts_failed")
	require.Equal(t, requestID, record.RequestID)
	require.Equal(t, "all_attempts_failed", record.Kind)
	require.Equal(t, "Qwen/Test", record.Model)
	require.Equal(t, "escrow-7", record.Escrow)
	require.True(t, record.Stream)
	require.Contains(t, record.Error, "all attempts failed")
	require.Contains(t, string(record.Body), `"stream": true`)
}

func TestCaptureEmptyStreamAttemptRequestWritesSeparateFileWithAttempts(t *testing.T) {
	captureDir := t.TempDir()
	setRequestCaptureStore(&requestCaptureStore{dir: captureDir})
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	ctx, requestID := ensureRequestLogContext(t.Context())
	attempts := []*inflight{
		{
			hostIdx:                          2,
			hostID:                           "host-empty",
			escrowID:                         "escrow-7",
			nonce:                            11,
			receiptTime:                      time.Now(),
			resp:                             &host.HostResponse{ConfirmedAt: 123, Receipt: []byte("receipt"), StreamBytesRead: 14},
			err:                              errEmptyStream,
			emptyResponseBodySample:          "data: [DONE]\n\n",
			emptyResponseBodySampleTruncated: false,
		},
		{
			hostIdx:  3,
			hostID:   "host-ok",
			escrowID: "escrow-7",
			nonce:    12,
			resp:     &host.HostResponse{ConfirmedAt: 124, Receipt: []byte("receipt"), StreamBytesRead: 64},
			err:      nil,
		},
	}
	attempts[0].outputBytes.Store(14)
	attempts[1].outputChunks.Store(1)
	attempts[1].contentChunks.Store(1)
	attempts[1].outputBytes.Store(64)

	captureEmptyStreamAttemptRequest(ctx, "escrow-7", user.InferenceParams{
		Model:  "Qwen/Test",
		Prompt: []byte(`{"model":"Qwen/Test","stream":true,"messages":[{"role":"user","content":"hello"}]}`),
		Stream: true,
	}, attempts, 12)

	record := requireSingleCapturedRequest(t, captureDir, "empty_stream_attempt")
	require.Equal(t, requestID, record.RequestID)
	require.Equal(t, "empty_stream_attempt", record.Kind)
	require.Equal(t, "Qwen/Test", record.Model)
	require.Equal(t, "escrow-7", record.Escrow)
	require.True(t, record.Stream)
	require.Contains(t, string(record.Body), `"stream": true`)
	require.NotEmpty(t, record.RequestFlags)
	require.Len(t, record.Attempts, 2)
	require.True(t, record.Attempts[0].EmptyStream)
	require.Equal(t, "host-empty", record.Attempts[0].Host)
	require.Equal(t, "data: [DONE]\n\n", record.Attempts[0].ResponseBodySample)
	require.False(t, record.Attempts[0].Winner)
	require.True(t, record.Attempts[1].Winner)
	require.False(t, record.Attempts[1].EmptyStream)
}

func TestConfigureRequestCaptureStoreDisabledByDefault(t *testing.T) {
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_DIR", "")
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_ENABLED", "")
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	configureRequestCaptureStore(t.TempDir())

	require.Nil(t, currentRequestCaptureStore())
}

func TestConfigureRequestCaptureStoreDefaultsUnderGatewayDBDirectory(t *testing.T) {
	baseStorageDir := t.TempDir()
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_ENABLED", "true")
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_DIR", "")
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	configureRequestCaptureStore(baseStorageDir)

	store := currentRequestCaptureStore()
	require.NotNil(t, store)
	require.Equal(t, filepath.Join(baseStorageDir, requestCaptureDirName), store.dir)
}

func TestConfigureRequestCaptureStoreUsesExplicitDir(t *testing.T) {
	captureDir := t.TempDir()
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_ENABLED", "true")
	t.Setenv("DEVSHARD_REQUEST_CAPTURE_DIR", captureDir)
	t.Cleanup(func() { setRequestCaptureStore(nil) })

	configureRequestCaptureStore(t.TempDir())

	store := currentRequestCaptureStore()
	require.NotNil(t, store)
	require.Equal(t, captureDir, store.dir)
}

func requireSingleCapturedRequest(t *testing.T, captureDir, kind string) capturedChatRequest {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(captureDir, kind))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	body, err := os.ReadFile(filepath.Join(captureDir, kind, entries[0].Name()))
	require.NoError(t, err)
	var record capturedChatRequest
	require.NoError(t, json.Unmarshal(body, &record))
	return record
}

type errTestAllAttemptsFailed struct{}

func (errTestAllAttemptsFailed) Error() string {
	return "all attempts failed"
}
