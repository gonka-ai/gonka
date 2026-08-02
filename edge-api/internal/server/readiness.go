package server

import (
	"context"
	"net/http"
	"sync"
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

	mu        sync.Mutex
	checkedAt time.Time
	lastErr   error
}

func newReadiness(chainClient *chain.Client) *readiness {
	return &readiness{chain: chainClient}
}

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
	if err := r.check(c.Request().Context()); err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": "chain unreachable",
			"error":  err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
}
