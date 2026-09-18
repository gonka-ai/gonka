package probe

import (
	"sync"
	"time"
)

type capState struct {
	demoted  bool
	demotedAt time.Time
}

type capabilityCache struct {
	mu sync.Mutex
	m  map[string]capState
}

func newCapabilityCache() *capabilityCache {
	return &capabilityCache{m: make(map[string]capState)}
}

func (c *capabilityCache) shouldTryClock(key string, ttl time.Duration, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.m[key]
	if !ok || !st.demoted {
		return true
	}
	if ttl > 0 && !st.demotedAt.IsZero() && now.Sub(st.demotedAt) >= ttl {
		// TTL expired: allow one rediscovery attempt (stay demoted until success).
		return true
	}
	return false
}

func (c *capabilityCache) demote(key string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key] = capState{demoted: true, demotedAt: now}
}

func (c *capabilityCache) markClock(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

func (c *capabilityCache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.m, key)
}

func (c *capabilityCache) isDemoted(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.m[key]
	return ok && st.demoted
}
