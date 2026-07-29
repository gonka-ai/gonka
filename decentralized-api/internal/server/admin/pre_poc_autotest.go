package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
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
// outside the pre-PoC window, and runs the actual test in a goroutine.
// Iterating broker-registered nodes (not raw config) means a node that failed
// to register is never tested; the synced-state gate means an active node is
// never mistaken for an idle one during startup.
//
// Auto-test is an onboarding aid, so it is skipped entirely for a participant
// already in the active set: those nodes are (or are about to be) doing real
// work, and a self-initiated stop outside the broker's state machine is not
// something a production participant should ever incur. Their operators can
// still test explicitly via POST /nodes/:id/test.
//
// Reads the broker's node list once per block and passes each node's state down,
// rather than re-querying the broker per node (that was N+1 command round-trips
// on the block-processing path, which runs synchronously in the dispatcher).
func (s *Server) OnEpochState(epochState *chainphase.EpochState) {
	if s.tester == nil || epochState.IsNilOrNotSynced() {
		return
	}
	if s.participantIsActive() {
		return
	}
	// One shared timing decision per block: it is identical for every node, and
	// evaluating it up front avoids touching the broker at all when the window
	// is closed (the common case).
	if reason := pocTestBlockReason(epochState, apiconfig.AutoTestMinSecondsBeforePoC); reason != "" {
		return
	}
	// Only one node can be under test at a time; skip the sweep when one is
	// already running instead of queuing up per-node rejections.
	if s.tester.IsAnyRunning() {
		return
	}
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		if s.maybeAutoTestNode(n) {
			// Started a test: the global limit means no other node can start
			// one now, so stop the sweep and let the next block continue.
			return
		}
	}
}

// participantIsActive reports whether this participant is known to be in the
// active set. Unknown counts as not active, which is the onboarding case
// auto-test exists for; the activity tracker only reports Active after a
// successful chain observation.
func (s *Server) participantIsActive() bool {
	return s.activityTracker != nil && s.activityTracker.IsActive()
}

// maybeAutoTest fires a one-shot MLnode validation in the background
// after a node is registered or its config changes, implementing the
// proposal's "auto-test a new MLnode when there's >1h until PoC". It is
// fire-and-forget: the result is recorded on the MLNodeTester and
// surfaces through GET /nodes via computeOnboarding — nothing is written
// back to the broker. No-op when the participant is active, the schedule is
// unknown, PoC is near, a node is busy with broker-managed work, or a test
// for this node is already running/recorded (with a backoff for transient
// failures).
func (s *Server) maybeAutoTest(nodeId string) {
	if s.tester == nil {
		return
	}
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return
	}
	for _, n := range nodes {
		if n.Node.Id == nodeId {
			s.maybeAutoTestNode(n)
			return
		}
	}
}

// maybeAutoTestNode is maybeAutoTest for a node whose broker view the caller
// already holds. Returns true when a background test was started.
func (s *Server) maybeAutoTestNode(n broker.NodeResponse) bool {
	if s.tester == nil {
		return false
	}
	nodeId := n.Node.Id
	if s.participantIsActive() {
		return false
	}
	if s.tester.IsRunning(nodeId) {
		return false
	}
	// A recorded result belongs to the current configuration revision.
	// Successful tests do not need to run again, and deterministic config
	// failures wait for an operator/config change rather than retrying
	// continuously. A transient (retryable) failure — e.g. a brief MLnode or
	// RPC hiccup — is retried after a backoff so it can self-heal without
	// manual intervention (OnEpochState calls this every synced block).
	if last := s.tester.LastResult(nodeId); last != nil {
		if last.Status != TestFailed || !last.Retryable {
			return false
		}
		// Back off from when the attempt ENDED, not when it started: a test can
		// itself take minutes, so measuring from StartedAt could make a
		// 5-minute failed run immediately eligible for a retry.
		since := last.FinishedAt
		if since.IsZero() {
			since = last.StartedAt
		}
		if time.Since(since) < time.Duration(apiconfig.AutoTestRetryBackoffSeconds)*time.Second {
			return false
		}
	}
	if s.phaseTracker == nil {
		return false
	}
	// Never auto-test a node involved in broker-managed work: the test
	// reloads + stops the MLnode outside the broker's state machine.
	if reason := s.nodeTestBlockReasonFor(n); reason != "" {
		return false
	}
	// Same schedule gate as the manual endpoint, with auto-test's much wider
	// margin. Fail-closed: an unknown schedule blocks.
	if reason := pocTestBlockReason(s.phaseTracker.GetCurrentEpochState(), apiconfig.AutoTestMinSecondsBeforePoC); reason != "" {
		return false
	}
	go s.runAutoTest(nodeId)
	return true
}

// runAutoTest performs the background test and logs the outcome. The context is
// detached from any request, but bounded by the same budget the manual endpoint
// uses so a wedged MLnode cannot hold the node's test slot indefinitely.
func (s *Server) runAutoTest(nodeId string) {
	ctx, cancel := context.WithTimeout(context.Background(),
		time.Duration(apiconfig.NodeTestTimeoutSeconds)*time.Second)
	defer cancel()
	result, err := s.tester.Run(ctx, nodeId)
	switch {
	case errors.Is(err, ErrTestInProgress), errors.Is(err, ErrTestBusy):
		// A test is already running (for this node or another); nothing to do.
	case err != nil:
		logging.Debug("Auto-test could not run", types.Nodes, "node_id", nodeId, "error", err)
	case result != nil && result.Status == TestFailed:
		// Report the failure prominently in the API node logs so the
		// operator can fix it before PoC (proposal: detailed error
		// reporting on TEST_FAILED).
		logging.Error("Auto-test failed for MLnode before PoC", types.Nodes,
			"node_id", nodeId, "failing_model", result.FailingModel,
			"retryable", result.Retryable, "error", result.Error)
	case result != nil:
		logging.Info("Auto-test passed for MLnode", types.Nodes,
			"node_id", nodeId, "duration_ms", result.DurationMs)
	}
}
