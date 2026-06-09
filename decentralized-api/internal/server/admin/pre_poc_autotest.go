package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/logging"
	"errors"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// OnEpochState is invoked by the block dispatcher after each synced phase-tracker
// update (i.e. on every new block once the chain is synced). It retries pre-PoC
// validation for broker-registered nodes that have not yet been tested for their
// current configuration — covering nodes loaded from config at process startup
// and nodes whose first auto-test was skipped while the schedule was unknown or
// too close to PoC.
//
// Safe to call every block: maybeAutoTest is idempotent and asynchronous — it
// skips nodes already tested, in-flight, busy with broker-managed work, or
// outside the >1h pre-PoC window, and runs the actual test in a goroutine.
// Iterating broker-registered nodes (not raw config) means a node that failed
// to register is never tested; the synced-state gate means an active node is
// never mistaken for an idle one during startup.
func (s *Server) OnEpochState(epochState *chainphase.EpochState) {
	if s.tester == nil || epochState.IsNilOrNotSynced() {
		return
	}
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		s.maybeAutoTest(n.Node.Id)
	}
}

// shouldAutoTest reports whether a node should be auto-tested right now.
// We only auto-test when the next PoC is more than
// AutoTestMinSecondsBeforePoC away, so loading models for a validation
// run never disturbs an imminent PoC. An unknown schedule
// (SecondsUntilPoCUnknown / negative) returns false.
func shouldAutoTest(secondsUntilNextPoC int64) bool {
	return secondsUntilNextPoC > apiconfig.AutoTestMinSecondsBeforePoC
}

// maybeAutoTest fires a one-shot MLnode validation in the background
// after a node is registered or its config changes, implementing the
// proposal's "auto-test a new MLnode when there's >1h until PoC". It is
// fire-and-forget: the result is recorded on the MLNodeTester and
// surfaces through GET /nodes via computeOnboarding — nothing is written
// back to the broker. No-op when the schedule is unknown, PoC is near,
// a node is busy with broker-managed work, or a test for this node is
// already running/recorded (with a backoff for transient failures).
func (s *Server) maybeAutoTest(nodeId string) {
	if s.tester == nil {
		return
	}
	if s.tester.IsRunning(nodeId) {
		return
	}
	// A recorded result belongs to the current configuration revision.
	// Successful tests do not need to run again, and deterministic config
	// failures wait for an operator/config change rather than retrying
	// continuously. A transient (retryable) failure — e.g. a brief MLnode or
	// RPC hiccup — is retried after a backoff so it can self-heal without
	// manual intervention (OnEpochState calls this every synced block).
	if last := s.tester.LastResult(nodeId); last != nil {
		if last.Status != TestFailed || !last.Retryable {
			return
		}
		if time.Since(last.StartedAt) < time.Duration(apiconfig.AutoTestRetryBackoffSeconds)*time.Second {
			return
		}
	}
	if s.phaseTracker == nil {
		return
	}
	// Never auto-test a node involved in broker-managed work: the test
	// reloads + stops the MLnode outside the broker's state machine.
	if reason := s.nodeTestBlockReason(nodeId); reason != "" {
		return
	}
	timing := ComputeTiming(s.phaseTracker.GetCurrentEpochState())
	if timing == nil || !shouldAutoTest(timing.SecondsUntilNextPoC) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		result, err := s.tester.Run(ctx, nodeId)
		switch {
		case errors.Is(err, ErrTestInProgress):
			// A test is already running for this node; nothing to do.
		case err != nil:
			logging.Debug("Auto-test could not run", types.Nodes, "node_id", nodeId, "error", err)
		case result != nil && result.Status == TestFailed:
			// Report the failure prominently in the API node logs so the
			// operator can fix it before PoC (proposal: detailed error
			// reporting on TEST_FAILED).
			logging.Error("Auto-test failed for MLnode before PoC", types.Nodes,
				"node_id", nodeId, "failing_model", result.FailingModel, "error", result.Error)
		case result != nil:
			logging.Info("Auto-test passed for MLnode", types.Nodes,
				"node_id", nodeId, "duration_ms", result.DurationMs)
		}
	}()
}
