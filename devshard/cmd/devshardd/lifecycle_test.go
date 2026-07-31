package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleReadyAndDrainStatus(t *testing.T) {
	lifecycle := newLifecycleState()
	e := buildServer(lifecycle)
	admin := buildAdminServer(lifecycle, func() bool { return true })
	e.GET("/work", func(c echo.Context) error {
		time.Sleep(20 * time.Millisecond)
		return c.String(http.StatusOK, "done")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	lifecycle.SetReady(true)
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	workDone := make(chan struct{})
	go func() {
		defer close(workDone)
		e.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/work", nil))
	}()
	require.Eventually(t, func() bool {
		return lifecycle.Status().Inflight == 1
	}, time.Second, 5*time.Millisecond)

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/drain/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var status drainStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.EqualValues(t, 1, status.Inflight)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "devshardd_lifecycle_inflight_requests 1")

	<-workDone
	require.EqualValues(t, 0, lifecycle.Status().Inflight)
}

func TestLifecycleDrainRejectsNewWork(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)
	admin := buildAdminServer(lifecycle, func() bool { return true })
	e.GET("/work", func(c echo.Context) error {
		return c.String(http.StatusOK, "done")
	})

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/drain", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/drain/status", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var status drainStatus
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &status))
	require.Zero(t, status.Inflight)
}

func TestReadyReflectsStorageReadiness(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	storageReady := false
	admin := buildAdminServer(lifecycle, func() bool { return storageReady })

	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code,
		"chain-ready but storage rebuilding must report 503")

	storageReady = true
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "\"storage_ready\":true")
}

func TestAdminExposesPprofNotPublic(t *testing.T) {
	lifecycle := newLifecycleState()
	e := buildServer(lifecycle)
	admin := buildAdminServer(lifecycle, func() bool { return true })

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	require.Equal(t, http.StatusNotFound, rec.Code)

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "goroutine")

	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotEmpty(t, rec.Body.Bytes())
}
