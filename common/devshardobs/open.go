package devshardobs

import (
	"context"
	"fmt"
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
// Always returns a Handler that talks to versiond (default URL); lookup may be nil.
func OpenFromEnv(ctx context.Context) (*Router, error) {
	base, err := VersiondBaseFromEnv()
	if err != nil {
		return nil, err
	}

	lookup, err := OpenLookupFromEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("session lookup: %w", err)
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
