package main

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"common/probe"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestPingEndpointReturns204AndHeaders(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/clock", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	recv, err := strconv.ParseInt(rec.Header().Get(probe.HeaderServerRecvNs), 10, 64)
	require.NoError(t, err)
	send, err := strconv.ParseInt(rec.Header().Get(probe.HeaderServerSendNs), 10, 64)
	require.NoError(t, err)
	require.Greater(t, recv, int64(0))
	require.GreaterOrEqual(t, send, recv)
	require.Empty(t, rec.Body.Bytes())
}

func TestHealthzUnchangedAlongsidePing(t *testing.T) {
	lifecycle := newLifecycleState()
	e := buildServer(lifecycle)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "ok", rec.Body.String())
}

func TestPingBypassesLifecycleInflight(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)

	before := lifecycle.Status().Inflight
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/clock", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.Equal(t, before, lifecycle.Status().Inflight)
}

func TestPingAnswersDuringDrain(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)
	admin := buildAdminServer(lifecycle, func() bool { return true }, nil, recoveryDone)
	e.GET("/work", func(c echo.Context) error {
		return c.String(http.StatusOK, "done")
	})

	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.True(t, lifecycle.Status().Draining)

	// Reachability is not readiness: /clock must keep answering while draining.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/clock", nil))
	require.Equal(t, http.StatusNoContent, rec.Code)
	require.NotEmpty(t, rec.Header().Get(probe.HeaderServerRecvNs))

	// Non-bypass work is still rejected.
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
}
