package inference

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	devshardpkg "devshard"
)

func TestValidationResultCache_GetPut(t *testing.T) {
	t.Parallel()
	var c validationResultCache

	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok)

	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: false, Reason: "payload unavailable"})
	valid, reason, ok := c.get("escrow-1", 1)
	require.True(t, ok)
	require.False(t, valid)
	require.Equal(t, "payload unavailable", reason)

	_, _, ok = c.get("escrow-1", 2)
	require.False(t, ok, "different inference must miss")
	_, _, ok = c.get("escrow-2", 1)
	require.False(t, ok, "different escrow must miss")
}

func TestValidationResultCache_Expires(t *testing.T) {
	t.Parallel()
	var c validationResultCache
	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: true, Reason: "ok"})

	c.mu.Lock()
	key := validationResultCacheKey{escrowID: "escrow-1", inferenceID: 1}
	ent := c.cur[key]
	ent.expires = time.Now().Add(-time.Second)
	c.cur[key] = ent
	c.mu.Unlock()

	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok, "expired entry must miss")

	c.mu.Lock()
	_, stillPresent := c.cur[key]
	c.mu.Unlock()
	require.False(t, stillPresent, "expired entry must be deleted on get")
}

func TestValidationResultCache_RotationKeepsPreviousWindow(t *testing.T) {
	t.Parallel()
	var c validationResultCache
	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: true, Reason: "old"})
	c.put("escrow-1", 2, &devshardpkg.ValidateResult{Valid: false, Reason: "also-old"})

	ageValidationCacheWindow(&c, validationResultCacheTTL)
	c.put("escrow-1", 3, &devshardpkg.ValidateResult{Valid: true, Reason: "new"})

	valid, reason, ok := c.get("escrow-1", 1)
	require.True(t, ok, "previous window must stay readable")
	require.True(t, valid)
	require.Equal(t, "old", reason)

	c.mu.Lock()
	oldKey := validationResultCacheKey{escrowID: "escrow-1", inferenceID: 1}
	unreadKey := validationResultCacheKey{escrowID: "escrow-1", inferenceID: 2}
	newKey := validationResultCacheKey{escrowID: "escrow-1", inferenceID: 3}
	_, oldInCur := c.cur[oldKey]
	_, oldInPrev := c.prev[oldKey]
	_, unreadInPrev := c.prev[unreadKey]
	_, newInCur := c.cur[newKey]
	c.mu.Unlock()
	require.False(t, oldInCur)
	require.True(t, oldInPrev)
	require.True(t, unreadInPrev, "unread key moves with its generation")
	require.True(t, newInCur)

	// The next window drops the generation that was never read again.
	ageValidationCacheWindow(&c, validationResultCacheTTL)
	c.put("escrow-1", 4, &devshardpkg.ValidateResult{Valid: true, Reason: "newer"})

	_, _, ok = c.get("escrow-1", 1)
	require.False(t, ok, "entry from two windows ago must be gone")
	_, _, ok = c.get("escrow-1", 2)
	require.False(t, ok, "unread entry must leave with its generation")
	_, _, ok = c.get("escrow-1", 3)
	require.True(t, ok, "previous window stays readable")
	_, _, ok = c.get("escrow-1", 4)
	require.True(t, ok)
}

func TestValidationResultCache_RotationDropsBothAfterLongGap(t *testing.T) {
	t.Parallel()
	var c validationResultCache
	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: true, Reason: "stale"})

	ageValidationCacheWindow(&c, 2*validationResultCacheTTL)
	c.put("escrow-1", 2, &devshardpkg.ValidateResult{Valid: false, Reason: "fresh"})

	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok, "a gap of two TTLs drops both windows")
	valid, reason, ok := c.get("escrow-1", 2)
	require.True(t, ok)
	require.False(t, valid)
	require.Equal(t, "fresh", reason)

	c.mu.Lock()
	require.Nil(t, c.prev)
	c.mu.Unlock()
}

func TestValidationResultCache_NilSafe(t *testing.T) {
	t.Parallel()
	var c *validationResultCache
	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok)
	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: true})
}

func ageValidationCacheWindow(c *validationResultCache, age time.Duration) {
	c.mu.Lock()
	c.curStart = time.Now().Add(-age)
	c.mu.Unlock()
}
