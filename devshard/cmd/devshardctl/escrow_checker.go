package main

import (
	"errors"
	"log"
	"sync"

	"devshard/bridge"
)

// EscrowChecker verifies escrow existence against the chain when a host
// reports "escrow not found". Concurrent checks for the same escrow are
// deduplicated to a single chain query.
type EscrowChecker struct {
	mu        sync.Mutex
	inflight  map[string]bool
	chainREST func() string
}

func NewEscrowChecker(chainREST func() string) *EscrowChecker {
	return &EscrowChecker{
		inflight:  make(map[string]bool),
		chainREST: chainREST,
	}
}

// TriggerCheck queries the chain for the given escrow. If confirmed missing,
// calls deactivate. If another check for the same escrow is already in flight,
// this call returns immediately (the in-flight check will handle deactivation).
func (ec *EscrowChecker) TriggerCheck(escrowID string, deactivate func()) {
	ec.triggerCheck(escrowID, deactivate)
}

func (ec *EscrowChecker) triggerCheck(escrowID string, deactivate func()) {
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

	br := newRESTBridgeForProtocol(ec.chainREST(), "")
	_, err := br.GetEscrow(escrowID)
	if err != nil {
		if errors.Is(err, bridge.ErrEscrowNotFound) {
			log.Printf("escrow %s confirmed missing on chain, deactivating devshard", escrowID)
			deactivate()
		} else {
			log.Printf("escrow %s chain check failed (keeping active): %v", escrowID, err)
		}
		return
	}
	log.Printf("escrow %s verified on chain, host reported false escrow-not-found", escrowID)
}
