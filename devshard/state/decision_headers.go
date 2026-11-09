package state

import "devshard/types"

// withResolvedDecision runs fn while sm.mu is held. Seal and drain read the
// carried floor, so this does not wait on the header map. Replay never waits.
func (sm *StateMachine) withResolvedDecision(nonce uint64, _ []*types.DevshardTx, replay bool, fn func() error) error {
	if !replay {
		if err := sm.waitFloorTimeIfMissing(nonce); err != nil {
			return err
		}
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	prevReplay := sm.replayingPersisted
	if replay {
		sm.replayingPersisted = true
	}
	defer func() { sm.replayingPersisted = prevReplay }()
	return fn()
}

// waitFloorTimeIfMissing is a no-op. Seal and drain read a carried floor
// point, and TimeoutDue reads the carried time of the current floor. A header
// map row must not change either clock, so settlement does not wait for one.
// Caller must not hold sm.mu.
func (sm *StateMachine) waitFloorTimeIfMissing(uint64) error {
	return nil
}

// maySettlementDrainOnNonceLocked reports that applying nonce would run
// drainLiveIntoSealedAccLocked (Finalizing deadline reached). Caller holds sm.mu.
func (sm *StateMachine) maySettlementDrainOnNonceLocked(nonce uint64) bool {
	if sm.state.Phase != types.PhaseFinalizing {
		return false
	}
	fin := sm.state.FinalizeNonce
	if fin == 0 {
		fin = nonce
	}
	group := uint64(len(sm.state.Group))
	return nonce >= fin+group
}
