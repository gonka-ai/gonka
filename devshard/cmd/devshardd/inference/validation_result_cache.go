package inference

import (
	"sync"
	"time"

	devshardpkg "devshard"
)

const validationResultCacheTTL = 30 * time.Minute

type validationResultCacheKey struct {
	escrowID    string
	inferenceID uint64
}

type validationResultCacheEntry struct {
	valid   bool
	reason  string
	expires time.Time
}

// validationResultCache stores local Validate verdicts so a host that already
// completed payload/ML work can publish a Phase-B MsgValidationVote without
// re-running the job. Entries expire after validationResultCacheTTL.
type validationResultCache struct {
	mu sync.Mutex
	m  map[validationResultCacheKey]validationResultCacheEntry
}

func (c *validationResultCache) get(escrowID string, inferenceID uint64) (valid bool, reason string, ok bool) {
	if c == nil {
		return false, "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		return false, "", false
	}
	key := validationResultCacheKey{escrowID: escrowID, inferenceID: inferenceID}
	ent, found := c.m[key]
	if !found {
		return false, "", false
	}
	if time.Now().After(ent.expires) {
		delete(c.m, key)
		return false, "", false
	}
	return ent.valid, ent.reason, true
}

func (c *validationResultCache) put(escrowID string, inferenceID uint64, result *devshardpkg.ValidateResult) {
	if c == nil || result == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = make(map[validationResultCacheKey]validationResultCacheEntry)
	}
	c.m[validationResultCacheKey{escrowID: escrowID, inferenceID: inferenceID}] = validationResultCacheEntry{
		valid:   result.Valid,
		reason:  result.Reason,
		expires: time.Now().Add(validationResultCacheTTL),
	}
}
