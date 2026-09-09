package store

// ParamsBlockHeight returns the chain height at which runtime params were last published.
func (s *Store) GetParamsBlockHeight() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ParamsBlockHeight
}

// SetParamsBlockHeight sets the published params revision height (tests).
func (s *Store) SetParamsBlockHeight(height int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ParamsBlockHeight = height
}

func (s *Store) publishParamsAtLocked(height int64) {
	if height <= 0 {
		height = s.BlockHeight
		if height <= 0 {
			height = 1
		}
	}
	if height <= s.ParamsBlockHeight {
		s.ParamsBlockHeight++
		return
	}
	s.ParamsBlockHeight = height
}
