package transport

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/internal/testutil"
)

const compressionTestEscrowID = "escrow-compression"

// capturedRequest is what one signed POST put on the wire.
type capturedRequest struct {
	contentEncoding string
	wire            []byte
	signature       string
	timestamp       string
	readErr         error
}

func captureOnePost(t *testing.T) (*httptest.Server, *capturedRequest) {
	t.Helper()
	captured := &capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Not require: FailNow off the test goroutine hangs the client.
		body, err := io.ReadAll(r.Body)
		captured.readErr = err
		captured.contentEncoding = r.Header.Get("Content-Encoding")
		captured.signature = r.Header.Get(HeaderSignature)
		captured.timestamp = r.Header.Get(HeaderTimestamp)
		captured.wire = body
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	return server, captured
}

// Go asks for gzip and unwraps it, so the parser never sees an encoded byte.
func TestParseSSE_ReadsACompressedStreamFromTheHost(t *testing.T) {
	router := echo.New()
	router.HideBanner = true
	router.GET("/stream", func(c echo.Context) error {
		response := c.Response()
		response.Header().Set("Content-Type", "text/event-stream")
		response.WriteHeader(http.StatusOK)
		for _, frame := range []string{"data: " + strings.Repeat("alpha ", 500) + "\n\n", "data: [DONE]\n\n"} {
			if _, err := response.Write([]byte(frame)); err != nil {
				return err
			}
			response.Flush()
		}
		return nil
	}, ResponseCompressionMiddleware)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)

	response, err := http.Get(server.URL + "/stream")
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })
	require.True(t, response.Uncompressed, "the host must have answered gzip and the transport unwrapped it")

	client := &HTTPClient{escrowID: compressionTestEscrowID, config: DefaultClientConfig()}
	var forwarded bytes.Buffer
	_, err = client.parseSSEResponse(context.Background(), response.Body, &forwarded, nil)

	require.NoError(t, err)
	require.Contains(t, forwarded.String(), "alpha")
	require.Contains(t, forwarded.String(), "[DONE]")
}

func TestHTTPClient_Post_StaysUncompressedUntilItIsTurnedOn(t *testing.T) {
	signer := testutil.MustGenerateKey(t)
	server, captured := captureOnePost(t)

	client := NewHTTPClient(server.URL, compressionTestEscrowID, signer, DefaultClientConfig())

	body := []byte(`{"diffs":[],"nonce":7,"payload":{"prompt":"` + strings.Repeat("A", 8192) + `"}}`)
	response, err := client.doPostRawOnce(context.Background(), "/sessions/1/chat/completions", body, "application/octet-stream", false)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())

	require.Empty(t, captured.contentEncoding,
		"phase 7 retired request-body gzip; the POST body is the signed bytes")
	require.Equal(t, string(body), string(captured.wire))
}
