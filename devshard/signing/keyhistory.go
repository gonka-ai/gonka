package signing

import (
	"errors"
	"fmt"
	"sort"
)

// WarmKeyBinding is one warm key bound to a slot for a range of nonces. It
// comes from a MsgWarmKeyBind applied at FromNonce and ends at the nonce of
// the MsgWarmKeyRevoke that names it; RevokedAtNonce is zero while the key is
// live. Height is the chain height at which the grant was known to exist when
// the binding was proposed; it is what a host verifies the grant against.
//
// See docs/warm-keys-v7.md.
type WarmKeyBinding struct {
	Addr           string
	FromNonce      uint64
	Height         uint64
	RevokedAtNonce uint64
}

// Covers reports whether the binding is in force for a signature produced at
// nonce: at or after FromNonce and before the revoke, if any.
func (b WarmKeyBinding) Covers(nonce uint64) bool {
	if nonce < b.FromNonce {
		return false
	}
	return b.RevokedAtNonce == 0 || nonce < b.RevokedAtNonce
}

// WarmKeyHistory is every binding ever applied to an escrow, by slot. It is
// consensus state: it changes only through diffs, and the acceptance rule
// (Allows) is a function of the history and the nonce, so every host that has
// applied the same diffs answers the same way.
type WarmKeyHistory map[uint32][]WarmKeyBinding

var (
	ErrWarmKeyEmpty    = errors.New("warm key address is empty")
	ErrWarmKeyLive     = errors.New("warm key is already live on this slot")
	ErrWarmKeyNotLive  = errors.New("warm key is not live on this slot")
	ErrWarmKeyNonce    = errors.New("warm key nonce is not after the previous entry")
	ErrWarmKeyNotFound = errors.New("warm key is not bound on this slot")
)

// Bind appends a live binding of addr to slot from nonce on. A key that is
// already live on the slot cannot be bound twice; a key revoked earlier can
// be bound again. Entries are append-only and ordered by nonce, so a bind at
// a nonce before the slot's latest entry is refused: it would mean the diffs
// were applied out of order.
func (h WarmKeyHistory) Bind(slot uint32, addr string, nonce, height uint64) error {
	if addr == "" {
		return ErrWarmKeyEmpty
	}
	entries := h[slot]
	if last, ok := latestEntryNonce(entries); ok && nonce < last {
		return fmt.Errorf("%w: slot %d nonce %d after %d", ErrWarmKeyNonce, slot, nonce, last)
	}
	for _, b := range entries {
		if b.Addr == addr && b.RevokedAtNonce == 0 {
			return fmt.Errorf("%w: slot %d %s", ErrWarmKeyLive, slot, addr)
		}
	}
	h[slot] = append(entries, WarmKeyBinding{Addr: addr, FromNonce: nonce, Height: height})
	return nil
}

// Revoke ends the live binding of addr on slot at nonce. Signatures produced
// before nonce keep verifying; from nonce on the key is refused.
func (h WarmKeyHistory) Revoke(slot uint32, addr string, nonce uint64) error {
	if addr == "" {
		return ErrWarmKeyEmpty
	}
	entries := h[slot]
	if last, ok := latestEntryNonce(entries); ok && nonce < last {
		return fmt.Errorf("%w: slot %d nonce %d after %d", ErrWarmKeyNonce, slot, nonce, last)
	}
	for i := range entries {
		if entries[i].Addr != addr {
			continue
		}
		if entries[i].RevokedAtNonce != 0 {
			continue
		}
		entries[i].RevokedAtNonce = nonce
		return nil
	}
	for _, b := range entries {
		if b.Addr == addr {
			return fmt.Errorf("%w: slot %d %s", ErrWarmKeyNotLive, slot, addr)
		}
	}
	return fmt.Errorf("%w: slot %d %s", ErrWarmKeyNotFound, slot, addr)
}

// ActiveAt returns the warm keys that cover nonce on slot, in binding order.
func (h WarmKeyHistory) ActiveAt(slot uint32, nonce uint64) []string {
	var keys []string
	for _, b := range h[slot] {
		if b.Covers(nonce) {
			keys = append(keys, b.Addr)
		}
	}
	return keys
}

// Live returns the warm keys with no revoke on slot, in binding order.
func (h WarmKeyHistory) Live(slot uint32) []string {
	var keys []string
	for _, b := range h[slot] {
		if b.RevokedAtNonce == 0 {
			keys = append(keys, b.Addr)
		}
	}
	return keys
}

// Allows reports whether recovered may act for slot on a signature produced
// at nonce. slotKeys maps every slot of the group to its cold address.
//
// The rule mirrors SlotActors.Allows, with the history in place of the single
// binding: the cold key; a warm key covering nonce on this slot; and, only
// while nothing covers nonce on this slot, a warm key covering nonce on a
// sibling slot of the same validator. There is no chain fallback here: a key
// the history does not cover is refused until a MsgWarmKeyBind for it is
// applied.
func (h WarmKeyHistory) Allows(slotKeys map[uint32]string, slot uint32, recovered string, nonce uint64) bool {
	if recovered == "" {
		return false
	}
	cold, ok := slotKeys[slot]
	if !ok || cold == "" {
		return false
	}
	if recovered == cold {
		return true
	}
	active := h.ActiveAt(slot, nonce)
	if len(active) > 0 {
		for _, key := range active {
			if key == recovered {
				return true
			}
		}
		return false
	}
	siblings := make([]uint32, 0, len(h))
	for other := range h {
		if other != slot && slotKeys[other] == cold {
			siblings = append(siblings, other)
		}
	}
	sort.Slice(siblings, func(i, j int) bool { return siblings[i] < siblings[j] })
	for _, other := range siblings {
		for _, key := range h.ActiveAt(other, nonce) {
			if key == recovered {
				return true
			}
		}
	}
	return false
}

// ActorsAt projects the history at nonce onto SlotActors, for callers that
// still take the single-binding form. A slot with several keys covering nonce
// keeps the most recently bound one; Allows is the complete rule.
func (h WarmKeyHistory) ActorsAt(slotKeys map[uint32]string, nonce uint64) SlotActors {
	warm := make(map[uint32]string, len(h))
	for slot := range h {
		if active := h.ActiveAt(slot, nonce); len(active) > 0 {
			warm[slot] = active[len(active)-1]
		}
	}
	return SlotActors{SlotKeys: slotKeys, WarmKeys: warm}
}

// Clone returns a deep copy, for state snapshots and journal undo.
func (h WarmKeyHistory) Clone() WarmKeyHistory {
	if h == nil {
		return nil
	}
	out := make(WarmKeyHistory, len(h))
	for slot, entries := range h {
		out[slot] = append([]WarmKeyBinding(nil), entries...)
	}
	return out
}

// latestEntryNonce is the nonce of the slot's most recent change: the last
// bind, or a later revoke of any entry.
func latestEntryNonce(entries []WarmKeyBinding) (uint64, bool) {
	if len(entries) == 0 {
		return 0, false
	}
	latest := entries[len(entries)-1].FromNonce
	for _, b := range entries {
		if b.RevokedAtNonce > latest {
			latest = b.RevokedAtNonce
		}
	}
	return latest, true
}
