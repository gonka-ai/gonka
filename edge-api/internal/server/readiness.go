package server

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cosmos/cosmos-sdk/client/grpc/cmtservice"
	"github.com/labstack/echo/v4"

	"common/chain"
)

const (
	// readinessCacheTTL keeps an active health check from turning into chain
	// load: the balancer probes every second, the chain is asked far less often.
	readinessCacheTTL = 3 * time.Second
	readinessTimeout  = 2 * time.Second
)

// readiness answers "may this instance take new traffic", which for a read-only
// Tier A service means "can it still reach the chain". Liveness stays on
// /healthz: a process that is up but cut off from the chain must leave the
// rotation without being restarted.
type readiness struct {
	chain *chain.Client

	// draining latches at shutdown. It is checked before the chain probe, so a
	// leaving instance reports unready at once instead of waiting out the cache.
	draining atomic.Bool

	mu        sync.Mutex
	checkedAt time.Time
	lastErr   error
}

func newReadiness(chainClient *chain.Client) *readiness {
	return &readiness{chain: chainClient}
}

// beginDrain makes this instance report unready while it keeps serving. The
// balancer needs a moment to observe the failing check and stop routing here;
// that window is owned by the caller (see the announce window in cmd/edge-api).
func (r *readiness) beginDrain() { r.draining.Store(true) }

func (r *readiness) check(ctx context.Context) error {
	r.mu.Lock()
	if !r.checkedAt.IsZero() && time.Since(r.checkedAt) < readinessCacheTTL {
		err := r.lastErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	probeCtx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()
	_, err := r.chain.CometServiceClient().GetNodeInfo(
		probeCtx,
		&cmtservice.GetNodeInfoRequest{},
	)

	r.mu.Lock()
	r.checkedAt = time.Now()
	r.lastErr = err
	r.mu.Unlock()
	return err
}

func (r *readiness) handler(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if r.draining.Load() {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": "draining",
		})
	}
	if err := r.check(c.Request().Context()); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": "chain unreachable",
			"error":  err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
}
