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

func filteredWarmHistory(history signing.WarmKeyHistory, nonce uint64, valid func(uint32, signing.WarmKeyBinding) bool) signing.WarmKeyHistory {
	out := make(signing.WarmKeyHistory, len(history))
	for slot, entries := range history {
		for _, b := range entries {
			if !bindingActive(b, nonce) || valid(slot, b) {
				out[slot] = append(out[slot], b)
			}
		}
	}
	return out
}

func (sm *StateMachine) consensusWarmHistoryLocked(nonce uint64) signing.WarmKeyHistory {
	if !types.RequiresMainnetHeightProof(sm.state.StateRootAndProtocolVersion) {
		return cloneWarmKeyHistory(sm.state.WarmKeyHistory)
	}
	return filteredWarmHistory(sm.state.WarmKeyHistory, nonce, func(slot uint32, b signing.WarmKeyBinding) bool {
		return sm.warmHeightValidity[warmGrantKey{sm.slotToAddress[slot], b.GrantID, b.Addr}]
	})
}

// historicalWarmActors runs after releasing sm.mu. The current checkpoint
// covers latest and the next admission nonce. Earlier state signatures use
// their own already verified journal prelude, never today's grant status.
func (sm *StateMachine) historicalWarmActors(actors signing.SlotActors, cp lightproof.Checkpoint, latest uint64, checker WarmKeyHeightChecker, escrowID string) signing.SlotActors {
	if actors.Nonce < latest {
		records, err := sm.inferenceStore.GetDiffs(escrowID, actors.Nonce, actors.Nonce)
		if err != nil || len(records) != 1 {
			actors.History = nil
			return actors
		}
		var height *types.MsgMainnetHeight
		if len(records[0].Txs) > 0 {
			height = records[0].Txs[0].GetMainnetHeight()
		}
		if height == nil || height.Height <= 0 || height.TimeUnixNano == 0 {
			actors.History = nil
			return actors
		}
		cp.Height = height.Height
		cp.Time = time.Unix(0, height.TimeUnixNano).UTC()
	}
	valid := make(map[warmGrantKey]bool)
	actors.History = filteredWarmHistory(actors.History, actors.Nonce, func(slot uint32, b signing.WarmKeyBinding) bool {
		key := warmGrantKey{actors.SlotKeys[slot], b.GrantID, b.Addr}
		if ok, seen := valid[key]; seen {
			return ok
		}
		ok := false
		if checker != nil {
			var err error
			ok, err = checker(key.cold, b.Addr, b.GrantID, cp.Height, cp.Time)
			ok = ok && err == nil
		}
		valid[key] = ok
		return ok
	})
	return actors
}
