package store

import inferencetypes "github.com/productscience/inference/x/inference/types"

// GetOrCreateAccount returns auth metadata for address, allocating defaults when missing.
func (s *Store) GetOrCreateAccount(address string) Account {
	s.mu.Lock()
	defer s.mu.Unlock()
	if acc, ok := s.Accounts[address]; ok && acc != nil {
		return *acc
	}
	acc := &Account{
		AccountNumber: uint64(len(s.Accounts)),
		Sequence:      0,
	}
	s.Accounts[address] = acc
	return *acc
}

// IncrementSequence bumps the account sequence after a successful broadcast.
func (s *Store) IncrementSequence(address string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	acc, ok := s.Accounts[address]
	if !ok || acc == nil {
		acc = &Account{AccountNumber: uint64(len(s.Accounts)), Sequence: 0}
		s.Accounts[address] = acc
	}
	acc.Sequence++
}

// AllocateEscrowID returns the next escrow id and advances the counter.
func (s *Store) AllocateEscrowID() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.nextEscrowID
	if id == 0 {
		id = 1
	}
	s.nextEscrowID = id + 1
	return id
}

func (s *Store) reinitEscrowCounterLocked() {
	var max uint64
	for id := range s.Escrows {
		if id > max {
			max = id
		}
	}
	if max == 0 {
		s.nextEscrowID = 1
		return
	}
	s.nextEscrowID = max + 1
}

func cloneAccountMap(in map[string]*Account) map[string]*Account {
	out := make(map[string]*Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		c := *v
		out[k] = &c
	}
	return out
}

// TemplateEscrow returns a seeded escrow used to fill defaults on REST create.
func (s *Store) TemplateEscrow() *inferencetypes.DevshardEscrow {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.Escrows {
		if e != nil {
			c := *e
			if e.Slots != nil {
				c.Slots = append([]string(nil), e.Slots...)
			}
			return &c
		}
	}
	return nil
}

// MarkEscrowSettled marks an escrow settled in the store.
func (s *Store) MarkEscrowSettled(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.Escrows[id]
	if !ok || e == nil {
		return false
	}
	e.Settled = true
	return true
}
