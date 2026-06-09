package admin

import (
	"decentralized-api/broker"

	"github.com/productscience/inference/x/inference/types"
)

// nodeTestBlockReason returns a human-readable reason when testing nodeId
// would risk disrupting broker-managed work. It uses node-specific broker
// state, so idle/spare nodes remain testable while assigned, preserved,
// locked, reconciling, and PoC nodes are protected.
//
// This is a best-effort, point-in-time check: the test then runs for up to a
// few minutes outside the broker's state machine, so a node assigned or
// preserved AFTER the check could still be stopped mid-test. We accept this
// rather than add a broker-level test lease, because (a) auto-tests only run
// when a node looks idle and there is >1h until PoC, making mid-test
// reassignment unlikely, and (b) the broker's next-block reconciliation
// re-brings-up any node the test left stopped. Eliminating the window
// entirely would require an atomic "reserve-if-idle" in the broker, which the
// onboarding design deliberately avoids (no broker state/commands for UX).
func (s *Server) nodeTestBlockReason(nodeId string) string {
	nodes, err := s.nodeBroker.GetNodes()
	if err != nil {
		return "could not determine whether node is safe to test"
	}
	for _, n := range nodes {
		if n.Node.Id != nodeId {
			continue
		}
		return nodeStateTestBlockReason(n.State)
	}
	// Let MLNodeTester return ErrNodeNotFound for nodes absent from the broker.
	return ""
}

// pocImminentTestBlockReason returns a human-readable reason when the node
// should already be online for an imminent/active PoC, so starting a
// multi-minute MLnode test (which stops the node) would risk taking it out of
// service when it is needed. Unlike auto-test (gated a full hour out so it
// never even begins near PoC), the manual endpoint lets an operator test much
// closer in; we only refuse once the node is in the "must be online" window
// (ShouldBeOnline: during a PoC phase, or within OnlineAlertLeadSeconds of the
// next PoC). An unknown schedule (tracker not synced, or no phase tracker)
// returns "" so the operator's discretion applies.
func (s *Server) pocImminentTestBlockReason() string {
	if s.phaseTracker == nil {
		return ""
	}
	timing := ComputeTiming(s.phaseTracker.GetCurrentEpochState())
	if timing == nil {
		return ""
	}
	if timing.ShouldBeOnline {
		return "the node should be online for an imminent or active PoC (" +
			formatShortDuration(timing.SecondsUntilNextPoC) +
			" until next PoC); testing would take it out of service"
	}
	return ""
}

func nodeStateTestBlockReason(state broker.NodeState) string {
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
	default:
		return ""
	}
}
