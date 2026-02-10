package keeper

import (
	"sync"
)

// revalidationVoteCacheKey tracks (blockHeight, inferenceId) for FIFO eviction by height.
type revalidationVoteCacheKey struct {
	Height     int64
	InferenceId string
}

// revalidationVoteParticipantsCache holds the list of participant addresses selected to vote
// on revalidation per inferenceId. Entries are evicted by height using the FIFO queue of
// (blockHeight, inferenceId), but the primary lookup key is inferenceId so we don't need
// to know the original event height at eligibility-check time.
type revalidationVoteParticipantsCache struct {
	mu     sync.RWMutex
	byInf  map[string][]string
	queue  []revalidationVoteCacheKey
}

func newRevalidationVoteParticipantsCache() *revalidationVoteParticipantsCache {
	return &revalidationVoteParticipantsCache{
		byInf: make(map[string][]string),
		queue: nil,
	}
}

// Add stores the list of participant addresses selected to vote for the given block height and inference id.
func (c *revalidationVoteParticipantsCache) Add(blockHeight int64, inferenceId string, participants []string) {
	if inferenceId == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := revalidationVoteCacheKey{Height: blockHeight, InferenceId: inferenceId}
	// Copy slice so caller can't mutate cache
	list := make([]string, len(participants))
	copy(list, participants)
	c.byInf[inferenceId] = list
	c.queue = append(c.queue, key)
}

// ClearByHeight removes all entries with height < targetHeight.
func (c *revalidationVoteParticipantsCache) ClearByHeight(targetHeight int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i := 0
	for i < len(c.queue) && c.queue[i].Height < targetHeight {
		ref := c.queue[i]
		delete(c.byInf, ref.InferenceId)
		i++
	}
	if i > 0 {
		c.queue = c.queue[i:]
	}
}

// Get returns the list of participant addresses for the given inferenceId, or (nil, false).
func (c *revalidationVoteParticipantsCache) Get(blockHeight int64, inferenceId string) ([]string, bool) {
	if inferenceId == "" {
		return nil, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	list, ok := c.byInf[inferenceId]
	return list, ok
}

// Contains returns true if participantAddress is in the selected-to-vote list for (blockHeight, inferenceId).
func (c *revalidationVoteParticipantsCache) Contains(blockHeight int64, inferenceId string, participantAddress string) bool {
	list, ok := c.Get(blockHeight, inferenceId)
	if !ok || participantAddress == "" {
		return false
	}
	for _, addr := range list {
		if addr == participantAddress {
			return true
		}
	}
	return false
}
