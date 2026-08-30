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
// base until one returns 200 or ctx is done. That is the versiond-router
// catalog-admission signal. In-process clients have no catalog URL and
// return immediately so unit tests keep sending heartbeats.
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
	if probeRouterCatalog(client, urls) {
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
			if probeRouterCatalog(client, urls) {
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

func probeRouterCatalog(client *http.Client, urls []string) bool {
	for _, u := range urls {
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
	}
	return false
}
