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
	ent := c.m[key]
	ent.expires = time.Now().Add(-time.Second)
	c.m[key] = ent
	c.mu.Unlock()

	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok, "expired entry must miss")

	c.mu.Lock()
	_, stillPresent := c.m[key]
	c.mu.Unlock()
	require.False(t, stillPresent, "expired entry must be deleted on get")
}

func TestValidationResultCache_NilSafe(t *testing.T) {
	t.Parallel()
	var c *validationResultCache
	_, _, ok := c.get("escrow-1", 1)
	require.False(t, ok)
	c.put("escrow-1", 1, &devshardpkg.ValidateResult{Valid: true})
}
