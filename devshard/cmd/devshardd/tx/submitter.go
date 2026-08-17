package tx

import (
	"context"

	chainbridge "devshard/cmd/devshardd/bridge"
)

// disputeSubmitter adapts *Manager to bridge.Submitter (no ctx on the bridge interface).
type disputeSubmitter struct {
	mgr *Manager
}

// NewDisputeSubmitter returns a bridge.Submitter backed by the gRPC tx manager.
func NewDisputeSubmitter(mgr *Manager) chainbridge.Submitter {
	return &disputeSubmitter{mgr: mgr}
}

func (s *disputeSubmitter) SubmitDisputeState(escrowID uint64, stateRoot []byte, nonce uint64, sigs map[uint32][]byte) error {
	return s.mgr.SubmitDisputeState(context.Background(), escrowID, stateRoot, nonce, sigs)
}
