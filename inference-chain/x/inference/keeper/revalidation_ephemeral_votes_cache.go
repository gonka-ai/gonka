package keeper

import (
	"sync"
)

// ephemeralRevalidationVoteEntry is a single vote in the ephemeral cache (when storeRevalidationVotes is false).
type ephemeralRevalidationVoteEntry struct {
	Pass       bool
	Participant string
	Weight     int64
}

// ephemeralRevalidationSession holds total eligible weight and votes for one inference in the ephemeral cache.
type ephemeralRevalidationSession struct {
	BlockHeight         int64
	TotalEligibleWeight int64
	Votes               []ephemeralRevalidationVoteEntry
}

type ephemeralRevalidationCacheKey struct {
	Height      int64
	InferenceId string
}

// ephemeralRevalidationVoteCache holds votes per inference when storeRevalidationVotes is false.
// Evicted after NormalizedParticipantsCacheBlocks (300) blocks.
type ephemeralRevalidationVoteCache struct {
	mu       sync.RWMutex
	byInf    map[string]*ephemeralRevalidationSession
	queue    []ephemeralRevalidationCacheKey
}

func newEphemeralRevalidationVoteCache() *ephemeralRevalidationVoteCache {
	return &ephemeralRevalidationVoteCache{
		byInf: make(map[string]*ephemeralRevalidationSession),
		queue: nil,
	}
}

func (c *ephemeralRevalidationVoteCache) ClearByHeight(targetHeight int64) {
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

// StartSession starts a vote session for the inference (or no-op if store is used; this is for ephemeral only).
func (c *ephemeralRevalidationVoteCache) StartSession(blockHeight int64, inferenceId string, totalEligibleWeight int64, invalidator string, invalidatorWeight int64) {
	if inferenceId == "" || totalEligibleWeight <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byInf[inferenceId] = &ephemeralRevalidationSession{
		BlockHeight:         blockHeight,
		TotalEligibleWeight: totalEligibleWeight,
		Votes:               []ephemeralRevalidationVoteEntry{{Pass: false, Participant: invalidator, Weight: invalidatorWeight}},
	}
	c.queue = append(c.queue, ephemeralRevalidationCacheKey{Height: blockHeight, InferenceId: inferenceId})
}

// AddVote adds a vote; returns passTotal, noPassTotal, thresholdReached, invalidateWon. If participant already voted, idempotent.
func (c *ephemeralRevalidationVoteCache) AddVote(inferenceId string, pass bool, participant string, weight int64) (passTotal, noPassTotal int64, thresholdReached bool, invalidateWon bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ses, ok := c.byInf[inferenceId]
	if !ok {
		return 0, 0, false, false
	}
	for _, v := range ses.Votes {
		if v.Participant == participant {
			// already voted
			var p, n int64
			for _, e := range ses.Votes {
				if e.Pass {
					p += e.Weight
				} else {
					n += e.Weight
				}
			}
			half := ses.TotalEligibleWeight / 2
			return p, n, p >= half || n >= half, n >= half
		}
	}
	ses.Votes = append(ses.Votes, ephemeralRevalidationVoteEntry{Pass: pass, Participant: participant, Weight: weight})
	var passSum, noPassSum int64
	for _, v := range ses.Votes {
		if v.Pass {
			passSum += v.Weight
		} else {
			noPassSum += v.Weight
		}
	}
	half := ses.TotalEligibleWeight / 2
	thresholdReached = passSum >= half || noPassSum >= half
	invalidateWon = noPassSum >= half
	return passSum, noPassSum, thresholdReached, invalidateWon
}

// HasSession returns true if there is an ephemeral session for this inference.
func (c *ephemeralRevalidationVoteCache) HasSession(inferenceId string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.byInf[inferenceId]
	return ok
}

// GetInvalidator returns the invalidator (first voter with pass=false) for the session, or "" if none.
func (c *ephemeralRevalidationVoteCache) GetInvalidator(inferenceId string) string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	ses, ok := c.byInf[inferenceId]
	if !ok || len(ses.Votes) == 0 {
		return ""
	}
	for _, v := range ses.Votes {
		if !v.Pass {
			return v.Participant
		}
	}
	return ""
}
