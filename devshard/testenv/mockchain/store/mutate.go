package store

import (
	"sort"

	inferencetypes "github.com/productscience/inference/x/inference/types"
)

// PatchDevshardEscrowParams mutates DevshardEscrowParams and stamps params_block_height
// at the current chain tip (mock-chain analogue of dapi publishing on param change).
func (s *Store) PatchDevshardEscrowParams(patch func(*inferencetypes.DevshardEscrowParams)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Params.DevshardEscrowParams == nil {
		s.Params.DevshardEscrowParams = &inferencetypes.DevshardEscrowParams{}
	}
	patch(s.Params.DevshardEscrowParams)
	s.publishParamsAtLocked(s.BlockHeight)
}

// SetGrantees replaces warm-key grantees for a validator granter + message
// type, and keeps the warm-key log in step: a grantee that was not live gets
// a new entry at the current height, a grantee that is dropped has its live
// entry ended as REVOKED. The legacy and current markers share one log, as
// GranteesByMessageType aliases them.
func (s *Store) SetGrantees(granter, messageTypeURL string, grantees []inferencetypes.Grantee) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := GranteeKey{GranterAddress: granter, MessageTypeURL: messageTypeURL}
	if len(grantees) == 0 {
		delete(s.Grantees, key)
	} else {
		s.Grantees[key] = append([]inferencetypes.Grantee(nil), grantees...)
	}
	s.syncWarmKeyLogLocked(granter)
}

// syncWarmKeyLogLocked makes the warm-key log of granter agree with the
// grantees of every message type: one live entry per live grantee.
func (s *Store) syncWarmKeyLogLocked(granter string) {
	live := make(map[string]bool)
	for key, grantees := range s.Grantees {
		if key.GranterAddress != granter {
			continue
		}
		for _, g := range grantees {
			live[g.Address] = true
		}
	}
	if s.WarmKeyLog == nil {
		s.WarmKeyLog = make(map[string][]*inferencetypes.WarmKeyLogEntry)
	}
	logged := make(map[string]bool)
	for _, e := range s.WarmKeyLog[granter] {
		if e.RevokedAtHeight != 0 {
			continue
		}
		if live[e.Grantee] {
			logged[e.Grantee] = true
			continue
		}
		e.RevokedAtHeight = s.BlockHeight
		e.End = inferencetypes.WarmKeyLogEnd_WARM_KEY_LOG_END_REVOKED
	}
	// New grantees in a fixed order so ids are deterministic across runs.
	var fresh []string
	for addr := range live {
		if !logged[addr] {
			fresh = append(fresh, addr)
		}
	}
	sort.Strings(fresh)
	for _, addr := range fresh {
		s.WarmKeyLog[granter] = append(s.WarmKeyLog[granter], &inferencetypes.WarmKeyLogEntry{
			Granter:         granter,
			Id:              uint64(len(s.WarmKeyLog[granter]) + 1),
			Grantee:         addr,
			GrantedAtHeight: s.BlockHeight,
		})
	}
}

// SetEpoch replaces the current epoch. When index or poc start changes, params are
// republished at the current chain tip.
func (s *Store) SetEpoch(epoch inferencetypes.Epoch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := s.Epoch.Index != epoch.Index || s.Epoch.PocStartBlockHeight != epoch.PocStartBlockHeight
	s.Epoch = epoch
	if changed {
		s.publishParamsAtLocked(s.BlockHeight)
	}
}
