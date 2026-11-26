package api

import "devshard/cmd/gateway/chain"

type nonceReference struct {
	escrowID string
	nonce    uint64
}

func (e cachedResponse) servedReference() nonceReference {
	return nonceReference{escrowID: e.escrowID, nonce: e.nonce}
}

func (e cachedResponse) indexed() bool { return e.nonce != servedNonceNone }

type revokedSet struct {
	refs map[nonceReference]struct{}
	ring []nonceReference
	next int
}

func newRevokedSet() *revokedSet {
	return &revokedSet{refs: make(map[nonceReference]struct{})}
}

func (s *revokedSet) has(reference nonceReference) bool {
	_, revoked := s.refs[reference]
	return revoked
}

func (s *revokedSet) add(reference nonceReference) {
	if s.has(reference) {
		return
	}
	if len(s.ring) < revokedLimit {
		s.ring = append(s.ring, reference)
	} else {
		delete(s.refs, s.ring[s.next])
		s.ring[s.next] = reference
		s.next = (s.next + 1) % revokedLimit
	}
	s.refs[reference] = struct{}{}
}

func (c *responseCache) currentGeneration() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.generation
}

func (c *responseCache) invalidate(reference nonceReference) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if key, stored := c.byNonce[reference]; stored {
		c.dropLocked(key)
		return
	}
	c.revoked.add(reference)
}

func (c *responseCache) invalidateParticipant(participant string) {
	c.removeWhere(func(entry cachedResponse) bool { return entry.participant == participant })
}

func (c *responseCache) observeEpoch(epoch uint64) {
	if c == nil || epoch == chain.EpochIndexUnknown {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if epoch <= c.epoch {
		return
	}
	previous := c.epoch
	c.epoch = epoch
	if previous != chain.EpochIndexUnknown && previous != epoch {
		c.removeWhereLocked(func(cachedResponse) bool { return true })
	}
}

func (c *responseCache) removeWhere(matches func(cachedResponse) bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.removeWhereLocked(matches)
}

func (c *responseCache) removeWhereLocked(matches func(cachedResponse) bool) {
	c.generation++
	for key, entry := range c.entries {
		if matches(entry) {
			c.dropLocked(key)
		}
	}
}

// InvalidateCachedReply removes the cached reply the nonce produced, or remembers the verdict for a reply not yet stored.
func (s *Server) InvalidateCachedReply(escrowID string, nonce uint64) {
	s.cache.invalidate(nonceReference{escrowID: escrowID, nonce: nonce})
}

// ObservePhase clears the cache when the chain's effective epoch changes.
func (s *Server) ObservePhase(snapshot chain.PhaseSnapshot) {
	s.cache.observeEpoch(snapshot.EffectiveEpochIndex)
}
