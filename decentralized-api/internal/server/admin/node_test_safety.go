package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"

	"github.com/productscience/inference/x/inference/types"
)

// nodeTestBlockReason returns a human-readable reason when testing nodeId
// would risk disrupting broker-managed work. It uses node-specific broker
// state, so genuinely idle/spare nodes remain testable while assigned,
// preserved, locked, reconciling, routable and PoC nodes are protected.
//
// This is a best-effort, point-in-time check: the test then runs for up to a
// few minutes outside the broker's state machine, so a node assigned or
// preserved AFTER the check could still be stopped mid-test. The window is
// narrowed rather than eliminated: tests only start when the node looks idle by
// the same criteria the router uses, when there is a wide margin before PoC
// (see pocTestBlockReason), and only one node may be under test at a time.
// Closing the window entirely would require an atomic "reserve-if-idle" in the
// broker, which the onboarding design deliberately avoids (no broker state or
// commands for UX); the residual exposure is a stopped node that the broker's
// status query + reconciler bring back within roughly a minute.
func (s *Server) nodeTestBlockReason(nodeId string) string {
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return "could not determine whether node is safe to test"
	}
	for _, n := range nodes {
		if n.Node.Id != nodeId {
			continue
		}
		return s.nodeTestBlockReasonFor(n)
	}
	// Let MLNodeTester return ErrNodeNotFound for nodes absent from the broker.
	return ""
}

// nodeTestBlockReasonFor is the form used when the caller already holds the
// node's broker view (the per-block auto-test sweep reads all nodes once instead
// of re-querying the broker per node).
func (s *Server) nodeTestBlockReasonFor(n broker.NodeResponse) string {
	return nodeStateTestBlockReason(n.State, s.currentEpochAndPhase())
}

// epochPhase carries the epoch index and phase needed to evaluate a node's admin
// state. known is false when the chain phase is unavailable, which callers must
// treat as fail-closed rather than assuming the node is disabled or enabled.
type epochPhase struct {
	epoch uint64
	phase types.EpochPhase
	known bool
}

// currentEpochAndPhase reads the epoch/phase from the tracker. Returns
// known=false when there is no tracker or it has not synced.
func (s *Server) currentEpochAndPhase() epochPhase {
	if s.phaseTracker == nil {
		return epochPhase{}
	}
	es := s.phaseTracker.GetCurrentEpochState()
	if es.IsNilOrNotSynced() {
		return epochPhase{}
	}
	return epochPhase{epoch: es.LatestEpoch.EpochIndex, phase: es.CurrentPhase, known: true}
}

// pocTestBlockReason returns a human-readable reason when the chain schedule
// makes it unsafe to start an MLnode test, or "" when there is enough slack.
// minSlackSeconds is how far the next PoC must be for the test to be allowed;
// callers pass a wide margin for automatic tests and the narrower
// operator-discretion floor for the manual endpoint.
//
// Fail-closed by design. A test stops the MLnode for minutes, so when the
// schedule is unknown (no phase tracker, or the tracker has not synced) we
// refuse rather than assume there is time — the old fail-open behavior would
// happily start a multi-minute test on a node that, for all we knew, was due in
// PoC on the next block.
func pocTestBlockReason(es *chainphase.EpochState, minSlackSeconds int64) string {
	if es.IsNilOrNotSynced() {
		return "chain phase is not known yet (node still syncing); refusing to take the MLnode out of service"
	}
	timing := ComputeTiming(es)
	if timing == nil {
		return "chain phase is not known yet (node still syncing); refusing to take the MLnode out of service"
	}
	if timing.InPoCWindow {
		return "a PoC window is active (" + timing.CurrentPhase + "); testing would take the node out of service"
	}
	if timing.SecondsUntilNextPoC < 0 {
		return "the next PoC time is unknown; refusing to take the MLnode out of service"
	}
	if timing.SecondsUntilNextPoC < minSlackSeconds {
		return "the next PoC is too close (" + formatShortDuration(timing.SecondsUntilNextPoC) +
			" away, need at least " + formatShortDuration(minSlackSeconds) +
			"); testing would take the node out of service"
	}
	return ""
}

// pocImminentTestBlockReason applies pocTestBlockReason to the manual test
// endpoint. The manual floor is OnlineAlertLeadSeconds plus the test's own
// timeout, so even a test that runs to its deadline finishes before the node is
// expected online — the previous ShouldBeOnline-only gate let an operator start
// a 5-minute test 601 seconds before PoC and leave the node stopped well inside
// the must-be-online window.
func (s *Server) pocImminentTestBlockReason() string {
	if s.phaseTracker == nil {
		return "chain phase tracker is not available; refusing to take the MLnode out of service"
	}
	return pocTestBlockReason(s.phaseTracker.GetCurrentEpochState(), apiconfig.ManualTestMinSecondsBeforePoC)
}

// nodeStateTestBlockReason reports why the broker's view of a node makes it
// unsafe to test, or "" when the node is idle.
//
// The routable check is the important one: it mirrors broker.nodeAvailable, the
// predicate the router actually uses to pick a node for an inference request. A
// node with epoch models that is up in INFERENCE and not reconciling can be
// handed a request at any moment — even when it holds no lock right now and is
// not in the epoch assignment, because the broker populates EpochModels from
// the node's configured model for participants outside the active set. Testing
// such a node would stop it outside the state machine and fail in-flight
// requests, so it is treated as busy. Nodes the router cannot pick (STOPPED,
// FAILED, UNKNOWN, or without epoch models) stay testable — which is exactly
// the onboarding case this feature exists for.
//
// An administratively disabled node is NOT routable, so it stays testable: that
// is the operator's remediation when this check refuses a serving node. When the
// chain phase is unknown (ep.known == false) admin state cannot be evaluated, so
// a node that is otherwise routable is refused — fail-closed, consistent with
// pocTestBlockReason.
func nodeStateTestBlockReason(state broker.NodeState, ep epochPhase) string {
	switch {
	case len(state.EpochMLNodes) > 0:
		return "node is assigned in the current epoch"
	case len(state.PreservedModels) > 0:
		return "node is preserved for inference service"
	case state.LockCount > 0:
		return "node is serving one or more inference requests"
	case state.ReconcileInfo != nil:
		return "node is currently reconciling"
	case state.CurrentStatus == types.HardwareNodeStatus_POC ||
		state.IntendedStatus == types.HardwareNodeStatus_POC:
		return "node is participating in PoC"
	case isRoutableForInference(state, ep):
		return "node is serving inference and can receive requests at any time"
	default:
		return ""
	}
}

// isRoutableForInference reports whether the router could pick this node for an
// inference request right now. Mirrors every condition of broker.nodeAvailable
// except the ones with their own, more specific reason above (lock count,
// reconciling, epoch assignment): intended and current status both INFERENCE,
// not reconciling, administratively operational, and at least one epoch model to
// serve.
//
// The admin-state term is evaluated only when the epoch/phase is known. With an
// unknown phase we cannot tell an enabled node from a disabled one, so we report
// routable (i.e. refuse the test) rather than assume the node is disabled.
func isRoutableForInference(state broker.NodeState, ep epochPhase) bool {
	if state.IntendedStatus != types.HardwareNodeStatus_INFERENCE ||
		state.CurrentStatus != types.HardwareNodeStatus_INFERENCE ||
		state.ReconcileInfo != nil ||
		len(state.EpochModels) == 0 {
		return false
	}
	if !ep.known {
		return true
	}
	return state.ShouldBeOperational(ep.epoch, ep.phase)
}
