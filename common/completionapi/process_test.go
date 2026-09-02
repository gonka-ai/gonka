package completionapi

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type discardProcessor struct{}

func (discardProcessor) ProcessJsonResponse(responseBytes []byte) ([]byte, error) {
	return responseBytes, nil
}

func (discardProcessor) ProcessStreamedResponse(line string) (string, error) { return line, nil }

func (discardProcessor) GetResponseBytes() ([]byte, error) { return nil, nil }

func getResponse(t *testing.T, handler http.HandlerFunc) *http.Response {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	resp, err := server.Client().Get(server.URL)
	require.NoError(t, err)
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestProcessHTTPResponse_OversizedJSONRejected(t *testing.T) {
	resp := getResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "application/json")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gz.Write([]byte(`{"id":"inf-1","pad":"`))
		chunk := bytes.Repeat([]byte("a"), 64<<10)
		for written := 0; written <= MaxResponseBytes; written += len(chunk) {
			if _, err := gz.Write(chunk); err != nil {
				return
			}
		}
		gz.Write([]byte(`"}`))
	})

	err := ProcessHTTPResponse(resp, discardProcessor{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestProcessHTTPResponse_OversizedSSERejected(t *testing.T) {
	resp := getResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Type", "text/event-stream")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		line := fmt.Sprintf("data: {\"id\":\"inf-1\",\"pad\":\"%s\"}\n", strings.Repeat("a", 32<<10))
		for written := 0; written <= MaxResponseBytes; written += len(line) {
			if _, err := gz.Write([]byte(line)); err != nil {
				return
			}
		}
	})

	err := ProcessHTTPResponse(resp, discardProcessor{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResponseTooLarge)
}

func TestProcessHTTPResponse_JSONUnderCapDecodes(t *testing.T) {
	resp := getResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":"original","object":"chat.completion"}`))
	})

	processor := NewExecutorResponseProcessor("inf-1")
	require.NoError(t, ProcessHTTPResponse(resp, processor))

	body, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.Contains(t, string(body), `"id":"inf-1"`)
	assert.NotContains(t, string(body), "original")
}

func TestProcessHTTPResponse_SSEStreamDecodes(t *testing.T) {
	resp := getResponse(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"id\":\"original\",\"choices\":[]}\n\ndata: [DONE]\n\n"))
	})

	processor := NewExecutorResponseProcessor("inf-1")
	require.NoError(t, ProcessHTTPResponse(resp, processor))

	body, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.Contains(t, string(body), "inf-1")
	assert.Contains(t, string(body), "[DONE]")
	assert.NotContains(t, string(body), "original")
}
