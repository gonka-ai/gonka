package host

import (
	"fmt"

	"subnet/types"
)

// StalenessChecker implements AcceptanceChecker by withholding when the
// mempool contains entries older than a grace window. The signing decision
// lives here, not in the mempool itself.
type StalenessChecker struct {
	mempool *Mempool
	grace   uint64
}

func NewStalenessChecker(mempool *Mempool, grace uint64) *StalenessChecker {
	return &StalenessChecker{mempool: mempool, grace: grace}
}

func (s *StalenessChecker) Check(st types.EscrowState, _ []*types.SubnetTx) error {
	if s.mempool.HasStaleEntry(st.LatestNonce, s.grace) {
		return fmt.Errorf("mempool tx pending for >%d nonces", s.grace)
	}
	return nil
}
