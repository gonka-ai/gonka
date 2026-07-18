package proxy

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
)

// Handler returns an http.Handler that routes requests by version prefix.
// First path segment is the version name, stripped before forwarding.
// An optional leading /devshard/ is accepted (same as versiond-router) so
// gateway clients that use RoutePrefix /devshard/<ver> can hit versiond
// directly without going through the sticky router.
// Example: /v0.2.11/chat/completions -> localhost:9001/chat/completions
// Example: /devshard/v2/sessions/1/mempool -> localhost:9001/sessions/1/mempool
func Handler(routes *atomic.Value) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		path = strings.TrimPrefix(path, "devshard/")
		parts := strings.SplitN(path, "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			http.Error(w, "version prefix required", http.StatusBadRequest)
			return
		}

		version := parts[0]
		rest := "/"
		if len(parts) == 2 {
			rest = "/" + parts[1]
		}

		routeMap := routes.Load().(map[string]string)
		target, ok := routeMap[version]
		if !ok {
			http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
			return
		}

		targetURL, err := url.Parse("http://" + target)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}

		p := &httputil.ReverseProxy{
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

		p.ServeHTTP(w, r)
	})
}
