//go:build devshard_testenv

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Point the fault directory at a temp dir and build the public and admin servers with a ready lifecycle.
// 2. With no trip file, require /healthz and a session route to pass through.
// 3. Create the trip file and require 503 on every public route, including an unknown one, while admin /ready stays 200.
// 4. Remove the trip file and require the public routes to pass through again.
func TestPublicFaultDownTripFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(envTestenvPublicFaultDir, dir)
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)
	e.POST("/rpc/work", func(c echo.Context) error {
		return c.String(http.StatusOK, "done")
	})
	admin := buildAdminServer(lifecycle, func() bool { return true }, nil, recoveryDone, nil)
	serve := func(handler http.Handler, method, path string) int {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}

	require.Equal(t, http.StatusOK, serve(e, http.MethodGet, "/healthz"))
	require.Equal(t, http.StatusOK, serve(e, http.MethodPost, "/rpc/work"))

	trip := filepath.Join(dir, PublicFaultDownFile)
	require.NoError(t, os.WriteFile(trip, nil, 0o600))
	for _, route := range [][2]string{{http.MethodGet, "/healthz"}, {http.MethodPost, "/rpc/work"}, {http.MethodGet, "/clock"}, {http.MethodGet, "/metrics"}, {http.MethodGet, "/missing"}} {
		require.Equal(t, http.StatusServiceUnavailable, serve(e, route[0], route[1]), route[1])
	}
	require.Equal(t, http.StatusOK, serve(admin, http.MethodGet, "/ready"))

	require.NoError(t, os.Remove(trip))
	require.Equal(t, http.StatusOK, serve(e, http.MethodGet, "/healthz"))
	require.Equal(t, http.StatusOK, serve(e, http.MethodPost, "/rpc/work"))
}

// Test flow:
// 1. Leave the fault directory unset so the default /tmp location is used.
// 2. Require publicFaultFile to resolve to the trip file name under /tmp.
func TestPublicFaultDefaultDir(t *testing.T) {
	t.Setenv(envTestenvPublicFaultDir, "")
	require.Equal(t, filepath.Join("/tmp", PublicFaultDownFile), publicFaultFile())
}
