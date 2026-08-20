package admin

// Read-only "fact" endpoints. Each exposes one small factual query so an
// operator, a script, or an AI skill can compose its own onboarding flow on
// top of primitives, rather than relying only on the derived onboarding
// envelope in GET /nodes. They surface the same internals the envelope is
// derived from — the raw last test result, the launch plan a node would use
// right now, and PoC timing — and write nothing back to the broker.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
)

// governanceQueryTimeout bounds a read-only handler's chain query.
const governanceQueryTimeout = 15 * time.Second

// getNodeTestResult handles GET /admin/v1/nodes/:id/test: the raw result of
// the most recent MLnode test for this node (POST runs a test, this reads it
// back).
//
//	200 — {node_id, running, last: TestResult|null} (last is null when no
//	      test has been recorded for the current configuration)
//	404 — node id not in configManager
func (s *Server) getNodeTestResult(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "node id is required"})
	}
	if s.tester == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tester not initialized"})
	}
	if !s.tester.HasNode(nodeId) {
		return c.JSON(http.StatusNotFound, map[string]string{"error": ErrNodeNotFound.Error(), "node_id": nodeId})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{
		"node_id": nodeId,
		"running": s.tester.IsRunning(nodeId),
		"last":    s.tester.LastResult(nodeId),
	})
}

// getNodeLaunchPlan handles GET /admin/v1/nodes/:id/launch-plan: which models
// this node would launch right now and with which final (governance + local)
// args, plus configured models that would not launch and why. Read-only; the
// MLnode itself is never contacted.
//
//	200 — LaunchPlanReport
//	404 — node id not in configManager
//	502 — governance models unavailable (upstream chain query failed)
func (s *Server) getNodeLaunchPlan(c echo.Context) error {
	nodeId := c.Param("id")
	if nodeId == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "node id is required"})
	}
	if s.tester == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "tester not initialized"})
	}
	// Bound the governance query by the request context so a hung chain RPC
	// cannot hold the handler (and its connection) open indefinitely.
	ctx, cancel := context.WithTimeout(c.Request().Context(), governanceQueryTimeout)
	defer cancel()
	report, err := s.tester.LaunchPlans(ctx, nodeId)
	switch {
	case errors.Is(err, ErrNodeNotFound):
		return c.JSON(http.StatusNotFound, map[string]string{"error": err.Error(), "node_id": nodeId})
	case err != nil:
		// The only other failure is the governance-model chain query: an
		// upstream dependency, so 502 rather than 500.
		return c.JSON(http.StatusBadGateway, map[string]string{"error": err.Error(), "node_id": nodeId})
	}
	return c.JSON(http.StatusOK, report)
}

// getPoCTiming handles GET /admin/v1/poc/timing: the chain-phase timing facts
// (current phase, blocks/seconds until the next PoC, whether nodes should be
// online for it). available=false until the chain is synced, instead of
// reporting zeros that look like an imminent PoC.
//
//	200 — {available: true, timing: TimingInfo} | {available: false}
func (s *Server) getPoCTiming(c echo.Context) error {
	if s.phaseTracker == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{"available": false})
	}
	timing := ComputeTiming(s.phaseTracker.GetCurrentEpochState())
	if timing == nil {
		return c.JSON(http.StatusOK, map[string]interface{}{"available": false})
	}
	return c.JSON(http.StatusOK, map[string]interface{}{"available": true, "timing": timing})
}
