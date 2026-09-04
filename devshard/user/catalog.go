package user

import (
	"context"
	"net/http"
	"strings"
	"time"

	"devshard/logging"
)

const catalogWaitTick = time.Second

type catalogHealthzSource interface {
	CatalogHealthzURL() string
}

// WaitRouterCatalog polls GET /{version}/healthz on each HTTP client's host
// base until one returns 200 or ctx is done. That path is catalog admission
// on the versiond-router, child health on versiond, and the matching probe
// on e2e hosts. 503, 404, and dial errors keep waiting — root /healthz is
// never consulted (router process-up is not this version). In-process
// clients have no catalog URL and return immediately.
func (s *Session) WaitRouterCatalog(ctx context.Context) error {
	if s == nil {
		return nil
	}
	urls := s.catalogHealthzURLs()
	if len(urls) == 0 {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	client := &http.Client{Timeout: 5 * time.Second}
	if probeRouterCatalog(client, urls) == catalogProbeAdmitted {
		return nil
	}
	logging.Debug("waiting for router catalog", "subsystem", "heightsync",
		"escrow", s.escrowID, "urls", strings.Join(urls, ","))
	ticker := time.NewTicker(catalogWaitTick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if probeRouterCatalog(client, urls) == catalogProbeAdmitted {
				logging.Debug("router catalog admitted", "subsystem", "heightsync",
					"escrow", s.escrowID)
				return nil
			}
		}
	}
}

func (s *Session) catalogHealthzURLs() []string {
	s.mu.Lock()
	clients := append([]HostClient(nil), s.clients...)
	s.mu.Unlock()
	seen := make(map[string]struct{}, len(clients))
	var urls []string
	for _, c := range clients {
		src, ok := c.(catalogHealthzSource)
		if !ok {
			continue
		}
		u := strings.TrimSpace(src.CatalogHealthzURL())
		if u == "" {
			continue
		}
		if _, dup := seen[u]; dup {
			continue
		}
		seen[u] = struct{}{}
		urls = append(urls, u)
	}
	return urls
}

type catalogProbe int

const (
	catalogProbeWait catalogProbe = iota
	catalogProbeAdmitted
)

// probeRouterCatalog classifies GET /{version}/healthz on the HTTP client bases.
// 200 means this version is serving. Anything else keeps waiting.
func probeRouterCatalog(client *http.Client, urls []string) catalogProbe {
	if len(urls) == 0 {
		return catalogProbeWait
	}
	for _, u := range urls {
		code, err := httpStatus(client, u)
		if err == nil && code == http.StatusOK {
			return catalogProbeAdmitted
		}
	}
	return catalogProbeWait
}

func httpStatus(client *http.Client, rawURL string) (int, error) {
	resp, err := client.Get(rawURL)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
