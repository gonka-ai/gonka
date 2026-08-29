package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildPayloadRequestURL_DevshardPath(t *testing.T) {
	// Test with devshard session-specific path
	url, err := BuildPayloadRequestURL("https://executor.example.com", "escrow-123", "456")
	require.NoError(t, err)
	assert.Contains(t, url, "escrow-123")
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

	expectedPromptHash, err := computePromptHash(promptPayload)
	require.NoError(t, err)
	expectedResponseHash, err := computeResponseHash(responsePayload)
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

	expectedResponseHash, err := computeResponseHash(responsePayload)
	require.NoError(t, err)

	// Use wrong prompt hash
	err = VerifyPayloadHashes(promptPayload, responsePayload, "wrong-hash", expectedResponseHash, "inf-1")
	assert.ErrorIs(t, err, ErrHashMismatch)
}

func TestVerifyPayloadHashes_ResponseMismatch(t *testing.T) {
	promptPayload := []byte(`{"model":"test"}`)
	responsePayload := []byte(`{"choices":[]}`)

	expectedPromptHash, err := computePromptHash(promptPayload)
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

func TestFetchPayloadsHTTP_NotFoundReturnsErrPayloadGone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	t.Cleanup(server.Close)

	_, err := FetchPayloadsHTTP(
		context.Background(),
		server.Client(),
		server.URL+"?inference_id=inf-1",
		"gonka1validator",
		1,
		4,
		"sig",
		0,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPayloadGone)
}

func TestFetchPayloadsHTTP_ErrorBodyIsBoundedAndQuoted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("line one\nline two"))
		_, _ = w.Write(bytes.Repeat([]byte("x"), 4*maxPayloadErrorBodyBytes))
	}))
	t.Cleanup(server.Close)

	_, err := FetchPayloadsHTTP(
		context.Background(), server.Client(), server.URL, "gonka1validator", 1, 4, "sig", 0,
	)
	require.Error(t, err)
	// Bounded so a withholding executor cannot blow up validator memory or logs,
	// and quoted so it cannot forge log lines.
	assert.LessOrEqual(t, len(err.Error()), maxPayloadErrorQuotedBytes+64)
	assert.NotContains(t, err.Error(), "line one\nline two")
	assert.Contains(t, err.Error(), `line one\nline two`)
}

func TestQuotedSnippet_BoundsEscapedLength(t *testing.T) {
	t.Parallel()
	// Escaping binary expands ~4x, so the raw read cap alone is not a bound.
	binary := bytes.Repeat([]byte{0x00}, maxPayloadErrorBodyBytes)
	got := quotedSnippet(binary)
	assert.LessOrEqual(t, len(got), maxPayloadErrorQuotedBytes+4)

	short := quotedSnippet([]byte("upstream connect error"))
	assert.Equal(t, `"upstream connect error"`, short)
}

func TestCappedReader_ReturnsErrPayloadTooLarge(t *testing.T) {
	t.Parallel()
	r := &cappedReader{r: bytes.NewReader(bytes.Repeat([]byte("a"), 100)), remaining: 10}
	_, err := io.ReadAll(r)
	require.ErrorIs(t, err, ErrPayloadTooLarge)
}

func TestFetchPayloadsHTTP_OversizedResponseRejected(t *testing.T) {
	// Decode through a cappedReader sized to the real body so the cap, not a
	// truncated JSON document, is what fails.
	body := []byte(`{"inference_id":"1","prompt_payload":"YQ=="}`)
	var resp PayloadResponse
	err := json.NewDecoder(&cappedReader{r: bytes.NewReader(body), remaining: int64(len(body) - 5)}).Decode(&resp)
	require.ErrorIs(t, err, ErrPayloadTooLarge)
}

func TestPayloadResponseByteLimit_DefaultGatewayCapFits(t *testing.T) {
	t.Parallel()
	// Shipped RequestMaxTokensCap is 4096 (devshardctl/gateway.go). A derived
	// cap below the old 64 MiB constant means default traffic cannot be
	// false-invalidated by the read limit.
	const defaultRequestMaxTokensCap = 4096
	limit := PayloadResponseByteLimit(defaultRequestMaxTokensCap)
	assert.LessOrEqual(t, limit, int64(MaxPayloadResponseBytes))
	// The flat prompt allowance is always included, so even a zero-token
	// response carries the full gateway body cap.
	assert.Greater(t, limit, int64(maxPromptPayloadBytes))
}

func TestPayloadResponseByteLimit_ScalesWithOutputTokens(t *testing.T) {
	t.Parallel()
	small := PayloadResponseByteLimit(32)
	large := PayloadResponseByteLimit(16_384)
	assert.Greater(t, large, small)
	assert.Equal(t, int64(maxPayloadResponseBytesHard), PayloadResponseByteLimit(200_000))
}

// A claimed output-token count near the uint64 ceiling must clip to the hard
// bound. Multiplying it out overflows int64 and used to produce a cap *smaller*
// than a modest request, which would false-invalidate an honest payload.
func TestPayloadResponseByteLimit_HugeTokenCountsClipAndStayMonotonic(t *testing.T) {
	t.Parallel()
	modest := PayloadResponseByteLimit(4096)
	for _, out := range []uint64{
		65_537,
		1 << 30,
		1 << 50,
		1 << 62,
		^uint64(0),
	} {
		got := PayloadResponseByteLimit(out)
		assert.Equal(t, int64(maxPayloadResponseBytesHard), got, "outputTokens=%d must clip", out)
		assert.Greater(t, got, modest, "outputTokens=%d must not undercut a modest request", out)
	}
}

func TestPayloadReadLimit_ZeroUsesDefault(t *testing.T) {
	t.Parallel()
	assert.Equal(t, int64(MaxPayloadResponseBytes), payloadReadLimit(0))
	assert.Equal(t, int64(1<<20), payloadReadLimit(1<<20))
	assert.Equal(t, int64(maxPayloadResponseBytesHard), payloadReadLimit(maxPayloadResponseBytesHard+1))
}
