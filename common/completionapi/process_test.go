package completionapi

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A long prompt makes one line far longer than the scanner's default token.
func TestProcessSSEReadsALineLongerThanTheScannerDefault(t *testing.T) {
	ids := strings.TrimPrefix(strings.Repeat(",163586", 100_000), ",")
	line := `data: {"id":"c","object":"chat.completion.chunk","created":1,"model":"m",` +
		`"prompt_token_ids":[` + ids + `],"choices":[{"index":0,"delta":{"role":"assistant"}}],` +
		`"usage":{"prompt_tokens":100000,"completion_tokens":1}}`
	require.Greater(t, len(line), 64*1024, "fixture must exceed the default scanner token")

	processor := NewExecutorResponseProcessor("devshard-1-1")

	require.NoError(t, processSSE(strings.NewReader(line+"\n"), processor))

	stored, err := processor.GetResponseBytes()
	require.NoError(t, err)
	require.NotEmpty(t, stored)
}

// A line past the bound must reach the caller as an error.
func TestProcessSSERefusesALineBeyondTheBound(t *testing.T) {
	line := "data: " + strings.Repeat("x", MaxSSELineBytes+1)

	err := processSSE(strings.NewReader(line+"\n"), NewExecutorResponseProcessor("devshard-1-1"))

	require.Error(t, err)
}

func TestProcessHTTPResponseSurfacesAReadFailure(t *testing.T) {
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(failingReader{}),
	}

	err := ProcessHTTPResponse(resp, NewExecutorResponseProcessor("devshard-1-1"))

	require.ErrorIs(t, err, errReadFailed)
}

var errReadFailed = errors.New("connection reset by peer")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errReadFailed }
