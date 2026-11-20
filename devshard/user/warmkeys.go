package user

import (
	"cmp"
	"log/slog"
	"slices"
	"time"

	"devshard/bridge"
	"devshard/signing"
	"devshard/state"
	"devshard/types"
)

// WithWarmKeyGrantSource gives the sequencer its view of the chain's warm-key
// log. Before each diff the session compares the group's live grants with the
// binding history and proposes a MsgWarmKeyBind for every live grant that has
// no live binding on its slot and a MsgWarmKeyRevoke for every live binding
// whose grant has ended or expired. Nil means the session never proposes
// key entries (tests, cold-key-only groups). See docs/warm-keys-v7.md.
func WithWarmKeyGrantSource(src bridge.WarmKeyGrantSource) SessionOption {
	return func(sess *Session) { sess.warmKeySource = src; installWarmHeightChecker(sess.sm, src) }
}

// SetWarmKeyGrantSource installs the grant source on a session built without
// it, such as one rebuilt by RecoverSession.
func (s *Session) SetWarmKeyGrantSource(src bridge.WarmKeyGrantSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.warmKeySource = src
	installWarmHeightChecker(s.sm, src)
}

// warmKeyProposalsLocked returns the bind and revoke entries the next diff
// should carry, for nonce. The entries name chain-log ids; the hosts confirm
// them against the same log, so the gateway proposes only what its source
// has. A source error for one cold key skips that key for this diff: the
// next compose tries again. Caller must hold s.mu.
func (s *Session) warmKeyProposalsLocked(nonce uint64) []*types.DevshardTx {
	return s.warmKeyProposalsAtLocked(nonce, nil)
}

func (s *Session) warmKeyProposalsAtLocked(nonce uint64, checkpoint *types.MsgMainnetHeight) []*types.DevshardTx {
	if s.warmKeySource == nil || s.sm == nil {
		return nil
	}
	history := s.sm.WarmKeyHistory()
	var out []*types.DevshardTx
	liveByCold := make(map[string][]bridge.WarmKeyEntry, len(s.addrToSlots))
	historicalByCold := make(map[string]map[uint64]bridge.WarmKeyEntry)
	failed := make(map[string]bool)
	for _, slot := range s.group {
		cold := slot.ValidatorAddress
		if failed[cold] {
			continue
		}
		live, ok := liveByCold[cold]
		if !ok {
			var err error
			if checkpoint == nil {
				live, err = s.warmKeySource.LiveWarmKeys(cold)
			} else {
				live, historicalByCold[cold], err = s.warmKeysAtCheckpoint(cold, checkpoint)
			}
			if err != nil {
				slog.Warn("warm keys: grant source unavailable, no proposals for this cold key",
					"escrow_id", s.escrowID, "cold", cold, "error", err)
				failed[cold] = true
				continue
			}
			liveByCold[cold] = live
		}
		// Revokes first, and the binds against the history they leave: a
		// grant renewed on chain (the old entry replaced by a new one for
		// the same key) is revoked and re-bound in one diff, so the key is
		// never without a binding between the two.
		scratch := signing.WarmKeyHistory{slot.SlotID: append([]signing.WarmKeyBinding(nil), history[slot.SlotID]...)}
		for _, tx := range s.warmKeyRevokesForSlotLocked(history, slot.SlotID, cold, live, historicalByCold[cold]) {
			r := tx.GetWarmKeyRevoke()
			if err := scratch.Revoke(r.SlotId, r.Address, nonce, r.GrantId); err != nil {
				continue
			}
			out = append(out, tx)
		}
		out = append(out, s.warmKeyBindsForSlotLocked(scratch, slot.SlotID, live, nonce)...)
	}
	return out
}

func (s *Session) warmKeysAtCheckpoint(cold string, checkpoint *types.MsgMainnetHeight) ([]bridge.WarmKeyEntry, map[uint64]bridge.WarmKeyEntry, error) {
	source, ok := s.warmKeySource.(bridge.WarmKeyLogHeightSource)
	if !ok {
		return nil, nil, bridge.ErrWarmKeyViewBehind
	}
	entries, err := source.WarmKeyLogAtHeight(cold, checkpoint.Height)
	if err != nil {
		return nil, nil, err
	}
	at := time.Unix(0, checkpoint.TimeUnixNano)
	var live []bridge.WarmKeyEntry
	for _, entry := range entries {
		if entry.LiveAtHeight(checkpoint.Height, at) {
			live = append(live, entry)
		}
	}
	slices.SortFunc(live, func(a, b bridge.WarmKeyEntry) int {
		return cmp.Compare(a.ID, b.ID)
	})
	return live, entries, nil
}

// warmKeyBindsForSlotLocked proposes a bind for every live grant the history
// would accept on slot at nonce: not already live there, and with a grant id
// above every earlier grant of the same key on the slot.
func (s *Session) warmKeyBindsForSlotLocked(history signing.WarmKeyHistory, slot uint32, live []bridge.WarmKeyEntry, nonce uint64) []*types.DevshardTx {
	var out []*types.DevshardTx
	for _, g := range live {
		trial := signing.WarmKeyHistory{slot: append([]signing.WarmKeyBinding(nil), history[slot]...)}
		if err := trial.Bind(slot, g.Grantee, nonce, g.ID); err != nil {
			continue
		}
		out = append(out, &types.DevshardTx{Tx: &types.DevshardTx_WarmKeyBind{WarmKeyBind: &types.MsgWarmKeyBind{
			SlotId: slot, Address: g.Grantee, GrantId: g.ID,
		}}})
	}
	return out
}

// warmKeyRevokesForSlotLocked proposes a revoke for every live binding on
// slot whose grant is no longer among cold's live grants: revoked, replaced
// or expired. Expiration is read from the entry, without a chain end event.
// A binding whose entry the source cannot see at all is
// left alone; the source is append-only, so a missing entry is a gap in the
// copy, not an end.
func (s *Session) warmKeyRevokesForSlotLocked(history signing.WarmKeyHistory, slot uint32, cold string, live []bridge.WarmKeyEntry, historical map[uint64]bridge.WarmKeyEntry) []*types.DevshardTx {
	liveIDs := make(map[uint64]bool, len(live))
	for _, g := range live {
		liveIDs[g.ID] = true
	}
	var out []*types.DevshardTx
	for _, b := range history[slot] {
		if b.RevokedAtNonce != 0 || liveIDs[b.GrantID] {
			continue
		}
		if historical != nil {
			if _, found := historical[b.GrantID]; !found {
				continue
			}
		} else {
			entry, found, err := s.warmKeySource.WarmKeyEntry(cold, b.GrantID)
			if err != nil || !found || entry.LiveAt(time.Now()) {
				continue
			}
		}
		out = append(out, &types.DevshardTx{Tx: &types.DevshardTx_WarmKeyRevoke{WarmKeyRevoke: &types.MsgWarmKeyRevoke{
			SlotId: slot, Address: b.Addr, GrantId: b.GrantID,
		}}})
	}
	return out
}

func installWarmHeightChecker(sm *state.StateMachine, src bridge.WarmKeyGrantSource) {
	if sm == nil || src == nil {
		return
	}
	sm.SetWarmKeyHeightChecker(func(cold, warm string, id uint64, height int64, t time.Time) (bool, error) {
		return bridge.CheckWarmKeyAtHeight(src, cold, warm, id, height, t)
	})
}
