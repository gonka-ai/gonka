package probe_test

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"common/probe"

	"github.com/stretchr/testify/require"
)

func TestHandler_HeadersAndStatus(t *testing.T) {
	fixed := time.Date(2026, 8, 11, 12, 0, 0, 123456789, time.UTC)
	var n int
	h := probe.Handler(func() time.Time {
		n++
		if n == 1 {
			return fixed
		}
		return fixed.Add(time.Microsecond)
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/clock", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	recv, err := strconv.ParseInt(rec.Header().Get(probe.HeaderServerRecvNs), 10, 64)
	require.NoError(t, err)
	send, err := strconv.ParseInt(rec.Header().Get(probe.HeaderServerSendNs), 10, 64)
	require.NoError(t, err)
	require.Equal(t, fixed.UnixNano(), recv)
	require.GreaterOrEqual(t, send, recv)
	require.Empty(t, rec.Body.Bytes())
}

func BenchmarkHandler(b *testing.B) {
	h := probe.Handler(nil)
	req := httptest.NewRequest(http.MethodGet, "/clock", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}
}
