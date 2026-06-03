package admin

import "decentralized-api/chainphase"

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
