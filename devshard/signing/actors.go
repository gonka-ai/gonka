package signing

// SlotActors is the identity set that may produce a host signature for a slot.
//
// Both binaries (devshardd and devshardctl) recover signatures with Verifier
// and then call Allows. The same rule applies to HeightAck, confirm, finish,
// votes, repair, settlement, and gossip: cold slot key, or this slot's bound
// warm key; if the slot is still unbound, a sibling slot's bound warm key for
// the same validator, or AcceptWarm (live authz / CheckWarmKey / ResolveWarmKey).
type SlotActors struct {
	SlotKeys map[uint32]string
	WarmKeys map[uint32]string
	// AcceptWarm is the non-cache (or bind-on-success) fallback used when
	// WarmKeys has no entry for this slot. Nil means cache-only.
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
// Bound slots are exclusive: cold key or that slot's warm key. Sibling lookup
// and AcceptWarm run only when the slot has no binding yet, which is the
// HeightAck case (a heartbeat can name a slot that has never executed).
//
// Everything above AcceptWarm is decided from consensus state alone, so every
// replica answers it identically. AcceptWarm is the only step that can consult
// the chain, and it is reached only for an unbound slot.
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
	if warm := a.WarmKeys[slotID]; warm != "" {
		return recovered == warm
	}
	for other, warm := range a.WarmKeys {
		if warm == "" || recovered != warm {
			continue
		}
		if a.SlotKeys[other] == key {
			return true
		}
	}
	return a.AcceptWarm != nil && a.AcceptWarm(slotID, recovered, key)
}
