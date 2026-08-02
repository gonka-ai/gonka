package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A draining instance must report unready without touching the chain: the
// answer no longer depends on chain reachability, and probing would only delay
// the very check the balancer is waiting on. The nil client is the assertion —
// any chain call here would panic.
func TestReadyz_ReportsDrainingWithoutProbingTheChain(t *testing.T) {
	r := newReadiness(nil)
	r.beginDrain()

	rec := serveReadyz(t, r)

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"reason":"draining"`)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

// Draining latches: readiness must not come back while the process is on its
// way out, or the balancer would route to it again mid-shutdown.
func TestReadyz_DrainingLatches(t *testing.T) {
	r := newReadiness(nil)
	r.beginDrain()

	for i := 0; i < 3; i++ {
		require.Equal(t, http.StatusServiceUnavailable, serveReadyz(t, r).Code)
	}
}

func TestBeginDrain_FlipsReadinessOnTheServer(t *testing.T) {
	srv := New(nil)
	srv.BeginDrain()

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), `"reason":"draining"`)
}

func serveReadyz(t *testing.T, r *readiness) *httptest.ResponseRecorder {
	t.Helper()
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/readyz", nil), rec)
	require.NoError(t, r.handler(c))
	return rec
}
