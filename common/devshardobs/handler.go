package devshardobs

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

// Handler routes versionless /v1/devshard obs to versiond /devshard/{version}/….
type Handler struct {
	versiond  *url.URL
	lookup    SessionVersionLookup // optional
	versions  VersionsSource
	transport http.RoundTripper
}

// Config configures a versionless obs Handler.
type Config struct {
	VersiondBase *url.URL
	Lookup       SessionVersionLookup // nil → fan-out only
	Versions     VersionsSource
	Transport    http.RoundTripper // optional
}

// NewHandler builds a versionless obs reverse-proxy handler.
func NewHandler(cfg Config) (*Handler, error) {
	if cfg.VersiondBase == nil {
		return nil, fmt.Errorf("devshardobs: VersiondBase required")
	}
	if cfg.Versions == nil {
		return nil, fmt.Errorf("devshardobs: Versions required")
	}
	base := *cfg.VersiondBase
	if base.Path == "/" {
		base.Path = ""
	}
	transport := cfg.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &Handler{
		versiond:  &base,
		lookup:    cfg.Lookup,
		versions:  cfg.Versions,
		transport: transport,
	}, nil
}

// ServeHTTP implements http.Handler for versionless obs paths.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !IsVersionlessObsPath(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	rest := obsRestPath(r.URL.Path) // /sessions/… or /stats/… or /metrics

	versions, err := h.versions.ActiveVersions(r.Context())
	if err != nil {
		http.Error(w, "versiond versions unavailable", http.StatusBadGateway)
		return
	}
	versions = SortVersions(versions)
	if len(versions) == 0 {
		http.Error(w, "no versions available", http.StatusServiceUnavailable)
		return
	}

	escrowID, scoped := EscrowIDFromObsPath(rest)
	if !scoped {
		if isStatsShardsList(rest) {
			h.serveStatsShardsAggregate(w, r, versions)
			return
		}
		notePrimaryPin()
		h.serveVersion(w, r, PrimaryVersion(versions), rest)
		return
	}

	running := make(map[string]struct{}, len(versions))
	for _, v := range versions {
		running[v] = struct{}{}
	}

	if h.lookup != nil {
		ver, ok, err := h.lookup.LookupSessionVersion(r.Context(), escrowID)
		if err != nil {
			noteLookupFanout(escrowID, err)
			noteFanout()
			h.serveFanout(w, r, versions, rest)
			return
		}
		if ok && ver != "" {
			if _, found := running[ver]; !found {
				http.Error(w, fmt.Sprintf("bound version %q not running", ver), http.StatusNotFound)
				return
			}
			noteLookupHit()
			h.serveVersion(w, r, ver, rest)
			return
		}
		// PG miss: fan-out (SQLite-only / mixed estates). Locked in Phase 0.
	}

	noteFanout()
	h.serveFanout(w, r, versions, rest)
}

func (h *Handler) serveFanout(w http.ResponseWriter, r *http.Request, versions []string, rest string) {
	var fallback409 *httptest.ResponseRecorder
	for i := len(versions) - 1; i >= 0; i-- {
		ver := versions[i]
		rec := httptest.NewRecorder()
		h.serveVersion(rec, r.Clone(r.Context()), ver, rest)
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

func (h *Handler) serveVersion(w http.ResponseWriter, r *http.Request, version, rest string) {
	targetPath := "/devshard/" + version + rest
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = h.versiond.Scheme
			pr.Out.URL.Host = h.versiond.Host
			pr.Out.Host = h.versiond.Host
			pr.Out.URL.Path = targetPath
			pr.Out.URL.RawPath = ""
			if pr.In.URL.RawQuery != "" {
				pr.Out.URL.RawQuery = pr.In.URL.RawQuery
			}
		},
		Transport:     h.transport,
		FlushInterval: -1,
		ErrorHandler: func(rw http.ResponseWriter, _ *http.Request, err error) {
			http.Error(rw, "versiond proxy error: "+err.Error(), http.StatusBadGateway)
		},
	}
	rp.ServeHTTP(w, r)
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

// VersiondBaseFromEnv reads DEVSHARD_VERSIOND_URL or VERSIOND_URL.
// Default: http://versiond:8080
func VersiondBaseFromEnv() (*url.URL, error) {
	raw := strings.TrimSpace(os.Getenv("DEVSHARD_VERSIOND_URL"))
	if raw == "" {
		raw = strings.TrimSpace(os.Getenv("VERSIOND_URL"))
	}
	if raw == "" {
		raw = "http://versiond:8080"
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse versiond URL %q: %w", raw, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return nil, fmt.Errorf("versiond URL %q must include scheme and host", raw)
	}
	return u, nil
}
