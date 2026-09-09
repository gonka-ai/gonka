package main

import (
	"devshard/bridge"
	"errors"
	"log"
	"sync"
)

// EscrowChecker verifies escrow existence against the chain when a host
// reports "escrow not found". Concurrent checks for the same escrow are
// deduplicated to a single chain query.
type EscrowChecker struct {
	mu        sync.Mutex
	inflight  map[string]bool
	newBridge func() bridge.MainnetBridge
}

func NewEscrowChecker(newBridge func() bridge.MainnetBridge) *EscrowChecker {
	return &EscrowChecker{
		inflight:  make(map[string]bool),
		newBridge: newBridge,
	}
}

// TriggerCheck queries the chain for the given escrow. If confirmed missing or
// settled, calls deactivate with the reason. If another check for the same
// escrow is already in flight, this call returns immediately (the in-flight
// check will handle deactivation).
func (ec *EscrowChecker) TriggerCheck(escrowID string, deactivate func(reason string)) {
	ec.triggerCheck(escrowID, deactivate)
}

func (ec *EscrowChecker) triggerCheck(escrowID string, deactivate func(reason string)) {
	ec.mu.Lock()
	if ec.inflight[escrowID] {
		ec.mu.Unlock()
		return
	}
	ec.inflight[escrowID] = true
	ec.mu.Unlock()

	defer func() {
		ec.mu.Lock()
		delete(ec.inflight, escrowID)
		ec.mu.Unlock()
	}()

	newBridge := ec.newBridge
	if newBridge == nil {
		log.Printf("escrow %s chain check skipped: bridge unavailable", escrowID)
		return
	}
	br := newBridge()
	if br == nil {
		log.Printf("escrow %s chain check skipped: bridge unavailable", escrowID)
		return
	}
	info, err := br.GetEscrow(escrowID)
	if err != nil {
		if errors.Is(err, bridge.ErrEscrowNotFound) {
			log.Printf("escrow %s confirmed missing on chain, deactivating devshard", escrowID)
			deactivate("escrow confirmed missing on chain")
		} else {
			log.Printf("escrow %s chain check failed (keeping active): %v", escrowID, err)
		}
		return
	}
	if info != nil && info.Settled {
		log.Printf("escrow %s confirmed settled on chain, deactivating devshard", escrowID)
		deactivate("escrow confirmed settled on chain")
		return
	}
	log.Printf("escrow %s verified on chain, host reported false escrow-not-found", escrowID)
}
