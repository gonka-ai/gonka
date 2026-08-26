package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleTransitionTableInvariants(t *testing.T) {
	states := []lifecyclePhase{
		lifecyclePhaseStarting,
		lifecyclePhaseServing,
		lifecyclePhaseDisconnected,
		lifecyclePhaseDraining,
	}
	events := []lifecycleEvent{
		lifecycleEventChainReady,
		lifecycleEventChainDisconnected,
		lifecycleEventDrainRequested,
	}

	require.Len(t, lifecyclePhaseTable, len(states))
	for _, from := range states {
		spec, ok := lifecyclePhaseTable[from]
		require.Truef(t, ok, "missing lifecycle phase %s", from)
		require.Lenf(t, spec.transitions, len(events), "transitions from %s", from)
		for _, event := range events {
			to, exists := nextLifecyclePhase(from, event)
			require.Truef(t, exists, "missing transition for %s + %s", from, event)
			_, targetKnown := lifecyclePhaseTable[to]
			require.Truef(t, targetKnown, "transition %s + %s targets unknown phase %s", from, event, to)
		}

		to, _ := nextLifecyclePhase(from, lifecycleEventDrainRequested)
		require.Equalf(t, lifecyclePhaseDraining, to,
			"drain request from %s must close admission", from)
	}

	for _, event := range events {
		to, ok := nextLifecyclePhase(lifecyclePhaseDraining, event)
		require.True(t, ok)
		require.Equalf(t, lifecyclePhaseDraining, to,
			"draining must absorb late %s events", event)
	}

	_, ok := nextLifecyclePhase("unknown", lifecycleEventDrainRequested)
	require.False(t, ok, "unknown lifecycle state accepted an event")
	_, ok = nextLifecyclePhase(lifecyclePhaseServing, "unknown")
	require.False(t, ok, "unknown lifecycle event was accepted")
}

func TestLifecyclePhaseTableProjections(t *testing.T) {
	type projection struct {
		ready     bool
		draining  bool
		accepting bool
	}
	expected := map[lifecyclePhase]projection{
		lifecyclePhaseStarting: {
			accepting: true,
		},
		lifecyclePhaseServing: {
			ready:     true,
			accepting: true,
		},
		lifecyclePhaseDisconnected: {
			ready:     true,
			accepting: true,
		},
		lifecyclePhaseDraining: {
			draining: true,
		},
	}

	require.Len(t, lifecyclePhaseTable, len(expected))
	for phase, want := range expected {
		spec, ok := lifecyclePhaseTable[phase]
		require.Truef(t, ok, "missing lifecycle phase %s", phase)
		require.Equal(t, want.ready, spec.ready, "ready projection for %s", phase)
		require.Equal(t, want.draining, spec.draining, "draining projection for %s", phase)
		require.Equal(t, want.accepting, spec.accepting, "admission projection for %s", phase)
		require.Falsef(
			t,
			spec.ready && spec.draining,
			"phase %s projects both ready and draining",
			phase,
		)
	}
}

func TestLifecycleTracksChainReconnect(t *testing.T) {
	lifecycle := newLifecycleState()
	require.False(t, lifecycle.Status().Ready)

	lifecycle.SetReady(true)
	require.True(t, lifecycle.Status().Ready)

	lifecycle.SetReady(false)
	status := lifecycle.Status()
	require.True(t, status.Ready,
		"a reconnect after initial readiness must not withdraw every replica")
	require.False(t, status.Draining)

	lifecycle.SetReady(true)
	require.True(t, lifecycle.Status().Ready)
}

func TestLifecycleDrainAbsorbsLateChainEvents(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	lifecycle.StartDrain()

	lifecycle.SetReady(false)
	lifecycle.SetReady(true)
	lifecycle.StartDrain()

	status := lifecycle.Status()
	require.False(t, status.Ready)
	require.True(t, status.Draining)
}

func TestLifecycleDrainKeepsAdmittedRequestInflight(t *testing.T) {
	lifecycle := newLifecycleState()
	lifecycle.SetReady(true)
	e := buildServer(lifecycle)
	started := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	finishRequest := sync.OnceFunc(func() {
		close(release)
		<-finished
	})
	t.Cleanup(finishRequest)
	e.GET("/work", func(c echo.Context) error {
		close(started)
		<-release
		return c.NoContent(http.StatusNoContent)
	})

	go func() {
		defer close(finished)
		e.ServeHTTP(
			httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/work", nil),
		)
	}()
	<-started

	lifecycle.StartDrain()
	status := lifecycle.Status()
	require.True(t, status.Draining)
	require.EqualValues(t, 1, status.Inflight)

	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/work", nil))
	require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	require.EqualValues(t, 1, lifecycle.Status().Inflight)

	finishRequest()
	require.Zero(t, lifecycle.Status().Inflight)
}

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
