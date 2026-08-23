package store

import inferencetypes "github.com/productscience/inference/x/inference/types"

const defaultTestenvEpochLength = int64(400)

// DefaultTestenvEpochLength is the mock-chain epoch length when Params.epoch_params is unset.
const DefaultTestenvEpochLength = defaultTestenvEpochLength

// EpochAdvancePlan describes a catch-up epoch transition to the next PoC start block.
type EpochAdvancePlan struct {
	FromHeight int64
	ToHeight   int64
	NewEpoch   inferencetypes.Epoch
	NewNextPoc int64
}

// GetNextPocStartBlockHeight returns the upcoming epoch PoC anchor height.
func (s *Store) GetNextPocStartBlockHeight() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.nextPocStartBlockHeightLocked()
}

// SetNextPocStartBlockHeight sets the upcoming PoC anchor (tests).
func (s *Store) SetNextPocStartBlockHeight(height int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.NextPocStartBlockHeight = height
}

func (s *Store) epochLengthLocked() int64 {
	if ep := s.Params.EpochParams; ep != nil && ep.EpochLength > 0 {
		return ep.EpochLength
	}
	return defaultTestenvEpochLength
}

func (s *Store) nextPocStartBlockHeightLocked() int64 {
	if s.NextPocStartBlockHeight > 0 {
		return s.NextPocStartBlockHeight
	}
	length := s.epochLengthLocked()
	next := s.Epoch.PocStartBlockHeight + length
	if next <= s.BlockHeight {
		next = s.BlockHeight + length
	}
	return next
}

func (s *Store) initNextPocStartLocked() {
	s.NextPocStartBlockHeight = s.nextPocStartBlockHeightLocked()
}

// InitNextPocStart recomputes NextPocStartBlockHeight from epoch params (seed loader).
func (s *Store) InitNextPocStart() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initNextPocStartLocked()
}

// PlanEpochAdvance computes the catch-up target for POST /testenv/epoch advance.
func (s *Store) PlanEpochAdvance() EpochAdvancePlan {
	s.mu.Lock()
	defer s.mu.Unlock()

	from := s.BlockHeight
	to := s.nextPocStartBlockHeightLocked()
	if to <= from {
		to = from + s.epochLengthLocked()
	}

	idx := s.Epoch.Index
	if idx == 0 {
		idx = 1
	}
	idx++

	length := s.epochLengthLocked()
	return EpochAdvancePlan{
		FromHeight: from,
		ToHeight:   to,
		NewEpoch: inferencetypes.Epoch{
			Index:               idx,
			PocStartBlockHeight: to,
		},
		NewNextPoc: to + length,
	}
}

// ApplyEpochAdvance commits epoch metadata after block heights were published up to ToHeight.
func (s *Store) ApplyEpochAdvance(plan EpochAdvancePlan) inferencetypes.Epoch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if plan.ToHeight > 0 {
		s.BlockHeight = plan.ToHeight
	}
	s.Epoch = plan.NewEpoch
	s.NextPocStartBlockHeight = plan.NewNextPoc
	s.publishParamsAtLocked(plan.ToHeight)
	return s.Epoch
}

// AdvanceEpochWithoutCatchUp increments epoch at the current tip without block simulation
// (store-only tests when no CometBFT publisher is wired).
func (s *Store) AdvanceEpochWithoutCatchUp() inferencetypes.Epoch {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Epoch.Index == 0 {
		s.Epoch.Index = 1
	}
	s.Epoch.Index++
	s.Epoch.PocStartBlockHeight = s.BlockHeight
	if s.Epoch.PocStartBlockHeight <= 0 {
		s.Epoch.PocStartBlockHeight = 1
	}
	s.NextPocStartBlockHeight = s.Epoch.PocStartBlockHeight + s.epochLengthLocked()
	s.publishParamsAtLocked(s.BlockHeight)
	return s.Epoch
}
