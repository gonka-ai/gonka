package proxy

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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
	originLookups := newOriginLookupLimiter()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// HAProxy normalizes before its path zones. A request that reaches
		// versiond with a still-encoded RPC path would be forwarded on the
		// decoded Path (RawPath cleared in the rewrite) and skip those zones.
		if encodedPeerRPCPath(r.URL) {
			http.Error(w, "encoded rpc path", http.StatusBadRequest)
			return
		}
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

		// PeerAuth Watch is a keepalive, not a user request. Counting it
		// would hold the retiring generation until DrainTimeout: the stream
		// stays open for the session TTL, and stop/start waits for inflight
		// to hit zero. The child is still the request's backend; Retire just
		// does not wait for this stream.
		if peerAuthWatchPath(rest) {
			table, _ := routes.Load().(RouteTable)
			target := table[version]
			if target == nil {
				http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
				return
			}
			serveChild(w, r, target, rest, originLookups)
			return
		}

		target, ok := acquireTarget(routes, version)
		if !ok {
			http.Error(w, fmt.Sprintf("version %q not found", version), http.StatusNotFound)
			return
		}
		defer target.release()

		serveChild(w, r, target, rest, originLookups)
	})
}

func encodedPeerRPCPath(u *url.URL) bool {
	if u == nil || u.RawPath == "" {
		return false
	}
	return strings.Contains(u.Path, "/sessions/") && strings.Contains(u.Path, "/rpc/")
}

func peerAuthWatchPath(rest string) bool {
	// The procedure is "...v1.PeerAuthService/Watch": the byte before
	// PeerAuthService is the protobuf package dot, not a slash.
	return strings.HasSuffix(rest, "PeerAuthService/Watch")
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
	switch path {
	case "metrics":
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

	if isRPCStatsPath(rest) {
		serveRPCStatsMerge(w, r, routes)
		return
	}

	// Process-level obs (/metrics, /stats/shards list): pin to primary.
	// Multi-version aggregation of /stats/shards is deferred; primary is the
	// newest approved version by numeric/dotted comparison (not lexicographic).
	// /healthz is owned by versiond's mux (not this handler).
	// GET /stats/rpc is the exception: merge every running child (Phase 4 step 8).
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
	reverseProxy(target, rest, nil, nil).ServeHTTP(w, r)
}

func serveSessionObsFanout(w http.ResponseWriter, r *http.Request, routes routeTableLoader, versions []string, rest string) {
	var fallback409 *httptest.ResponseRecorder
	// Try newest first — more likely to own recently bound escrows.
	for i := len(versions) - 1; i >= 0; i-- {
		ver := versions[i]
		target, ok := acquireTarget(routes, ver)
		if !ok {
			continue
		}
		rec := httptest.NewRecorder()
		reverseProxy(target, rest, nil, nil).ServeHTTP(rec, r.Clone(r.Context()))
		target.release()
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

func serveChild(w http.ResponseWriter, r *http.Request, target *Target, rest string, lim *originLookupLimiter) {
	// A token already rejected in this window is the child's
	// invalid_session_token, not the IP budget. 429 would not send the
	// peer back through Attach.
	if lim.cachedInvalidToken(r, rest) {
		writeInvalidSessionToken(w)
		return
	}
	release, ok := lim.admit(r, rest)
	if !ok {
		w.Header().Set(headerDevshardError, errorEscrowLookupLimited)
		http.Error(w, "too many escrow lookups", http.StatusTooManyRequests)
		return
	}
	defer release()
	reverseProxy(target, rest, lim, release).ServeHTTP(w, r)
}

// reverseProxy forwards to the child. release, when set, runs once the
// child's response headers are observed.
func reverseProxy(target *Target, rest string, lim *originLookupLimiter, release func()) *httputil.ReverseProxy {
	targetURL, err := url.Parse("http://" + target.Address())
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
	rp := &httputil.ReverseProxy{
		Transport: target.transport(), // h2c or HTTP/1.1, chosen when the child was registered
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			// SetXForwarded rewrites X-Forwarded-*. Keep X-Real-IP from the
			// trusted ingress (nginx / versiond-router). versiond keys the
			// unknown-escrow IP cap on the inbound header; the child does not.
			if ip := pr.In.Header.Get(originIPHeader); ip != "" {
				pr.Out.Header.Set(originIPHeader, ip)
			}
			pr.Out.URL.Scheme = targetURL.Scheme
			pr.Out.URL.Host = targetURL.Host
			pr.Out.Host = targetURL.Host
			pr.Out.URL.Path = rest
			pr.Out.URL.RawPath = ""
		},
		FlushInterval: -1, // flush immediately for SSE
	}
	if lim != nil {
		rp.ModifyResponse = func(resp *http.Response) error {
			if resp != nil && resp.Request != nil {
				lim.observe(resp.Request, rest, resp)
			}
			if release != nil {
				release()
			}
			return nil
		}
	}
	return rp
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
