package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
)

type routeTableLoader interface {
	Load() any
}

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
// An optional leading /devshard/ is accepted (same as versiond-router) so
// gateway clients that use RoutePrefix /devshard/<ver> can hit versiond
// directly without going through the sticky router.
// Example: /v0.2.11/chat/completions -> localhost:9001/chat/completions
// Example: /devshard/v2/sessions/1/mempool -> localhost:9001/sessions/1/mempool
//
// Versionless observability paths (sessions/.../diffs|mempool|signatures,
// stats/..., metrics) are forwarded without a version prefix so join
// proxy can rewrite legacy /{version}/obs URLs onto canonical versionless URLs.
// /healthz is intentionally NOT versionless here: versiond registers its own
// /healthz on the mux (supervisor status). Child health is /{version}/healthz.
func Handler(routes *atomic.Value, opts ...HandlerOption) http.Handler {
	var cfg handlerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimPrefix(path, "devshard/")
		if path == "" {
			http.Error(w, "version prefix required", http.StatusBadRequest)
			return
		}

		if isVersionlessObsPath(path) {
			serveVersionlessObs(w, r, routes, "/"+path, cfg.lookup)
			return
		}

		parts := strings.SplitN(path, "/", 2)
		version := parts[0]
		rest := "/"
		if len(parts) == 2 {
			rest = "/" + parts[1]
		}

		target, ok := acquireTarget(routes, version)
		if !ok {
			http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
			return
		}
		defer target.release()

		reverseProxy(target.Address(), rest).ServeHTTP(w, r)
	})
}

func acquireTarget(routes routeTableLoader, version string) (*Target, bool) {
	for {
		target, ok := routes.Load().(RouteTable)[version]
		if !ok {
			return nil, false
		}
		if target.acquire() {
			return target, true
		}
	}
}

// isVersionlessObsPath reports public observability paths that must not require
// a protocol version segment. payloads stays versioned (validator protocol).
func isVersionlessObsPath(path string) bool {
	if path == "metrics" {
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

func serveVersionlessObs(w http.ResponseWriter, r *http.Request, routes routeTableLoader, rest string, lookup SessionVersionLookup) {
	routeMap, _ := routes.Load().(RouteTable)
	versions := sortedVersions(routeMap)
	if len(versions) == 0 {
		http.Error(w, "no versions available", http.StatusServiceUnavailable)
		return
	}

	// Process-level obs (/metrics, /stats/shards list): pin to primary.
	// Multi-version aggregation of /stats/shards is deferred; primary is the
	// newest approved version by numeric/dotted comparison (not lexicographic).
	// /healthz is owned by versiond's mux (not this handler).
	escrowID, scoped := escrowIDFromObsPath(rest)
	if !scoped {
		serveAcquired(w, r, routes, primaryVersion(versions), rest)
		return
	}

	if lookup != nil {
		ver, ok, err := lookup.LookupSessionVersion(r.Context(), escrowID)
		if err != nil {
			// Shared index unavailable — degrade to fan-out (visible via warn + counter).
			noteLookupFanout(escrowID, err)
			serveSessionObsFanout(w, r, routes, versions, rest)
			return
		}
		if !ok || ver == "" {
			http.NotFound(w, r)
			return
		}
		if _, found := routeMap[ver]; !found {
			http.Error(w, fmt.Sprintf("bound version %q not running", ver), http.StatusNotFound)
			return
		}
		serveAcquired(w, r, routes, ver, rest)
		return
	}

	serveSessionObsFanout(w, r, routes, versions, rest)
}

func serveAcquired(w http.ResponseWriter, r *http.Request, routes routeTableLoader, version, rest string) {
	target, ok := acquireTarget(routes, version)
	if !ok {
		http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
		return
	}
	defer target.release()
	reverseProxy(target.Address(), rest).ServeHTTP(w, r)
}

func serveSessionObsFanout(w http.ResponseWriter, r *http.Request, routes routeTableLoader, versions []string, rest string) {
	var fallback409 *httptest.ResponseRecorder
	// Try newest first — more likely to own recently bound escrows.
	for _, v := range slices.Backward(versions) {
		ver := v
		target, ok := acquireTarget(routes, ver)
		if !ok {
			continue
		}
		rec := httptest.NewRecorder()
		reverseProxy(target.Address(), rest).ServeHTTP(rec, r.Clone(r.Context()))
		target.release()
		switch rec.Code {
		case http.StatusNotFound:
			continue
		case http.StatusConflict:
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
