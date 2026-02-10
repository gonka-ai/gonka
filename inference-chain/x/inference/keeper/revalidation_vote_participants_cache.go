package keeper

import "sync"

// revalidationVoteCacheKey tracks (blockHeight, inferenceId) for FIFO eviction by height.
type revalidationVoteCacheKey struct {
	Height      int64
	InferenceId string
}

// revalidationVoteEntry holds the invalidator (first vote addr) and capped weights per participant for one inference.
type revalidationVoteEntry struct {
	Invalidator string
	Weights     map[string]int64
}

// revalidationVoteParticipantsCache holds the list of participant addresses and their
// capped vote weights selected to vote on revalidation per inferenceId, plus the invalidator
// so ActiveInvalidations can be cleaned up when entries are evicted.
type revalidationVoteParticipantsCache struct {
	mu    sync.RWMutex
	byInf map[string]revalidationVoteEntry
	queue []revalidationVoteCacheKey
}

// EvictedRevalidationEntry is returned from ClearByHeight for each evicted inference so the keeper can remove ActiveInvalidations.
type EvictedRevalidationEntry struct {
	InferenceId string
	Invalidator string
}

func newRevalidationVoteParticipantsCache() *revalidationVoteParticipantsCache {
	return &revalidationVoteParticipantsCache{
		byInf: make(map[string]revalidationVoteEntry),
		queue: nil,
	}
}

// Add stores the invalidator and capped vote weights for the given block height and inference id.
func (c *revalidationVoteParticipantsCache) Add(blockHeight int64, inferenceId string, invalidator string, participants map[string]int64) {
	if inferenceId == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := revalidationVoteCacheKey{Height: blockHeight, InferenceId: inferenceId}
	m := make(map[string]int64, len(participants))
	for addr, w := range participants {
		if addr == "" {
			continue
		}
		m[addr] = w
	}
	c.byInf[inferenceId] = revalidationVoteEntry{Invalidator: invalidator, Weights: m}
	c.queue = append(c.queue, key)
}

// ClearByHeight removes all entries with height < targetHeight and returns evicted (inferenceId, invalidator) pairs
// so the keeper can remove the corresponding ActiveInvalidations entries.
func (c *revalidationVoteParticipantsCache) ClearByHeight(targetHeight int64) []EvictedRevalidationEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var evicted []EvictedRevalidationEntry
	i := 0
	for i < len(c.queue) && c.queue[i].Height < targetHeight {
		ref := c.queue[i]
		if e, ok := c.byInf[ref.InferenceId]; ok && e.Invalidator != "" {
			evicted = append(evicted, EvictedRevalidationEntry{InferenceId: ref.InferenceId, Invalidator: e.Invalidator})
		}
		delete(c.byInf, ref.InferenceId)
		i++
	}
	if i > 0 {
		c.queue = c.queue[i:]
	}
	return evicted
}

// Get returns the participant->cappedWeight map for the given inferenceId, or (nil, false).
func (c *revalidationVoteParticipantsCache) Get(blockHeight int64, inferenceId string) (map[string]int64, bool) {
	if inferenceId == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	e, ok := c.byInf[inferenceId]
	return e.Weights, ok
}

// Contains returns true if participantAddress is in the selected-to-vote list for (blockHeight, inferenceId).
func (c *revalidationVoteParticipantsCache) Contains(blockHeight int64, inferenceId string, participantAddress string) bool {
	m, ok := c.Get(blockHeight, inferenceId)
	if !ok || participantAddress == "" {
		return false
	}
	_, exists := m[participantAddress]
	return exists
}

// GetWeight returns the capped vote weight for (inferenceId, participantAddress), or (0, false).
func (c *revalidationVoteParticipantsCache) GetWeight(blockHeight int64, inferenceId string, participantAddress string) (int64, bool) {
	m, ok := c.Get(blockHeight, inferenceId)
	if !ok || participantAddress == "" {
		return 0, false
	}
	w, exists := m[participantAddress]
	return w, exists
}
