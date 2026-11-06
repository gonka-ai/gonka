package testutil

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

// FlushRecorder keeps what had reached the wire at each Flush.
type FlushRecorder struct {
	headers     http.Header
	body        []byte
	flushes     [][]byte
	code        int
	wroteHeader bool
}

func NewFlushRecorder() *FlushRecorder {
	return &FlushRecorder{headers: http.Header{}, code: http.StatusOK}
}

func (r *FlushRecorder) Header() http.Header { return r.headers }

func (r *FlushRecorder) Write(p []byte) (int, error) {
	r.body = append(r.body, p...)
	return len(p), nil
}

// WriteHeader keeps the first code, as httptest.ResponseRecorder does.
func (r *FlushRecorder) WriteHeader(code int) {
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.code = code
}

func (r *FlushRecorder) Flush() {
	r.flushes = append(r.flushes, append([]byte(nil), r.body...))
}

// Code is the status the handler wrote.
func (r *FlushRecorder) Code() int { return r.code }

// Body is everything written, in order.
func (r *FlushRecorder) Body() []byte { return r.body }

// Flushes is one snapshot of the body per Flush, in order.
func (r *FlushRecorder) Flushes() [][]byte { return r.flushes }

// GzipDecodeSoFar returns what a still-open gzip stream decodes to.
func GzipDecodeSoFar(t *testing.T, compressed []byte) string {
	t.Helper()
	if len(compressed) == 0 {
		return ""
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return ""
	}
	defer reader.Close()
	var decoded bytes.Buffer
	if _, err := io.Copy(&decoded, reader); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		require.NoError(t, err)
	}
	return decoded.String()
}
