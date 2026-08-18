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
	// readinessTimeout is how slow the chain may be before this instance calls
	// it unreachable. The router's `timeout check` must exceed it (it ships 3s;
	// see edge-api-router/haproxy.cfg.template): with only `inter 1s` the whole
	// check would time out before a 1–2s chain answer arrives, and every
	// instance — they share the chain node — would leave the pool at once over
	// a chain that is merely slow.
	readinessTimeout = 2 * time.Second
)

// readiness answers "may this instance take new traffic", which for a read-only
// Tier A service means "can it still reach the chain". Liveness stays on
// /healthz: a process that is up but cut off from the chain must leave the
// rotation without being restarted.
type readiness struct {
	// probe asks the chain; injectable so tests can shape its timing.
	probe func(ctx context.Context) error

	// drainMu makes the final readiness response linearizable with BeginDrain:
	// after BeginDrain returns, no concurrent handler can still publish 200.
	drainMu  sync.RWMutex
	draining bool

	// probeMu serialises the probe itself: when the cache expires, every
	// concurrent /readyz would otherwise fire its own chain query.
	probeMu sync.Mutex

	mu        sync.Mutex
	checkedAt time.Time
	lastErr   error
}

func newReadiness(chainClient *chain.Client) *readiness {
	r := &readiness{}
	r.probe = func(ctx context.Context) error {
		_, err := chainClient.CometServiceClient().GetNodeInfo(
			ctx,
			&cmtservice.GetNodeInfoRequest{},
		)
		return err
	}
	return r
}

// beginDrain makes this instance report unready while it keeps serving. The
// balancer needs a moment to observe the failing check and stop routing here;
// that window is owned by the caller (see the announce window in cmd/edge-api).
func (r *readiness) beginDrain() {
	r.drainMu.Lock()
	r.draining = true
	r.drainMu.Unlock()
}

func (r *readiness) isDraining() bool {
	r.drainMu.RLock()
	defer r.drainMu.RUnlock()
	return r.draining
}

func (r *readiness) check() error {
	if err, ok := r.cached(); ok {
		return err
	}

	r.probeMu.Lock()
	defer r.probeMu.Unlock()
	// A caller that waited here rode on another caller's probe; its answer is
	// in the cache now.
	if err, ok := r.cached(); ok {
		return err
	}

	// The probe runs on its own context, never the request's. The answer is
	// about the chain, not about the caller's patience: a health checker that
	// gives up and closes the connection would otherwise cancel the probe and
	// cache the cancellation as "chain unreachable" for every check that
	// follows in the next TTL.
	probeCtx, cancel := context.WithTimeout(context.Background(), readinessTimeout)
	defer cancel()
	err := r.probe(probeCtx)

	r.mu.Lock()
	r.checkedAt = time.Now()
	r.lastErr = err
	r.mu.Unlock()
	return err
}

func (r *readiness) cached() (error, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.checkedAt.IsZero() || time.Since(r.checkedAt) >= readinessCacheTTL {
		return nil, false
	}
	return r.lastErr, true
}

func (r *readiness) handler(c echo.Context) error {
	c.Response().Header().Set("Cache-Control", "no-store")
	if r.isDraining() {
		return drainingResponse(c)
	}
	err := r.check()
	// BeginDrain can race a probe already in progress. Drain is a latch, so its
	// newer answer must win even if that older chain probe eventually succeeds.
	// Hold the read lock through response publication: either this 200 completes
	// first, or BeginDrain completes first and this request returns 503.
	r.drainMu.RLock()
	defer r.drainMu.RUnlock()
	if r.draining {
		return drainingResponse(c)
	}
	if err != nil {
		return c.JSON(http.StatusServiceUnavailable, map[string]string{
			"status": "not ready",
			"reason": "chain unreachable",
			"error":  err.Error(),
		})
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
}

func drainingResponse(c echo.Context) error {
	return c.JSON(http.StatusServiceUnavailable, map[string]string{
		"status": "not ready",
		"reason": "draining",
	})
}
