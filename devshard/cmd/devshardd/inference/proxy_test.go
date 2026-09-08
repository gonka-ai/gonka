package inference

import (
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/completionapi"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type discardWriter struct {
	header http.Header
	status int
}

func newDiscardWriter() *discardWriter { return &discardWriter{header: http.Header{}} }

func (d *discardWriter) Header() http.Header { return d.header }

func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func (d *discardWriter) WriteHeader(status int) { d.status = status }

type passthroughProcessor struct{}

func (passthroughProcessor) ProcessJsonResponse(responseBytes []byte) ([]byte, error) {
	return responseBytes, nil
}

func (passthroughProcessor) ProcessStreamedResponse(line string) (string, error) { return line, nil }

func (passthroughProcessor) GetResponseBytes() ([]byte, error) { return nil, nil }

func proxyTestResponse(t *testing.T, handler http.HandlerFunc) *http.Response {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestProxyResponse_RejectsNonSSE(t *testing.T) {
	resp := proxyTestResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"original","object":"chat.completion"}`))
	})

	rec := httptest.NewRecorder()
	err := proxyResponse(resp, rec, true, nil, "inf-1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected content type")
	assert.Empty(t, rec.Body.String())
	assert.Empty(t, rec.Header().Get("Content-Type"))
}

func TestProxyResponse_OversizedStreamRejected(t *testing.T) {
	resp := proxyTestResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/event-stream")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		line := fmt.Sprintf("data: {\"id\":\"original\",\"pad\":\"%s\"}\n", strings.Repeat("a", 32<<10))
		for written := 0; written <= completionapi.MaxResponseBytes; written += len(line) {
			if _, err := gz.Write([]byte(line)); err != nil {
				return
			}
		}
	})

	err := proxyResponse(resp, newDiscardWriter(), true, passthroughProcessor{}, "inf-1")

	require.Error(t, err)
	assert.ErrorIs(t, err, completionapi.ErrResponseTooLarge)
}

func TestProxyResponse_StreamProxied(t *testing.T) {
	resp := proxyTestResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"id\":\"original\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	})

	rec := httptest.NewRecorder()
	err := proxyResponse(resp, rec, true, completionapi.NewExecutorResponseProcessor("inf-1", false), "inf-1")

	require.NoError(t, err)
	assert.Equal(t, "text/event-stream", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Body.String(), "inf-1")
	assert.Contains(t, rec.Body.String(), "[DONE]")
}
