package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
)

// SessionVersionLookup resolves which protocol version owns a bound escrow.
// ok=false means the escrow is unbound (no session row).
type SessionVersionLookup interface {
	LookupSessionVersion(ctx context.Context, escrowID string) (version string, ok bool, err error)
}

// HandlerOption configures versionless observability routing.
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	lookup SessionVersionLookup
}

// WithSessionVersionLookup enables bound-version routing for session-scoped
// versionless observability. When unset, session obs falls back to fan-out.
func WithSessionVersionLookup(lookup SessionVersionLookup) HandlerOption {
	return func(c *handlerConfig) {
		c.lookup = lookup
	}
}

// Handler returns an http.Handler that routes requests by version prefix.
// First path segment is the version name, stripped before forwarding.
// Example: /v0.2.11/chat/completions -> localhost:9001/chat/completions
//
// Versionless observability paths (sessions/.../diffs|mempool|signatures,
// stats/..., metrics, healthz) are forwarded without a version prefix so join
// proxy can rewrite legacy /{version}/obs URLs onto canonical versionless URLs.
func Handler(routes *atomic.Value, opts ...HandlerOption) http.Handler {
	var cfg handlerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			http.Error(w, "version prefix required", http.StatusBadRequest)
			return
		}

		routeMap := routes.Load().(map[string]string)

		if isVersionlessObsPath(path) {
			serveVersionlessObs(w, r, routeMap, "/"+path, cfg.lookup)
			return
		}

		parts := strings.SplitN(path, "/", 2)
		version := parts[0]
		rest := "/"
		if len(parts) == 2 {
			rest = "/" + parts[1]
		}

		target, ok := routeMap[version]
		if !ok {
			http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
			return
		}

		reverseProxy(target, rest).ServeHTTP(w, r)
	})
}

// isVersionlessObsPath reports public observability paths that must not require
// a protocol version segment. payloads stays versioned (validator protocol).
func isVersionlessObsPath(path string) bool {
	switch path {
	case "metrics", "healthz":
		return true
	}
	if path == "stats" || strings.HasPrefix(path, "stats/") {
		return true
	}
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "sessions" {
		switch parts[2] {
		case "diffs", "mempool", "signatures":
			return true
		}
	}
	return false
}

func serveVersionlessObs(w http.ResponseWriter, r *http.Request, routeMap map[string]string, rest string, lookup SessionVersionLookup) {
	versions := sortedVersions(routeMap)
	if len(versions) == 0 {
		http.Error(w, "no versions available", http.StatusServiceUnavailable)
		return
	}

	// Process-level obs (/metrics, /healthz, /stats/shards list): pin to primary.
	// Multi-version aggregation of /stats/shards is deferred; primary is the
	// lexicographic-max approved version (documented limitation for non-HA / multi-version lists).
	escrowID, scoped := escrowIDFromObsPath(rest)
	if !scoped {
		reverseProxy(routeMap[versions[len(versions)-1]], rest).ServeHTTP(w, r)
		return
	}

	if lookup != nil {
		ver, ok, err := lookup.LookupSessionVersion(r.Context(), escrowID)
		if err != nil {
			// Shared index unavailable — degrade to fan-out.
			serveSessionObsFanout(w, r, routeMap, versions, rest)
			return
		}
		if !ok || ver == "" {
			http.NotFound(w, r)
			return
		}
		target, found := routeMap[ver]
		if !found {
			http.Error(w, fmt.Sprintf("bound version %q not running", ver), http.StatusNotFound)
			return
		}
		reverseProxy(target, rest).ServeHTTP(w, r)
		return
	}

	serveSessionObsFanout(w, r, routeMap, versions, rest)
}

func serveSessionObsFanout(w http.ResponseWriter, r *http.Request, routeMap map[string]string, versions []string, rest string) {
	var fallback409 *httptest.ResponseRecorder
	for _, ver := range versions {
		rec := httptest.NewRecorder()
		reverseProxy(routeMap[ver], rest).ServeHTTP(rec, r.Clone(r.Context()))
		switch {
		case rec.Code == http.StatusNotFound:
			continue
		case rec.Code == http.StatusConflict:
			fallback409 = rec
			continue
		default:
			writeRecorder(w, rec)
			return
		}
	}
	if fallback409 != nil {
		writeRecorder(w, fallback409)
		return
	}
	http.NotFound(w, r)
}

// escrowIDFromObsPath extracts the escrow id from session-scoped or stats-detail
// versionless paths. ok=false for process-level paths (metrics, shard list, …).
func escrowIDFromObsPath(rest string) (string, bool) {
	path := strings.TrimPrefix(rest, "/")
	parts := strings.Split(path, "/")
	if len(parts) == 3 && parts[0] == "sessions" {
		switch parts[2] {
		case "diffs", "mempool", "signatures":
			if parts[1] != "" {
				return parts[1], true
			}
		}
	}
	// /stats/shards/{escrow_id}
	if len(parts) == 3 && parts[0] == "stats" && parts[1] == "shards" && parts[2] != "" {
		return parts[2], true
	}
	return "", false
}

func sortedVersions(routeMap map[string]string) []string {
	versions := make([]string, 0, len(routeMap))
	for v := range routeMap {
		versions = append(versions, v)
	}
	sort.Strings(versions)
	return versions
}

func reverseProxy(target, rest string) *httputil.ReverseProxy {
	targetURL, err := url.Parse("http://" + target)
	if err != nil {
		return &httputil.ReverseProxy{
			Director: func(req *http.Request) {
				req.URL.Scheme = "http"
				req.URL.Host = "invalid"
			},
			ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
				http.Error(w, "internal error", http.StatusInternalServerError)
			},
		}
	}
	return &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			pr.Out.Host = targetURL.Host
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
		},
		FlushInterval: -1, // flush immediately for SSE
	}
}

func writeRecorder(w http.ResponseWriter, rec *httptest.ResponseRecorder) {
	for k, vals := range rec.Header() {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}
