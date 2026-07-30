package public

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/devshardobs"

	"github.com/stretchr/testify/require"
)

// versiondStub answers /healthz plus version-pinned obs, standing in for the
// versiond supervisor behind dapi.
func versiondStub(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			entries := make([]map[string]string, 0, len(versions))
			for _, v := range versions {
				entries = append(entries, map[string]string{"name": v, "status": "running"})
			}
			w.Header().Set("Content-Type", "application/json")
			require.NoError(t, json.NewEncoder(w).Encode(entries))
			return
		}
		if strings.HasPrefix(r.URL.Path, "/devshard/") {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("versiond:" + r.URL.Path))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// setJoinPayloadEnv mirrors deploy/join defaults for dapi: payload storage env
// only, with an empty PGHOST. This env must not be mistaken for a session DB.
func setJoinPayloadEnv(t *testing.T) {
	t.Helper()
	t.Setenv("PGHOST", "")
	t.Setenv("PGPORT", "5432")
	t.Setenv("PGDATABASE", "payloads")
	t.Setenv("PGUSER", "payloads")
	t.Setenv("PGPASSWORD", "")
	t.Setenv("DATABASE_URL", "")
	t.Setenv("DEVSHARD_OBS_DATABASE_URL", "")
	t.Setenv("DEVSHARD_OBS_PGHOST", "")
	t.Setenv("DEVSHARD_OBS_DISABLE_SESSION_LOOKUP", "")
	t.Setenv("VERSIOND_DISABLE_SESSION_LOOKUP", "")
}

func serveObsFromEnv(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	router, err := devshardobs.OpenFromEnv(context.Background())
	require.NoError(t, err, "obs wiring must survive missing/broken session DB")
	require.NotNil(t, router)
	require.NotNil(t, router.Handler)
	t.Cleanup(router.Close)

	s := NewServer(nil, newTestConfigManager(t), nil, nil, nil, nil, WithDevshardObs(router.Handler))
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	return rec
}

// TestDevshardObs_JoinDefaultsServeVersionlessObs covers the regression chain:
// payload-only PG env → nil lookup → fan-out handler → dapi mounts obs, so the
// advertised versionless endpoints never fall through to the 410 catch-all.
func TestDevshardObs_JoinDefaultsServeVersionlessObs(t *testing.T) {
	versiond := versiondStub(t, "v1", "v2")
	setJoinPayloadEnv(t)
	t.Setenv("DEVSHARD_VERSIOND_URL", versiond.URL)

	for _, path := range []string{
		"/v1/devshard/metrics",
		"/v1/devshard/sessions/escrow-1/mempool",
	} {
		t.Run(path, func(t *testing.T) {
			rec := serveObsFromEnv(t, path)
			require.NotEqual(t, http.StatusGone, rec.Code, "versionless obs must not hit the deprecation catch-all")
			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "versiond:/devshard/v")
		})
	}
}

// TestDevshardObs_UnreachableSessionDBStillServesObs asserts a configured but
// dead session DB degrades to fan-out instead of unmounting obs.
func TestDevshardObs_UnreachableSessionDBStillServesObs(t *testing.T) {
	versiond := versiondStub(t, "v1")
	setJoinPayloadEnv(t)
	t.Setenv("DEVSHARD_VERSIOND_URL", versiond.URL)
	t.Setenv("DEVSHARD_OBS_PGHOST", "127.0.0.1")
	t.Setenv("DEVSHARD_OBS_PGPORT", "1")
	t.Setenv("DEVSHARD_OBS_PGDATABASE", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGUSER", "devshardd")
	t.Setenv("DEVSHARD_OBS_PGPASSWORD", "nope")

	rec := serveObsFromEnv(t, "/v1/devshard/sessions/escrow-1/diffs")
	require.NotEqual(t, http.StatusGone, rec.Code)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "versiond:/devshard/v1/sessions/escrow-1/diffs")
}

// TestDevshardObs_NonObsPathsStayDeprecated keeps the catch-all in place for
// everything outside the versionless obs surface.
func TestDevshardObs_NonObsPathsStayDeprecated(t *testing.T) {
	versiond := versiondStub(t, "v1")
	setJoinPayloadEnv(t)
	t.Setenv("DEVSHARD_VERSIOND_URL", versiond.URL)

	rec := serveObsFromEnv(t, "/v1/devshard/chat/completions")
	require.Equal(t, http.StatusGone, rec.Code)
}
