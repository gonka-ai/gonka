package devshardobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

type mapLookup map[string]string

func (m mapLookup) LookupSessionVersion(_ context.Context, escrowID string) (string, bool, error) {
	v, ok := m[escrowID]
	return v, ok, nil
}

type errLookup struct{ err error }

func (e errLookup) LookupSessionVersion(context.Context, string) (string, bool, error) {
	return "", false, e.err
}

func mustHandler(t *testing.T, versiond *url.URL, lookup SessionVersionLookup, versions []string) *Handler {
	t.Helper()
	h, err := NewHandler(Config{
		VersiondBase: versiond,
		Lookup:       lookup,
		Versions:     StaticVersions(versions),
	})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHandler_PrimaryForMetrics(t *testing.T) {
	var sawPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, nil, []string{"v0.2.9", "v0.2.11"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if sawPath != "/devshard/v0.2.11/metrics" {
		t.Fatalf("metrics path=%q", sawPath)
	}
}

func TestHandler_StatsShardsAggregatesVersions(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/v1/"):
			_, _ = w.Write([]byte(`{
				"protocol_version":"v1","binary_version":"b1",
				"current_epoch_id":3,"cached_at":1,"cache_ttl_seconds":60,
				"active_escrows":["a"],"shards":[{"escrow_id":"a","epoch_id":3}]
			}`))
		case strings.Contains(r.URL.Path, "/v2/"):
			_, _ = w.Write([]byte(`{
				"protocol_version":"v2","binary_version":"b2",
				"current_epoch_id":7,"cached_at":1,"cache_ttl_seconds":30,
				"active_escrows":["b"],"shards":[{"escrow_id":"b","epoch_id":7}]
			}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, nil, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/stats/shards")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var body struct {
		ProtocolVersion string   `json:"protocol_version"`
		BinaryVersion   string   `json:"binary_version"`
		CurrentEpochID  uint64   `json:"current_epoch_id"`
		CacheTTLSeconds int64    `json:"cache_ttl_seconds"`
		ActiveEscrows   []string `json:"active_escrows"`
		Shards          []struct {
			EscrowID string `json:"escrow_id"`
		} `json:"shards"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.ProtocolVersion != "v2" || body.BinaryVersion != "b2" {
		t.Fatalf("metadata=%+v, want primary v2", body)
	}
	if body.CurrentEpochID != 7 {
		t.Fatalf("epoch=%d, want 7", body.CurrentEpochID)
	}
	if body.CacheTTLSeconds != 30 {
		t.Fatalf("ttl=%d, want min 30", body.CacheTTLSeconds)
	}
	if len(body.ActiveEscrows) != 2 || body.ActiveEscrows[0] != "a" || body.ActiveEscrows[1] != "b" {
		t.Fatalf("active=%v", body.ActiveEscrows)
	}
	if len(body.Shards) != 2 {
		t.Fatalf("shards=%v", body.Shards)
	}
}

func TestHandler_BoundVersionLookup(t *testing.T) {
	var sawPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		fmt.Fprintf(w, "hit:%s", r.URL.Path)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, mapLookup{"42": "v2"}, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "hit:/devshard/v2/sessions/42/diffs" {
		t.Fatalf("status=%d body=%q path=%q", resp.StatusCode, body, sawPath)
	}

	resp2, err := http.Get(srv.URL + "/v1/devshard/stats/shards/42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK || string(body2) != "hit:/devshard/v2/stats/shards/42" {
		t.Fatalf("stats detail status=%d body=%q", resp2.StatusCode, body2)
	}
}

func TestHandler_PGMissFansOut(t *testing.T) {
	miss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprintf(w, "ok:%s", r.URL.Path)
	}))
	defer miss.Close()
	u, _ := url.Parse(miss.URL)

	// empty lookup map → ok=false → fan-out; newest is v2
	h := mustHandler(t, u, mapLookup{}, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/42/mempool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok:/devshard/v2/sessions/42/mempool" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestHandler_LookupErrorFansOut(t *testing.T) {
	resetLookupFanoutTelemetryForTest()
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok:%s", r.URL.Path)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, errLookup{err: fmt.Errorf("pg down")}, []string{"v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok:/devshard/v2/sessions/42/diffs" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got := LookupFanoutErrors(); got != 1 {
		t.Fatalf("LookupFanoutErrors()=%d, want 1", got)
	}
}

func TestHandler_BoundVersionNotRunning(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("must not proxy")
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, mapLookup{"42": "v9"}, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestHandler_FanoutAllMiss404(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, mapLookup{}, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/missing/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestHandler_FanoutSkips404(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/v2/"):
			http.NotFound(w, r)
		case strings.Contains(r.URL.Path, "/v1/"):
			fmt.Fprintf(w, "v1")
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	h := mustHandler(t, u, nil, []string{"v1", "v2"})
	srv := httptest.NewServer(h)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/devshard/sessions/x/signatures")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "v1" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestIsVersionlessObsPath(t *testing.T) {
	cases := map[string]bool{
		"/v1/devshard/metrics":                 true,
		"/v1/devshard/stats/shards":            true,
		"/v1/devshard/stats/shards/42":         true,
		"/v1/devshard/sessions/1/diffs":        true,
		"/v1/devshard/sessions/1/mempool":      true,
		"/v1/devshard/sessions/1/signatures":   true,
		"/devshard/v4/sessions/1/mempool":   false,
		"/v1/devshard/sessions/1/chat/completions": false,
		"/v1/debug/state":                   false,
	}
	for path, want := range cases {
		if got := IsVersionlessObsPath(path); got != want {
			t.Errorf("IsVersionlessObsPath(%q)=%v want %v", path, got, want)
		}
	}
}

func TestHealthzVersions_FiltersRunning(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[
			{"name":"v1","status":"running"},
			{"name":"v2","status":"draining"},
			{"name":"v3","status":"running"}
		]`))
	}))
	defer backend.Close()
	u, _ := url.Parse(backend.URL)

	src := NewHealthzVersions(u, backend.Client(), defaultVersionsCacheTTL)
	got, err := src.ActiveVersions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "v1" || got[1] != "v3" {
		t.Fatalf("got %v, want [v1 v3]", got)
	}
}
