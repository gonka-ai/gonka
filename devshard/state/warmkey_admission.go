package state

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"common/chainoracle/blocks"
	"common/chainoracle/blocks/lightproof"
	"devshard/types"
)

type warmAdmissionDecision struct {
	allowed bool
	err     error
}

// warmAdmission is a request-scoped, host-local policy. It is neither consensus
// state nor a claim that a signature was created before its advertised block.
// One current authenticated checkpoint and one complete grant view per key are
// shared by a batch. Cold-only diffs need no extra live lookup.
type warmAdmission struct {
	ctx        context.Context
	checkpoint *lightproof.Checkpoint
	decisions  map[warmGrantKey]warmAdmissionDecision
	err        error
}

// allowWarmAdmissionLocked is deliberately separate from warmHeightValidity:
// changing that historical cache would change binding/revoke consensus rules.
// SlotActors calls this only for a signature actually using a warm binding.
func (sm *StateMachine) allowWarmAdmissionLocked(key warmGrantKey) bool {
	a := sm.warmAdmission
	if a == nil {
		return true
	}
	if err := a.ctx.Err(); err != nil {
		a.err = fmt.Errorf("%w: live warm-key admission: %v", types.ErrHeaderPending, err)
		return false
	}
	if a.checkpoint == nil {
		if sm.mainnetHeights == nil {
			a.err = fmt.Errorf("%w: no live warm-key admission height source", types.ErrHeaderPending)
			return false
		}
		tip, err := sm.mainnetHeights.Latest(a.ctx)
		if err != nil || tip == nil || blocks.IsDummyHeader(tip) || tip.Height < sm.state.MainnetCheckpoint.Height {
			a.err = fmt.Errorf("%w: live warm-key admission tip unavailable: %v", types.ErrHeaderPending, err)
			return false
		}
		_, cp, err := lightproof.Build(a.ctx, sm.state.MainnetCheckpoint, tip.Height, sm.mainnetHeights)
		if err != nil {
			a.err = fmt.Errorf("%w: live warm-key admission proof: %v", types.ErrHeaderPending, err)
			return false
		}
		if !bytes.Equal(cp.Hash, tip.BlockHash) {
			a.err = fmt.Errorf("%w: live warm-key admission tip hash", types.ErrHeaderMismatch)
			return false
		}
		if err := checkCheckpointAge(cp, time.Now()); err != nil {
			a.err = err
			return false
		}
		a.checkpoint = &cp
	}
	if sm.state.MainnetCheckpoint.Height > a.checkpoint.Height {
		a.err = fmt.Errorf("%w: diff exceeds live warm-key admission tip", types.ErrHeaderPending)
		return false
	}
	if decision, ok := a.decisions[key]; ok {
		if !decision.allowed {
			a.err = decision.err
		}
		return decision.allowed
	}
	if sm.warmHeightCheck == nil {
		a.err = fmt.Errorf("%w: no complete warm-key admission grant source", types.ErrHeaderPending)
		return false
	}
	cp := a.checkpoint
	allowed, err := sm.warmHeightCheck(key.cold, key.warm, key.id, cp.Height, cp.Time)
	if err != nil {
		a.err = fmt.Errorf("%w: live warm-key admission grant %s/%d at %d: %v", types.ErrHeaderPending, key.cold, key.id, cp.Height, err)
		return false
	}
	if err := a.ctx.Err(); err != nil {
		a.err = fmt.Errorf("%w: live warm-key admission: %v", types.ErrHeaderPending, err)
		return false
	}
	if !allowed {
		a.err = fmt.Errorf("%w: warm-key grant %s/%d ended at live admission height %d", types.ErrInvalidWarmKeyEntry, key.cold, key.id, cp.Height)
	}
	decision := warmAdmissionDecision{allowed: allowed}
	if !allowed {
		decision.err = a.err
	}
	a.decisions[key] = decision
	return allowed
}
