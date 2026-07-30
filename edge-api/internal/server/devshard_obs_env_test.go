package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"common/devshardobs"
)

func versiondStub(t *testing.T, versions ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			entries := make([]map[string]string, 0, len(versions))
			for _, v := range versions {
				entries = append(entries, map[string]string{"name": v, "status": "running"})
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(entries); err != nil {
				t.Errorf("encode healthz: %v", err)
			}
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

// TestVersionlessObsMountedWithoutSessionDB mirrors edge-api's boot path under
// compose defaults that configure no session database: the obs router still
// builds (fan-out only) and its routes are mounted.
func TestVersionlessObsMountedWithoutSessionDB(t *testing.T) {
	versiond := versiondStub(t, "v1", "v2")
	for _, k := range []string{
		"PGHOST", "DATABASE_URL", "DEVSHARD_OBS_PGHOST", "DEVSHARD_OBS_DATABASE_URL",
		"DEVSHARD_OBS_DISABLE_SESSION_LOOKUP", "VERSIOND_DISABLE_SESSION_LOOKUP",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("PGDATABASE", "payloads")
	t.Setenv("DEVSHARD_VERSIOND_URL", versiond.URL)

	router, err := devshardobs.OpenFromEnv(context.Background())
	if err != nil {
		t.Fatalf("obs router must build without a session DB: %v", err)
	}
	defer router.Close()
	if router.Lookup != nil {
		t.Fatal("expected nil lookup with payload-only PG env")
	}

	e := New(nil, router.Handler)
	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/sessions/escrow-1/mempool", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q want 200 via fan-out", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "versiond:/devshard/v") {
		t.Fatalf("body=%q want proxied versiond response", rec.Body.String())
	}
}

// TestVersionlessObsUnmountedWhenRouterMissing keeps the nil-handler contract:
// no obs routes are registered, so nothing shadows Tier A routing.
func TestVersionlessObsUnmountedWhenRouterMissing(t *testing.T) {
	e := New(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/v1/devshard/metrics", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404 when obs handler is absent", rec.Code)
	}
}
