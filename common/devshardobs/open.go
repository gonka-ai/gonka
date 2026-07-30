package devshardobs

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Router is the dual-serve wiring bundle: versionless handler + optional PG lookup.
type Router struct {
	Handler *Handler
	Lookup  *Lookup // may be nil
}

// Close releases Postgres resources.
func (r *Router) Close() {
	if r != nil && r.Lookup != nil {
		r.Lookup.Close()
	}
}

// OpenFromEnv builds a versionless obs router for dapi/edge-api.
// Always returns a Handler that talks to versiond (default URL). Session lookup
// may be nil (disabled, unconfigured, or init failure → fan-out only).
func OpenFromEnv(ctx context.Context) (*Router, error) {
	base, err := VersiondBaseFromEnv()
	if err != nil {
		return nil, err
	}

	// Lookup init failures must not disable versionless obs: degrade to
	// fan-out-only (same behavior versiond had when PG was unreachable).
	lookup, err := OpenLookupFromEnv(ctx)
	if err != nil {
		slog.Warn("devshardobs: session lookup unavailable; continuing fan-out only", "error", err)
		lookup = nil
	}

	ttl := defaultVersionsCacheTTL
	if v := os.Getenv("DEVSHARD_OBS_VERSIONS_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ttl = d
		} else if sec, err := strconv.Atoi(v); err == nil && sec > 0 {
			ttl = time.Duration(sec) * time.Second
		} else {
			slog.Warn("devshardobs: invalid DEVSHARD_OBS_VERSIONS_CACHE_TTL; using default", "value", v)
		}
	}

	client := &http.Client{Timeout: 10 * time.Second}
	versions := NewHealthzVersions(base, client, ttl)
	h, err := NewHandler(Config{
		VersiondBase: base,
		Lookup:       lookup,
		Versions:     versions,
	})
	if err != nil {
		if lookup != nil {
			lookup.Close()
		}
		return nil, err
	}

	slog.Info("devshardobs: versionless obs router ready",
		"versiond", base.String(),
		"lookup_enabled", lookup != nil,
		"versions_cache_ttl", ttl.String(),
	)
	return &Router{Handler: h, Lookup: lookup}, nil
}
