package state

import (
	"fmt"
	"time"

	"common/chainoracle/blocks/lightproof"
	"devshard/signing"
	"devshard/types"
)

// WarmKeyHeightChecker answers from a chain view complete through height.
// It must use the supplied authenticated block time, including on replay.
type WarmKeyHeightChecker func(cold, warm string, grantID uint64, height int64, blockTime time.Time) (bool, error)

type warmGrantKey struct {
	cold string
	id   uint64
	warm string
}

func WithWarmKeyHeightChecker(check WarmKeyHeightChecker) SMOption {
	return func(sm *StateMachine) { sm.warmHeightCheck = check }
}
func (sm *StateMachine) SetWarmKeyHeightChecker(check WarmKeyHeightChecker) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.warmHeightCheck = check
}

// prepareWarmHeightLocked preloads decisions for the diff's bindings before
// signature verification. The derived cache is replaced atomically with its
// checkpoint and journaled for preview/rollback. It is not hashed and is
// rebuilt from the chain on snapshot recovery. Completeness errors fail closed
// on every apply path, including ApplyLocalPersisted.
func (sm *StateMachine) prepareWarmHeightLocked(nonce uint64, txs []*types.DevshardTx) (func(), error) {
	if !types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion) {
		return func() {}, nil
	}
	checked := make(map[warmGrantKey]bool)
	check := func(slot uint32, warm string, id uint64) error {
		cold := sm.slotToAddress[slot]
		key := warmGrantKey{cold, id, warm}
		if _, ok := checked[key]; ok {
			return nil
		}
		if sm.warmHeightCheck == nil {
			return fmt.Errorf("%w: no height-aware warm-key source", types.ErrHeaderPending)
		}
		cp := sm.state.MainnetCheckpoint
		allowed, err := sm.warmHeightCheck(cold, warm, id, cp.Height, cp.Time)
		if err != nil {
			return fmt.Errorf("%w: warm-key grant %s/%d at height %d: %v", types.ErrHeaderPending, cold, id, cp.Height, err)
		}
		checked[key] = allowed
		return nil
	}
	for slot, entries := range sm.state.WarmKeyHistory {
		for _, b := range entries {
			if bindingActive(b, nonce) {
				if err := check(slot, b.Addr, b.GrantID); err != nil {
					return nil, err
				}
			}
		}
	}
	for _, tx := range txs {
		if b := tx.GetWarmKeyBind(); b != nil {
			if err := check(b.SlotId, b.Address, b.GrantId); err != nil {
				return nil, err
			}
		}
	}
	before := sm.warmHeightValidity
	sm.warmHeightValidity = checked
	return func() { sm.warmHeightValidity = before }, nil
}

func bindingActive(b signing.WarmKeyBinding, nonce uint64) bool {
	return b.FromNonce <= nonce && (b.RevokedAtNonce == 0 || b.RevokedAtNonce > nonce)
}

// restrictWarmActors keeps all nonce bindings, including occupied slots whose
// grants are no longer valid. Removing them would enable sibling fallback.
func restrictWarmActors(actors signing.SlotActors, valid func(uint32, signing.WarmKeyBinding) bool) signing.SlotActors {
	checked := make(map[warmGrantKey]bool)
	keys := actors.SlotKeys
	for slot, entries := range actors.History {
		for _, b := range entries {
			if bindingActive(b, actors.Nonce) {
				checked[warmGrantKey{keys[slot], b.GrantID, b.Addr}] = valid(slot, b)
			}
		}
	}
	actors.BindingAllowed = func(slot uint32, b signing.WarmKeyBinding) bool {
		return checked[warmGrantKey{keys[slot], b.GrantID, b.Addr}]
	}
	return actors
}

// historicalWarmActors runs after releasing sm.mu. The current checkpoint
// covers latest and the next admission nonce. Earlier state signatures use
// their own already verified journal prelude, never today's grant status.
func (sm *StateMachine) historicalWarmActors(actors signing.SlotActors, cp lightproof.Checkpoint, latest uint64, checker WarmKeyHeightChecker, escrowID string, lazy bool) signing.SlotActors {
	if actors.Nonce < latest {
		records, err := sm.inferenceStore.GetDiffs(escrowID, actors.Nonce, actors.Nonce)
		if err != nil || len(records) != 1 {
			actors.History = nil
			actors.AuthorizationError = func(uint32, string) error {
				return fmt.Errorf("%w: historical authorization prelude unavailable", types.ErrHeaderPending)
			}
			return actors
		}
		var height *types.MsgMainnetHeight
		if len(records[0].Txs) > 0 {
			height = records[0].Txs[0].GetMainnetHeight()
		}
		if height == nil || height.Height <= 0 || height.TimeUnixNano == 0 {
			actors.History = nil
			actors.AuthorizationError = func(uint32, string) error {
				return fmt.Errorf("%w: historical authorization prelude unavailable", types.ErrHeaderPending)
			}
			return actors
		}
		cp.Height = height.Height
		cp.Time = time.Unix(0, height.TimeUnixNano).UTC()
	}
	lookupErrors := make(map[[2]string]error)
	valid := make(map[warmGrantKey]bool)
	checkBinding := func(slot uint32, b signing.WarmKeyBinding) bool {
		key := warmGrantKey{actors.SlotKeys[slot], b.GrantID, b.Addr}
		if ok, seen := valid[key]; seen {
			return ok
		}
		ok := false
		if checker != nil {
			var err error
			ok, err = checker(key.cold, b.Addr, b.GrantID, cp.Height, cp.Time)
			if err != nil {
				lookupErrors[[2]string{key.cold, b.Addr}] = err
			}
			ok = ok && err == nil
		} else {
			lookupErrors[[2]string{key.cold, b.Addr}] = fmt.Errorf("%w: no height-aware warm-key source", types.ErrHeaderPending)
		}
		valid[key] = ok
		return ok
	}
	if lazy {
		// A bounded local batch only resolves identities it actually uses.
		// The maps remain request-scoped, like AcceptWarm/RefuseWarm below.
		actors.BindingAllowed = checkBinding
	} else {
		actors = restrictWarmActors(actors, checkBinding)
	}
	keys := actors.SlotKeys
	actors.AuthorizationError = func(slot uint32, recovered string) error { return lookupErrors[[2]string{keys[slot], recovered}] }
	return actors
}
