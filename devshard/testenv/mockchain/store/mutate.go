package store

import inferencetypes "github.com/productscience/inference/x/inference/types"

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

// SetGrantees replaces warm-key grantees for a validator granter + message type.
func (s *Store) SetGrantees(granter, messageTypeURL string, grantees []inferencetypes.Grantee) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := GranteeKey{GranterAddress: granter, MessageTypeURL: messageTypeURL}
	if len(grantees) == 0 {
		delete(s.Grantees, key)
		return
	}
	s.Grantees[key] = append([]inferencetypes.Grantee(nil), grantees...)
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
