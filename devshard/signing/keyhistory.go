package signing

import (
	"errors"
	"fmt"
	"sort"
)

// WarmKeyBinding is one warm key bound to a slot for a range of nonces. It
// comes from a MsgWarmKeyBind applied at FromNonce and ends at the nonce of
// the MsgWarmKeyRevoke that ends it; RevokedAtNonce is zero while the key is
// live.
//
// Sequence is the granter's account sequence of the chain transaction that
// attests the binding (the cold-signed MsgGrant), and RevokeSequence that of
// the transaction that ends it (the MsgRevoke). Sequences only grow, so an
// attestation cannot be replayed: a bind must carry a sequence above every
// entry the slot has seen, and a revoke ends only a binding with a lower
// sequence than its own. Height is the chain height the attestation was
// confirmed at; ExpiresAt is the grant's expiry as unix seconds, zero for
// none. Attestation is the signed transaction, kept so a snapshot carries
// the proof with the history.
//
// See docs/warm-keys-v7.md.
type WarmKeyBinding struct {
	Addr           string
	FromNonce      uint64
	Sequence       uint64
	Height         uint64
	ExpiresAt      int64
	Attestation    []byte
	RevokedAtNonce uint64
	RevokeSequence uint64
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
	ErrWarmKeySequence = errors.New("warm key attestation sequence is not above the slot's latest")
	ErrWarmKeyNotFound = errors.New("warm key is not bound on this slot")
)

// Bind appends the live binding b to slot. b.FromNonce is the diff that
// carries it. A key that is already live on the slot cannot be bound twice;
// a key revoked earlier can be bound again with a newer attestation. Entries
// are append-only and ordered by nonce, so a bind at a nonce before the
// slot's latest entry is refused: the diffs would have been applied out of
// order. The attestation sequence must be above every sequence the slot has
// seen, bind or revoke, so a replayed attestation is refused too.
func (h WarmKeyHistory) Bind(slot uint32, b WarmKeyBinding) error {
	if b.Addr == "" {
		return ErrWarmKeyEmpty
	}
	b.RevokedAtNonce, b.RevokeSequence = 0, 0
	entries := h[slot]
	if last, ok := latestEntryNonce(entries); ok && b.FromNonce < last {
		return fmt.Errorf("%w: slot %d nonce %d after %d", ErrWarmKeyNonce, slot, b.FromNonce, last)
	}
	if last := latestSequence(entries); b.Sequence <= last {
		return fmt.Errorf("%w: slot %d sequence %d after %d", ErrWarmKeySequence, slot, b.Sequence, last)
	}
	for _, e := range entries {
		if e.Addr == b.Addr && e.RevokedAtNonce == 0 {
			return fmt.Errorf("%w: slot %d %s", ErrWarmKeyLive, slot, b.Addr)
		}
	}
	h[slot] = append(entries, b)
	return nil
}

// Revoke ends the live binding of addr on slot at nonce, with the revoking
// attestation's sequence. The sequence must be above the slot's latest, so a
// stale revoke cannot end a binding made after it. Signatures in diffs before
// nonce keep verifying; from nonce on the key is refused on this slot.
func (h WarmKeyHistory) Revoke(slot uint32, addr string, nonce, sequence uint64) error {
	if addr == "" {
		return ErrWarmKeyEmpty
	}
	entries := h[slot]
	if last, ok := latestEntryNonce(entries); ok && nonce < last {
		return fmt.Errorf("%w: slot %d nonce %d after %d", ErrWarmKeyNonce, slot, nonce, last)
	}
	if last := latestSequence(entries); sequence <= last {
		return fmt.Errorf("%w: slot %d sequence %d after %d", ErrWarmKeySequence, slot, sequence, last)
	}
	for i := range entries {
		if entries[i].Addr != addr || entries[i].RevokedAtNonce != 0 {
			continue
		}
		entries[i].RevokedAtNonce = nonce
		entries[i].RevokeSequence = sequence
		return nil
	}
	for _, e := range entries {
		if e.Addr == addr {
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

// Allows reports whether recovered may act for slot in the diff at nonce.
// slotKeys maps every slot of the group to its cold address.
//
// The rule mirrors SlotActors.Allows, with the history in place of the single
// binding: the cold key; a warm key covering nonce on this slot; and, only
// while nothing covers nonce on this slot, a warm key covering nonce on a
// sibling slot of the same validator. A key revoked on this slot at or before
// nonce is refused on this slot whatever its sibling bindings say: a revoke
// is final for the slot it names. There is no chain fallback here: a key the
// history does not cover is refused until a MsgWarmKeyBind for it is applied.
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
	if h.revokedOn(slot, recovered, nonce) {
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

// revokedOn reports whether addr has a binding on slot that was revoked at
// or before nonce.
func (h WarmKeyHistory) revokedOn(slot uint32, addr string, nonce uint64) bool {
	for _, b := range h[slot] {
		if b.Addr == addr && b.RevokedAtNonce != 0 && b.RevokedAtNonce <= nonce {
			return true
		}
	}
	return false
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

// latestSequence is the highest attestation sequence the slot has seen, bind
// or revoke; zero for an empty slot.
func latestSequence(entries []WarmKeyBinding) uint64 {
	var latest uint64
	for _, b := range entries {
		if b.Sequence > latest {
			latest = b.Sequence
		}
		if b.RevokeSequence > latest {
			latest = b.RevokeSequence
		}
	}
	return latest
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
