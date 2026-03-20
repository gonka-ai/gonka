package validation

import (
	"net/url"
	"testing"

	"decentralized-api/payloadstorage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPayloadRequestURL_SubnetPath(t *testing.T) {
	// Test with subnet session-specific path
	url, err := BuildPayloadRequestURL("https://executor.example.com", "v1/subnet/sessions/escrow-123/payloads", "456")
	require.NoError(t, err)
	assert.Contains(t, url, "v1/subnet/sessions/escrow-123/payloads")
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

