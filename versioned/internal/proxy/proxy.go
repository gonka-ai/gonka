package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
)

type routeTableLoader interface {
	Load() any
}

// Handler returns an http.Handler that routes requests by version prefix.
// First path segment is the version name, stripped before forwarding.
// An optional leading /devshard/ is accepted (same as versiond-router) so
// gateway clients that use RoutePrefix /devshard/<ver> can hit versiond
// directly without going through the sticky router.
// Example: /v0.2.11/chat/completions -> localhost:9001/chat/completions
// Example: /devshard/v2/sessions/1/mempool -> localhost:9001/sessions/1/mempool
//
// Versionless observability (/sessions|stats|metrics without a version
// segment) is served by dapi/edge-api (common/devshardobs), not versiond.
// /healthz on this mux is versiond's own supervisor status (registered by
// main ahead of this handler). Child health is /{version}/healthz.
func Handler(routes *atomic.Value) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimPrefix(path, "devshard/")
		if path == "" {
			http.Error(w, "version prefix required", http.StatusBadRequest)
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
