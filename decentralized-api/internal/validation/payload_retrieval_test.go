package validation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"decentralized-api/payloadstorage"
	devshardpkg "devshard"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPayloadRequestURL_DevshardPath(t *testing.T) {
	// Test with devshard session-specific path
	url, err := BuildPayloadRequestURL("https://executor.example.com", devshardpkg.LegacySessionPayloadPath("escrow-123"), "456")
	require.NoError(t, err)
	assert.Contains(t, url, devshardpkg.LegacySessionPayloadPath("escrow-123"))
	assert.Contains(t, url, "inference_id=456")
}

func TestBuildPayloadRequestURL_VersionedDevshardPath(t *testing.T) {
	url, err := BuildPayloadRequestURL("https://executor.example.com", devshardpkg.VersionedSessionPayloadPath("v1", "escrow-123"), "456")
	require.NoError(t, err)
	assert.Contains(t, url, devshardpkg.VersionedSessionPayloadPath("v1", "escrow-123"))
	assert.Contains(t, url, "inference_id=456")
}

func TestBuildPayloadRequestURL_PublicPath(t *testing.T) {
	// Test with public endpoint path
	url, err := BuildPayloadRequestURL("https://executor.example.com", "v1/inference/payloads", "test-id")
	require.NoError(t, err)
	assert.Contains(t, url, "v1/inference/payloads")
	assert.Contains(t, url, "inference_id=test-id")
}

func TestVerifyPayloadHashes_Valid(t *testing.T) {
	promptPayload := []byte(`{"model":"test","messages":[]}`)
	responsePayload := []byte(`{"choices":[]}`)

	expectedPromptHash, err := payloadstorage.ComputePromptHash(promptPayload)
	require.NoError(t, err)
	expectedResponseHash, err := payloadstorage.ComputeResponseHash(responsePayload)
	require.NoError(t, err)

	err = VerifyPayloadHashes(promptPayload, responsePayload, expectedPromptHash, expectedResponseHash, "inf-1")
	assert.NoError(t, err)
}

func TestVerifyPayloadHashes_EmptyExpectedHashes(t *testing.T) {
	// Empty expected hashes should pass (backward compatibility)
	err := VerifyPayloadHashes([]byte("prompt"), []byte("response"), "", "", "inf-1")
	assert.NoError(t, err)
}

func TestVerifyPayloadHashes_PromptMismatch(t *testing.T) {
	promptPayload := []byte(`{"model":"test"}`)
	responsePayload := []byte(`{"choices":[]}`)

	expectedResponseHash, err := payloadstorage.ComputeResponseHash(responsePayload)
	require.NoError(t, err)

	// Use wrong prompt hash
	err = VerifyPayloadHashes(promptPayload, responsePayload, "wrong-hash", expectedResponseHash, "inf-1")
	assert.ErrorIs(t, err, ErrHashMismatch)
}

func TestVerifyPayloadHashes_ResponseMismatch(t *testing.T) {
	promptPayload := []byte(`{"model":"test"}`)
	responsePayload := []byte(`{"choices":[]}`)

	expectedPromptHash, err := payloadstorage.ComputePromptHash(promptPayload)
	require.NoError(t, err)

	// Use wrong response hash
	err = VerifyPayloadHashes(promptPayload, responsePayload, expectedPromptHash, "wrong-hash", "inf-1")
	assert.ErrorIs(t, err, ErrHashMismatch)
}

// TestFetchPayloadsHTTP_RejectsOversizedResponse pins the OOM-defense: a
// malicious executor that streams more than MaxPayloadResponseSize bytes must
// be rejected before json.Unmarshal allocates the whole body.
func TestFetchPayloadsHTTP_RejectsOversizedResponse(t *testing.T) {
	origLimit := MaxPayloadResponseSize
	MaxPayloadResponseSize = 1024 // 1 KiB cap for the test
	defer func() { MaxPayloadResponseSize = origLimit }()

	// Body is well-formed JSON-shape but exceeds the cap due to a large
	// prompt_payload string. The test cares about the size check firing,
	// not about JSON validity.
	oversizedField := strings.Repeat("a", int(MaxPayloadResponseSize+128))
	body := `{"inference_id":"test","prompt_payload":"` + oversizedField + `"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := FetchPayloadsHTTP(context.Background(), client, server.URL, "validator", 0, 0, "sig")
	require.Nil(t, resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "exceeds maximum size")
}

// TestFetchPayloadsHTTP_AcceptsResponseUnderLimit pins that the size guard
// does not break the happy path: a well-formed response under the cap must
// still decode normally.
func TestFetchPayloadsHTTP_AcceptsResponseUnderLimit(t *testing.T) {
	origLimit := MaxPayloadResponseSize
	MaxPayloadResponseSize = 1024 // 1 KiB cap for the test
	defer func() { MaxPayloadResponseSize = origLimit }()

	expected := PayloadResponse{
		InferenceId:       "inf-1",
		PromptPayload:     []byte("hello"),
		ResponsePayload:   []byte("world"),
		ExecutorSignature: "sig",
	}
	bodyBytes, err := json.Marshal(expected)
	require.NoError(t, err)
	require.Less(t, int64(len(bodyBytes)), MaxPayloadResponseSize)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(bodyBytes)
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	got, err := FetchPayloadsHTTP(context.Background(), client, server.URL, "validator", 0, 0, "sig")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, expected.InferenceId, got.InferenceId)
	require.Equal(t, expected.PromptPayload, got.PromptPayload)
	require.Equal(t, expected.ResponsePayload, got.ResponsePayload)
	require.Equal(t, expected.ExecutorSignature, got.ExecutorSignature)
}

// TestFetchPayloadsHTTP_BoundsErrorBody pins that even the non-2xx error path
// no longer reads an unbounded body when an executor returns an error status
// with a giant body.
func TestFetchPayloadsHTTP_BoundsErrorBody(t *testing.T) {
	// 1 MiB of "x" so the test catches a bug that would have ReadAll'd the
	// whole thing into the error string.
	big := strings.Repeat("x", 1024*1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(big))
	}))
	defer server.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := FetchPayloadsHTTP(context.Background(), client, server.URL, "validator", 0, 0, "sig")
	require.Nil(t, resp)
	require.Error(t, err)
	require.Contains(t, err.Error(), "executor returned status 502")
	// Error string must not embed the full 1 MiB; the snippet is bounded by
	// maxPayloadErrorBodySize (4 KiB) plus the fixed prefix.
	require.Less(t, len(err.Error()), maxPayloadErrorBodySize+256)
}

func TestBuildPayloadRequestURL(t *testing.T) {
	tests := []struct {
		name        string
		executorUrl string
		inferenceId string
		wantQuery   string
	}{
		{
			name:        "simple base64 ID",
			executorUrl: "https://executor.example.com",
			inferenceId: "aW5mZXJlbmNlLTEyMzQ1",
			wantQuery:   "inference_id=aW5mZXJlbmNlLTEyMzQ1",
		},
		{
			name:        "base64 ID with slash",
			executorUrl: "https://executor.example.com",
			inferenceId: "abc/def/ghi",
			wantQuery:   "inference_id=abc%2Fdef%2Fghi",
		},
		{
			name:        "base64 ID with plus",
			executorUrl: "https://executor.example.com",
			inferenceId: "abc+def+ghi",
			wantQuery:   "inference_id=abc%2Bdef%2Bghi",
		},
		{
			name:        "base64 ID with slash and plus",
			executorUrl: "https://executor.example.com",
			inferenceId: "a/b+c/d+e",
			wantQuery:   "inference_id=a%2Fb%2Bc%2Fd%2Be",
		},
		{
			name:        "base64 ID with equals padding",
			executorUrl: "https://executor.example.com",
			inferenceId: "dGVzdA==",
			wantQuery:   "inference_id=dGVzdA%3D%3D",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			baseUrl, err := url.JoinPath(tt.executorUrl, "v1/inference/payloads")
			require.NoError(t, err)

			parsedUrl, err := url.Parse(baseUrl)
			require.NoError(t, err)

			query := parsedUrl.Query()
			query.Set("inference_id", tt.inferenceId)
			parsedUrl.RawQuery = query.Encode()

			result := parsedUrl.String()

			require.Contains(t, result, "v1/inference/payloads")
			require.Contains(t, result, tt.wantQuery)

			// Verify URL can be parsed and query param decoded correctly
			parsedResult, err := url.Parse(result)
			require.NoError(t, err)
			decodedId := parsedResult.Query().Get("inference_id")
			require.Equal(t, tt.inferenceId, decodedId)
		})
	}
}
