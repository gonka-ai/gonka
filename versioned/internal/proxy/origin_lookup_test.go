package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestIsUnknownEscrowBindPath(t *testing.T) {
	tests := []struct {
		method, rest string
		want         bool
	}{
		{http.MethodPost, "/sessions/1/chat/completions", true},
		{http.MethodPost, "/sessions/1/height-sync", true},
		{http.MethodPost, "/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Attach", true},
		{http.MethodGet, "/sessions/1/chat/completions", false},
		{http.MethodPost, "/sessions/1/diffs", false},
		{http.MethodPost, "/sessions/1/chat/completions/extra", false},
		{http.MethodPost, "/chat/completions", false},
		{http.MethodPost, "/sessions/1/rpc/devshard.transport.v1.PeerAuthService/Watch", false},
	}
	for _, tt := range tests {
		if got := isUnknownEscrowBindPath(tt.method, tt.rest); got != tt.want {
			t.Fatalf("isUnknownEscrowBindPath(%q, %q) = %v, want %v", tt.method, tt.rest, got, tt.want)
		}
	}
}

func TestParseOriginIP(t *testing.T) {
	if parseOriginIP("") != "" {
		t.Fatal("empty should skip")
	}
	if parseOriginIP("203.0.113.9") != "203.0.113.9" {
		t.Fatal("plain IPv4")
	}
	if parseOriginIP("203.0.113.9:443") != "203.0.113.9" {
		t.Fatal("host:port")
	}
	if parseOriginIP("  203.0.113.9, 198.51.100.1  ") != "203.0.113.9" {
		t.Fatal("first of list")
	}
	if parseOriginIP("not-an-ip") != "" {
		t.Fatal("garbage")
	}
}

func TestProxy_UnknownEscrowPerOriginIP(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.Header().Set(headerDevshardError, errorEscrowNotFound)
		http.Error(w, "get escrow: escrow not found", http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	handler := Handler(newRoutes(map[string]string{"v1": strings.TrimPrefix(backend.URL, "http://")}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	post := func(id, ip string) *http.Response {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/"+id+"/chat/completions", nil)
		if err != nil {
			t.Fatal(err)
		}
		if ip != "" {
			req.Header.Set(originIPHeader, ip)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}

	if resp := post("10", "203.0.113.9"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first miss status = %d", resp.StatusCode)
	}
	if resp := post("11", "203.0.113.9"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("second miss status = %d", resp.StatusCode)
	}
	if resp := post("12", "203.0.113.9"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("third miss status = %d, want 429", resp.StatusCode)
	}
	if forwarded.Load() != 2 {
		t.Fatalf("forwarded = %d, want 2 (third request must not reach the child)", forwarded.Load())
	}

	if resp := post("13", "203.0.113.10"); resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("other origin should still forward, status = %d", resp.StatusCode)
	}
	if forwarded.Load() != 3 {
		t.Fatalf("other origin forwarded = %d, want 3", forwarded.Load())
	}
}

func TestProxy_UnknownEscrowMissingOriginIPSkipped(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.Header().Set(headerDevshardError, errorEscrowNotFound)
		http.Error(w, "get escrow: escrow not found", http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	handler := Handler(newRoutes(map[string]string{"v1": strings.TrimPrefix(backend.URL, "http://")}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for i := 0; i < 5; i++ {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/20/chat/completions", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("missing X-Real-IP status = %d, want child 500", resp.StatusCode)
		}
	}
	if forwarded.Load() != 5 {
		t.Fatalf("forwarded = %d, want all five when origin IP is missing", forwarded.Load())
	}
}

func TestProxy_UnknownEscrowIgnoresSuccessAndUnrelated404(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		switch {
		case strings.HasSuffix(r.URL.Path, "/chat/completions"):
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok")
		case strings.HasSuffix(r.URL.Path, "/diffs"):
			http.NotFound(w, r)
		default:
			w.Header().Set(headerDevshardError, errorEscrowNotFound)
			http.Error(w, "invalid escrow id", http.StatusBadRequest)
		}
	}))
	t.Cleanup(backend.Close)

	handler := Handler(newRoutes(map[string]string{"v1": strings.TrimPrefix(backend.URL, "http://")}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	do := func(method, path, ip string) {
		req, err := http.NewRequest(method, srv.URL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(originIPHeader, ip)
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	ip := "203.0.113.11"
	for i := 0; i < 3; i++ {
		do(http.MethodPost, "/v1/sessions/30/chat/completions", ip)
	}
	do(http.MethodGet, "/v1/sessions/30/diffs", ip)
	do(http.MethodPost, "/v1/sessions/bad/seed", ip)
	if forwarded.Load() != 5 {
		t.Fatalf("forwarded = %d, want successes and unrelated paths not to fill the IP bucket", forwarded.Load())
	}
}

func TestProxy_UnknownEscrowIgnoresXForwardedFor(t *testing.T) {
	var forwarded atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded.Add(1)
		w.Header().Set(headerDevshardError, errorEscrowNotFound)
		http.Error(w, "get escrow: escrow not found", http.StatusInternalServerError)
	}))
	t.Cleanup(backend.Close)

	handler := Handler(newRoutes(map[string]string{"v1": strings.TrimPrefix(backend.URL, "http://")}))
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	for i := 0; i < 3; i++ {
		req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/sessions/40/chat/completions", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusInternalServerError {
			t.Fatalf("X-Forwarded-For must not key the IP bucket, status = %d", resp.StatusCode)
		}
	}
	if forwarded.Load() != 3 {
		t.Fatalf("forwarded = %d, want 3", forwarded.Load())
	}
}
