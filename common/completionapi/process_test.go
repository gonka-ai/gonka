package completionapi

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Three packages branch on this answer; one that disagrees with the processor waits for a body it never gets.
func TestIsEventStream(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		contentType string
		want        bool
	}{
		{name: "a stream", contentType: "text/event-stream", want: true},
		{name: "a stream naming its charset", contentType: "text/event-stream; charset=utf-8", want: true},
		{name: "json", contentType: "application/json"},
		{name: "no content type at all"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			response := &http.Response{Header: http.Header{}}
			if testCase.contentType != "" {
				response.Header.Set("Content-Type", testCase.contentType)
			}
			require.Equal(t, testCase.want, IsEventStream(response))
		})
	}
}

// A long prompt makes one line far longer than the scanner's default token.
func TestProcessSSEReadsALineLongerThanTheScannerDefault(t *testing.T) {
	ids := strings.TrimPrefix(strings.Repeat(",163586", 100_000), ",")
	line := `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m",` +
		`"prompt_token_ids":[` + ids + `],"choices":[{"index":0,"delta":{"role":"assistant"}}],` +
		`"usage":{"prompt_tokens":100000,"completion_tokens":1}}`
	require.Greater(t, len(line), 64*1024, "fixture must exceed the default scanner token")

	processor := NewExecutorResponseProcessor("devshard-1-1", false)

	require.NoError(t, processSSE(strings.NewReader(line+"\n"), processor))

	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	require.NotEmpty(t, stored)
}

// A line past the bound must reach the caller as an error.
func TestProcessSSERefusesALineBeyondTheBound(t *testing.T) {
	line := "data: " + strings.Repeat("x", MaxSSELineBytes+1)

	err := processSSE(strings.NewReader(line+"\n"), NewExecutorResponseProcessor("devshard-1-1", false))

	require.Error(t, err)
}

func TestProcessHTTPResponseSurfacesAReadFailure(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(failingReader{}),
	}

	err := ProcessHTTPResponse(resp, NewExecutorResponseProcessor("devshard-1-1", false))

	require.ErrorIs(t, err, errReadFailed)
}

var errReadFailed = errors.New("connection reset by peer")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errReadFailed }

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

	processor := NewExecutorResponseProcessor("inf-1", false)
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

	processor := NewExecutorResponseProcessor("inf-1", false)
	require.NoError(t, ProcessHTTPResponse(resp, processor))

	body, err := processor.GetResponseBytes()
	require.NoError(t, err)
	assert.Contains(t, string(body), "inf-1")
	assert.Contains(t, string(body), "[DONE]")
	assert.NotContains(t, string(body), "original")
}
