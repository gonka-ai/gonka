package devshardobs

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	defaultVersionsCacheTTL = 2 * time.Second
	statusRunning           = "running"
)

// VersionsSource returns running protocol version names (unsorted).
type VersionsSource interface {
	ActiveVersions(ctx context.Context) ([]string, error)
}

type healthStatusEntry struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// HealthzVersions fetches running versions from versiond GET /healthz.
type HealthzVersions struct {
	base   *url.URL
	client *http.Client
	ttl    time.Duration

	mu        sync.Mutex
	cached    []string
	cachedAt  time.Time
	cacheErr  error
}

// NewHealthzVersions polls versiond supervisor health for running children.
// ttl <= 0 uses defaultVersionsCacheTTL.
func NewHealthzVersions(base *url.URL, client *http.Client, ttl time.Duration) *HealthzVersions {
	if client == nil {
		client = http.DefaultClient
	}
	if ttl <= 0 {
		ttl = defaultVersionsCacheTTL
	}
	return &HealthzVersions{base: base, client: client, ttl: ttl}
}

// ActiveVersions implements VersionsSource.
func (h *HealthzVersions) ActiveVersions(ctx context.Context) ([]string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.cachedAt) < h.ttl && h.cached != nil {
		out := append([]string(nil), h.cached...)
		return out, h.cacheErr
	}

	versions, err := h.fetch(ctx)
	h.cached = versions
	h.cachedAt = time.Now()
	h.cacheErr = err
	out := append([]string(nil), versions...)
	return out, err
}

func (h *HealthzVersions) fetch(ctx context.Context) ([]string, error) {
	u := h.base.ResolveReference(&url.URL{Path: "/healthz"})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("versiond healthz: status %d", resp.StatusCode)
	}
	var entries []healthStatusEntry
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		return nil, fmt.Errorf("decode versiond healthz: %w", err)
	}
	seen := make(map[string]struct{}, len(entries))
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if !strings.EqualFold(e.Status, statusRunning) {
			continue
		}
		name := strings.TrimSpace(e.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out, nil
}

// StaticVersions is a fixed list for tests.
type StaticVersions []string

// ActiveVersions implements VersionsSource.
func (s StaticVersions) ActiveVersions(context.Context) ([]string, error) {
	return append([]string(nil), s...), nil
}
