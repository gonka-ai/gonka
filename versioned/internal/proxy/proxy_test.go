package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newRoutes(m map[string]string) *atomic.Value {
	v := &atomic.Value{}
	v.Store(m)
	return v
}

func TestProxy_BasicForwarding(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
	}))
	defer backend.Close()

	// Extract host:port from backend URL
	addr := strings.TrimPrefix(backend.URL, "http://")
	routes := newRoutes(map[string]string{"v1": addr})

	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "path=/chat/completions" {
		t.Errorf("body = %q", string(body))
	}
}

func TestProxy_OptionalDevshardPrefix(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	routes := newRoutes(map[string]string{"v2": addr})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/devshard/v2/sessions/1/mempool")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "path=/sessions/1/mempool" {
		t.Errorf("body = %q, want path=/sessions/1/mempool", string(body))
	}
}

func TestProxy_RootPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	routes := newRoutes(map[string]string{"v1": addr})

	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "path=/" {
		t.Errorf("body = %q", string(body))
	}
}

func TestProxy_VersionNotFound(t *testing.T) {
	routes := newRoutes(map[string]string{})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/foo")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestProxy_NoVersionPrefix(t *testing.T) {
	routes := newRoutes(map[string]string{})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestProxy_VersionlessObs_Primary(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	// Numeric primary is v10 (not lexicographic "v2").
	routes := newRoutes(map[string]string{"v2": "127.0.0.1:1", "v10": addr})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "path=/metrics" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestProxy_VersionlessObs_PrimarySemverIsh(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok")
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	// Lexicographic max would be v0.2.9; numeric primary must be v0.2.11.
	routes := newRoutes(map[string]string{"v0.2.9": "127.0.0.1:1", "v0.2.11": addr})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/stats/shards")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

func TestProxy_VersionlessObs_SessionFanout(t *testing.T) {
	miss := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer miss.Close()
	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "path=%s", r.URL.Path)
	}))
	defer hit.Close()

	routes := newRoutes(map[string]string{
		"v1": strings.TrimPrefix(miss.URL, "http://"),
		"v2": strings.TrimPrefix(hit.URL, "http://"),
	})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "path=/sessions/42/diffs" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
}

type mapLookup map[string]string

func (m mapLookup) LookupSessionVersion(_ context.Context, escrowID string) (string, bool, error) {
	v, ok := m[escrowID]
	return v, ok, nil
}

type errLookup struct{ err error }

func (e errLookup) LookupSessionVersion(context.Context, string) (string, bool, error) {
	return "", false, e.err
}

func TestProxy_VersionlessObs_BoundVersionLookup(t *testing.T) {
	v1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "v1:%s", r.URL.Path)
	}))
	defer v1.Close()
	v2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "v2:%s", r.URL.Path)
	}))
	defer v2.Close()

	routes := newRoutes(map[string]string{
		"v1": strings.TrimPrefix(v1.URL, "http://"),
		"v2": strings.TrimPrefix(v2.URL, "http://"),
	})
	handler := Handler(routes, WithSessionVersionLookup(mapLookup{"42": "v2"}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "v2:/sessions/42/diffs" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}

	// stats detail also uses bound-version lookup
	resp2, err := http.Get(srv.URL + "/stats/shards/42")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	body2, _ := io.ReadAll(resp2.Body)
	if resp2.StatusCode != http.StatusOK || string(body2) != "v2:/stats/shards/42" {
		t.Fatalf("stats detail status=%d body=%q", resp2.StatusCode, body2)
	}
}

func TestProxy_VersionlessObs_LookupUnbound404(t *testing.T) {
	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("backend must not be contacted for unbound escrow")
	}))
	defer hit.Close()

	routes := newRoutes(map[string]string{"v2": strings.TrimPrefix(hit.URL, "http://")})
	handler := Handler(routes, WithSessionVersionLookup(mapLookup{}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/missing/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404", resp.StatusCode)
	}
}

func TestProxy_VersionlessObs_LookupErrorFallsBackToFanout(t *testing.T) {
	resetLookupFanoutTelemetryForTest()
	hit := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "ok:%s", r.URL.Path)
	}))
	defer hit.Close()

	routes := newRoutes(map[string]string{"v2": strings.TrimPrefix(hit.URL, "http://")})
	handler := Handler(routes, WithSessionVersionLookup(errLookup{err: fmt.Errorf("pg down")}))
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "ok:/sessions/42/diffs" {
		t.Fatalf("status=%d body=%q", resp.StatusCode, body)
	}
	if got := LookupFanoutErrors(); got != 1 {
		t.Fatalf("LookupFanoutErrors()=%d, want 1", got)
	}

	// Second miss increments counter; warn is rate-limited (not asserted here).
	resp2, err := http.Get(srv.URL + "/sessions/42/diffs")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if got := LookupFanoutErrors(); got != 2 {
		t.Fatalf("LookupFanoutErrors()=%d, want 2", got)
	}
}

func TestProxy_VersionlessObs_PayloadsStillVersioned(t *testing.T) {
	routes := newRoutes(map[string]string{"v2": "127.0.0.1:1"})
	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sessions/42/payloads")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	// Treated as version name "sessions" → not found in route map.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status=%d, want 404 (payloads must stay versioned)", resp.StatusCode)
	}
}

func TestProxy_QueryParams(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "query=%s", r.URL.RawQuery)
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	routes := newRoutes(map[string]string{"v1": addr})

	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/search?q=hello&limit=10")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if string(body) != "query=q=hello&limit=10" {
		t.Errorf("body = %q", string(body))
	}
}

func TestProxy_SSEStreaming(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("ResponseWriter does not implement Flusher")
		}
		for i := 0; i < 3; i++ {
			fmt.Fprintf(w, "data: event %d\n\n", i)
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
		}
	}))
	defer backend.Close()

	addr := strings.TrimPrefix(backend.URL, "http://")
	routes := newRoutes(map[string]string{"v1": addr})

	handler := Handler(routes)
	srv := httptest.NewServer(handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Errorf("Content-Type = %q", resp.Header.Get("Content-Type"))
	}

	scanner := bufio.NewScanner(resp.Body)
	var events []string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data:") {
			events = append(events, line)
		}
	}
	if len(events) != 3 {
		t.Errorf("got %d events, want 3", len(events))
	}
}
