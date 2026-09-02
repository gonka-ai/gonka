package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"

	"devshard/cmd/devshardd/session"
)

func recoveryDone() session.RecoveryProgress {
	return session.RecoveryProgress{Complete: true}
}

func TestLifecycleReadyAndDrainStatus(t *testing.T) {
	lifecycle := newLifecycleState()
	e := buildServer(lifecycle)
	admin := buildAdminServer(lifecycle, func() bool { return true }, recoveryDone)
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
	admin := buildAdminServer(lifecycle, func() bool { return true }, recoveryDone)
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
	admin := buildAdminServer(lifecycle, func() bool { return storageReady }, recoveryDone)

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
	admin := buildAdminServer(lifecycle, func() bool { return true }, recoveryDone)

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

// Status 200 means the process can serve. recovery_complete in the body is the
// warm signal a version cutover waits on; draining and unready storage still 503.
func TestReadyReflectsSessionRecoveryProgress(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	progress := session.RecoveryProgress{
		Total: 10, Recovered: 3, Failed: 1, VersionSkipped: 2, Pending: 4,
	}
	admin := buildAdminServer(lifecycle, func() bool { return true }, func() session.RecoveryProgress {
		return progress
	})

	rec := httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, rec.Code,
		"chain-ready process must serve while session recovery is still draining")
	require.Contains(t, rec.Body.String(), `"recovery_complete":false`)
	require.Contains(t, rec.Body.String(), `"sessions_total":10`)
	require.Contains(t, rec.Body.String(), `"sessions_recovered":3`)
	require.Contains(t, rec.Body.String(), `"sessions_failed":1`)
	require.Contains(t, rec.Body.String(), `"sessions_version_skipped":2`)
	require.Contains(t, rec.Body.String(), `"sessions_pending":4`)

	progress = session.RecoveryProgress{Complete: true, Total: 10, Recovered: 7, Failed: 1, VersionSkipped: 2}
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"recovery_complete":true`)
	require.Contains(t, rec.Body.String(), `"sessions_pending":0`)

	lifecycle.StartDrain()
	rec = httptest.NewRecorder()
	admin.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ready", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, "draining must still report 503")
}
