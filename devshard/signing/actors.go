package signing

// SlotActors is the identity set that may produce a host signature for a slot.
//
// Both binaries (devshardd and devshardctl) recover signatures with Verifier
// and then call Allows. The same rule applies to HeightAck, confirm, finish,
// votes, repair, settlement, and gossip: the cold slot key, or a warm key the
// history binds to the slot at Nonce, or, while the slot has no binding at
// Nonce, a sibling slot's binding for the same validator; and, only where
// AcceptWarm is set, the grant source for a key the history does not cover.
type SlotActors struct {
	SlotKeys map[uint32]string
	// History is the consensus binding history of the escrow and Nonce the
	// authorization nonce of the caller's path: the diff being applied, the
	// signed state nonce, or the next diff for a local admission decision.
	History WarmKeyHistory
	Nonce   uint64
	// AcceptWarm is the local-admission fallback for a key the history does
	// not cover: the grant source (the chain's warm-key log). It is never
	// consulted on a consensus path, which leaves it nil, and never for a key
	// the history has revoked on the slot at or before Nonce.
	AcceptWarm func(slotID uint32, recovered, expected string) bool
}

// Exact is a SlotActors that accepts only key for slotID. Tests and callers
// that have already resolved the acting address use this.
func Exact(slotID uint32, key string) SlotActors {
	if key == "" {
		return SlotActors{}
	}
	return SlotActors{SlotKeys: map[uint32]string{slotID: key}}
}

// Expected is the cold slot address, if the slot is in the group.
func (a SlotActors) Expected(slotID uint32) (string, bool) {
	key, ok := a.SlotKeys[slotID]
	return key, ok && key != ""
}

// Allows reports whether recovered may act for slotID.
//
// Everything above AcceptWarm is decided from consensus state alone, so every
// replica answers it identically. AcceptWarm is the only step that can consult
// anything else, and a revoke in the history on this slot overrides it.
func (a SlotActors) Allows(slotID uint32, recovered string) bool {
	if recovered == "" {
		return false
	}
	key, ok := a.Expected(slotID)
	if !ok {
		return false
	}
	if recovered == key {
		return true
	}
	if a.History.Allows(a.SlotKeys, slotID, recovered, a.Nonce) {
		return true
	}
	if a.AcceptWarm == nil || a.History.RevokedOn(slotID, recovered, a.Nonce) {
		return false
	}
	return a.AcceptWarm(slotID, recovered, key)
}
