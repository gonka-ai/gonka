package state

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"devshard/types"
)

// FinalStateSigningSlots authorizes an attestation of the exact final identity
// (escrow, nonce, root), independently of advancing the diff checkpoint.
// The host must separately verify its durable admission watermark and root.
// New unbound signers and past nonces are deliberately outside this operation.
func (sm *StateMachine) FinalStateSigningSlots(ctx context.Context, final *types.StateSignatureContent, signer string, candidates []uint32) ([]uint32, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, mainnetCheckpointRPCTimeout)
	defer cancel()
	sm.mu.RLock()
	if final == nil || !types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion) || sm.state.Phase != types.PhaseSettlement || final.EscrowId != sm.state.EscrowID || final.Nonce != sm.state.LatestNonce {
		sm.mu.RUnlock()
		return nil, types.ErrInvalidTransition
	}
	root, err := sm.computeStateRootLocked()
	if err != nil {
		sm.mu.RUnlock()
		return nil, err
	}
	if !bytes.Equal(final.StateRoot, root) {
		sm.mu.RUnlock()
		return nil, fmt.Errorf("%w: final attestation root differs from local state", types.ErrHeaderPending)
	}
	historical := sm.consensusActorsAtLocked(final.Nonce)
	cp := cloneCheckpoint(sm.state.MainnetCheckpoint)
	source, times, checker, escrow := sm.mainnetHeights, sm.headerTimes, sm.warmHeightCheck, sm.state.EscrowID
	sm.mu.RUnlock()

	// The final checkpoint remains canonical even when its age/lag window has
	// ended. Live evidence authorizes the signer, never a different final root.
	if err := checkCanonicalCheckpoint(ctx, cp, source, times); err != nil {
		return nil, err
	}
	tip, err := latestMainnetHeader(ctx, source)
	if err != nil {
		return nil, err
	}
	// A recent carried checkpoint already supplies authenticated evidence under
	// the ordinary freshness policy. Preserve this path when the separate light-
	// block endpoint is down. Otherwise authenticate fresh evidence directly,
	// without constructing a MsgMainnetHeight or changing consensus state.
	if checkCheckpointAge(cp, time.Now()) != nil || checkCheckpointLag(cp, tip) != nil {
		cp, _, err = authenticateMainnetTip(ctx, cp, tip, source)
		if err != nil {
			return nil, fmt.Errorf("%w: final attestation tip: %v", types.ErrHeaderPending, err)
		}
	}
	current := sm.historicalWarmActors(historical, cp, final.Nonce, checker, escrow, true)
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
