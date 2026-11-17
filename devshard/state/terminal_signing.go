package state

import (
	"context"
	"fmt"
	"time"

	"devshard/types"
)

// TerminalSigningSlots rechecks the existing signer's nonce bindings against a
// fresh authenticated tip without applying that tip to the frozen final state.
// The host must separately verify its durable admission watermark and root.
// New unbound signers and past nonces are deliberately outside this operation.
func (sm *StateMachine) TerminalSigningSlots(ctx context.Context, nonce uint64, signer string, candidates []uint32) ([]uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, mainnetCheckpointRPCTimeout)
	defer cancel()
	sm.mu.RLock()
	if !types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion) || sm.state.Phase != types.PhaseSettlement || nonce != sm.state.LatestNonce {
		sm.mu.RUnlock()
		return nil, types.ErrInvalidTransition
	}
	historical := sm.consensusActorsAtLocked(nonce)
	cp := cloneCheckpoint(sm.state.MainnetCheckpoint)
	source, times, checker, escrow := sm.mainnetHeights, sm.headerTimes, sm.warmHeightCheck, sm.state.EscrowID
	sm.mu.RUnlock()

	// Retain the ordinary canonical check even though the old block may now be
	// outside the age/lag window. A fresh proof alone is not an age exemption
	// for a conflicting frozen checkpoint.
	if err := checkCanonicalCheckpoint(ctx, cp, source, times); err != nil {
		return nil, err
	}
	fresh, err := sm.BuildMainnetHeightTx(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: terminal signing tip: %v", types.ErrHeaderPending, err)
	}
	cp.Height, cp.Time = fresh.GetMainnetHeight().Height, time.Unix(0, fresh.GetMainnetHeight().TimeUnixNano).UTC()
	current := sm.historicalWarmActors(historical, cp, nonce, checker, escrow, true)
	var allowed []uint32
	var pending error
	for _, slot := range candidates {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: terminal signing: %v", types.ErrHeaderPending, err)
		}
		if historical.Allows(slot, signer) && current.Allows(slot, signer) {
			allowed = append(allowed, slot)
		} else if current.AuthorizationError != nil {
			if err := current.AuthorizationError(slot, signer); err != nil {
				pending = err
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: terminal signing: %v", types.ErrHeaderPending, err)
	}
	if len(allowed) == 0 {
		if pending != nil {
			return nil, fmt.Errorf("%w: terminal signer grant: %v", types.ErrHeaderPending, pending)
		}
		return nil, types.ErrWarmKeyUnconfirmed
	}
	return allowed, nil
}
