package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func cacheTestEntry(escrow, body string, expires time.Time) cachedChatResponse {
	return cachedChatResponse{
		EscrowID:  escrow,
		Body:      []byte(body),
		ExpiresAt: expires,
	}
}

// cacheLen reports the entry count without depending on any exported accessor,
// so this bug-fix test stays self-contained.
func cacheLen(c *chatResponseCache) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

func TestChatResponseCache_SetGetRoundTrip(t *testing.T) {
	c := newChatResponseCache(time.Hour)
	now := time.Unix(1000, 0)
	key := chatCacheKey("model-a", []byte("body-a"))

	c.Set(key, cacheTestEntry("escrow-1", "hello", time.Time{}), now)

	got, ok := c.Get(key, now)
	require.True(t, ok)
	require.Equal(t, "escrow-1", got.EscrowID)
	require.Equal(t, "hello", string(got.Body))

	// The returned body must be a copy: mutating it cannot corrupt the cache.
	got.Body[0] = 'H'
	again, ok := c.Get(key, now)
	require.True(t, ok)
	require.Equal(t, "hello", string(again.Body))
}

func TestChatResponseCache_ExpiredEntryNotServed(t *testing.T) {
	c := newChatResponseCache(time.Hour)
	now := time.Unix(1000, 0)
	key := chatCacheKey("m", []byte("b"))

	c.Set(key, cacheTestEntry("e", "x", now.Add(time.Minute)), now)

	_, ok := c.Get(key, now.Add(2*time.Minute))
	require.False(t, ok)
	require.Equal(t, 0, cacheLen(c), "expired entry should be dropped on access")
}

// TestChatResponseCache_BoundsEntryCount is the regression test for the
// unbounded-growth bug: streaming unique request bodies (the normal chat
// workload) must never let the cache exceed its configured maximum.
func TestChatResponseCache_BoundsEntryCount(t *testing.T) {
	c := newChatResponseCache(time.Hour)
	c.maxEntries = 8
	base := time.Unix(1000, 0)

	const inserted = 500
	for i := 0; i < inserted; i++ {
		now := base.Add(time.Duration(i) * time.Millisecond)
		key := chatCacheKey("m", []byte(fmt.Sprintf("body-%d", i)))
		c.Set(key, cacheTestEntry("e", fmt.Sprintf("resp-%d", i), time.Time{}), now)
		require.LessOrEqualf(t, cacheLen(c), c.maxEntries,
			"cache exceeded max entries (%d) after %d inserts", c.maxEntries, i+1)
	}

	// The most recent insert must survive: eviction drops the oldest, not the
	// newest, so a just-cached response is still available for a fast retry.
	lastNow := base.Add(time.Duration(inserted-1) * time.Millisecond)
	lastKey := chatCacheKey("m", []byte(fmt.Sprintf("body-%d", inserted-1)))
	got, ok := c.Get(lastKey, lastNow)
	require.True(t, ok)
	require.Equal(t, fmt.Sprintf("resp-%d", inserted-1), string(got.Body))
}

// TestChatResponseCache_EvictionReclaimsExpiredBeforeLive verifies that
// eviction frees expired entries before touching live ones, so a burst of
// stale entries does not evict a fresh, still-useful response.
func TestChatResponseCache_EvictionReclaimsExpiredBeforeLive(t *testing.T) {
	c := newChatResponseCache(time.Hour)
	c.maxEntries = 4
	base := time.Unix(1000, 0)

	for i := 0; i < 4; i++ {
		key := chatCacheKey("m", []byte(fmt.Sprintf("old-%d", i)))
		c.Set(key, cacheTestEntry("e", "old", base.Add(10*time.Second)), base)
	}
	require.Equal(t, 4, cacheLen(c))

	// Insert a fresh entry after the old ones have expired. Eviction should
	// reclaim the expired entries instead of dropping the newcomer.
	later := base.Add(20 * time.Second)
	freshKey := chatCacheKey("m", []byte("fresh"))
	c.Set(freshKey, cacheTestEntry("e", "fresh", time.Time{}), later)

	require.LessOrEqual(t, cacheLen(c), c.maxEntries)
	got, ok := c.Get(freshKey, later)
	require.True(t, ok)
	require.Equal(t, "fresh", string(got.Body))
}

// TestChatResponseCache_OverwriteDoesNotEvict confirms that re-Setting an
// existing key updates in place without triggering eviction (the entry count
// does not grow, so a full cache is not needlessly churned).
func TestChatResponseCache_OverwriteDoesNotEvict(t *testing.T) {
	c := newChatResponseCache(time.Hour)
	c.maxEntries = 2
	now := time.Unix(1000, 0)

	k1 := chatCacheKey("m", []byte("one"))
	k2 := chatCacheKey("m", []byte("two"))
	c.Set(k1, cacheTestEntry("e", "one", time.Time{}), now)
	c.Set(k2, cacheTestEntry("e", "two", time.Time{}), now)
	require.Equal(t, 2, cacheLen(c))

	// Overwriting k1 keeps the cache at capacity and must not evict k2.
	c.Set(k1, cacheTestEntry("e", "one-v2", time.Time{}), now.Add(time.Second))
	require.Equal(t, 2, cacheLen(c))

	got, ok := c.Get(k2, now)
	require.True(t, ok)
	require.Equal(t, "two", string(got.Body))

	got, ok = c.Get(k1, now)
	require.True(t, ok)
	require.Equal(t, "one-v2", string(got.Body))
}
