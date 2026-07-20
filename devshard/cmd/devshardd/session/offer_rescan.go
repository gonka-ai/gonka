package session

import (
	"context"
	"time"

	"devshard/host"
)

const (
	DefaultOfferRescanInterval = 15 * time.Second
	minOfferRescanInterval     = 5 * time.Second
	maxOfferRescanInterval     = 2 * time.Minute
)

// offerRescanHosts is the HostManager surface OfferRescan needs.
type offerRescanHosts interface {
	ActiveEscrowIDs() []string
	ExistingHost(escrowID string) (*host.Host, bool)
}

// OfferRescan periodically re-runs discovery Offers for quiet escrows that
// have no inbound HandleRequest traffic.
type OfferRescan struct {
	manager  offerRescanHosts
	interval time.Duration
}

// NewOfferRescan creates an OfferRescan ticker (default 15s).
func NewOfferRescan(manager *HostManager) *OfferRescan {
	return &OfferRescan{
		manager:  manager,
		interval: DefaultOfferRescanInterval,
	}
}

// WithInterval overrides the rescan period (clamped to [5s, 2m]).
func (r *OfferRescan) WithInterval(d time.Duration) *OfferRescan {
	r.interval = clampOfferRescanInterval(d)
	return r
}

func clampOfferRescanInterval(d time.Duration) time.Duration {
	if d < minOfferRescanInterval {
		return minOfferRescanInterval
	}
	if d > maxOfferRescanInterval {
		return maxOfferRescanInterval
	}
	return d
}

// Run starts the rescan ticker. Blocks until ctx is cancelled.
func (r *OfferRescan) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			r.runOnce()
		}
	}
}

func (r *OfferRescan) runOnce() {
	for _, escrowID := range r.manager.ActiveEscrowIDs() {
		h, ok := r.manager.ExistingHost(escrowID)
		if !ok {
			continue
		}
		h.RescanValidationOffers()
	}
}
