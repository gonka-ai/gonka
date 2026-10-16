package signing

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Bind a warm key to a slot at nonce 10, revoke it at nonce 50.
//  2. Assert the binding covers nonces 10 to 49 and nothing outside.
func TestWarmKeyBindingCoversItsNonceRange(t *testing.T) {
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1warm", 10, 10, 1000))
	require.NoError(t, h.Revoke(0, "gonka1warm", 50, 10, 5000))

	require.Empty(t, h.ActiveAt(0, 9))
	require.Equal(t, []string{"gonka1warm"}, h.ActiveAt(0, 10))
	require.Equal(t, []string{"gonka1warm"}, h.ActiveAt(0, 49))
	require.Empty(t, h.ActiveAt(0, 50))
	require.Empty(t, h.Live(0))
}

// Test flow:
//  1. Bind an old key at nonce 10 and a new key at nonce 30, then revoke the old key at nonce 40.
//  2. Assert both keys are accepted inside the rotation window, only the new one after it, and a signature from
//     before the revoke still verifies when replayed.
func TestWarmKeyRotationKeepsTheOldKeyUntilItsRevoke(t *testing.T) {
	slots := map[uint32]string{0: "gonka1cold"}
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1old", 10, 10, 1000))
	require.NoError(t, h.Bind(0, "gonka1new", 30, 12, 1200))
	require.NoError(t, h.Revoke(0, "gonka1old", 40, 10, 4000))

	require.True(t, h.Allows(slots, 0, "gonka1old", 20))
	require.False(t, h.Allows(slots, 0, "gonka1new", 20))
	require.True(t, h.Allows(slots, 0, "gonka1old", 35))
	require.True(t, h.Allows(slots, 0, "gonka1new", 35))
	require.False(t, h.Allows(slots, 0, "gonka1old", 40))
	require.True(t, h.Allows(slots, 0, "gonka1new", 40))
	require.True(t, h.Allows(slots, 0, "gonka1old", 39), "a signature from before the revoke replays as valid")
	require.Equal(t, []string{"gonka1new"}, h.Live(0))
}

// Test flow:
//  1. Bind a warm key on slot 0 of a validator that also owns slot 1; slot 2 belongs to another validator.
//  2. Assert the key acts for slot 1 while slot 1 has no binding of its own, not for slot 2, and not for slot 1
//     once slot 1 has its own binding.
func TestWarmKeyHistoryAllowsASiblingSlotOnlyWhileUnbound(t *testing.T) {
	slots := map[uint32]string{0: "gonka1cold", 1: "gonka1cold", 2: "gonka1other"}
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1warm", 10, 10, 1000))

	require.True(t, h.Allows(slots, 1, "gonka1warm", 20), "sibling slot of the same validator")
	require.False(t, h.Allows(slots, 2, "gonka1warm", 20), "another validator's slot")
	require.False(t, h.Allows(slots, 1, "gonka1warm", 5), "before the sibling binding")

	require.NoError(t, h.Bind(1, "gonka1otherwarm", 30, 11, 1100))
	require.False(t, h.Allows(slots, 1, "gonka1warm", 30), "a bound slot is exclusive")
	require.True(t, h.Allows(slots, 1, "gonka1otherwarm", 30))
	require.True(t, h.Allows(slots, 1, "gonka1cold", 30))
}

// Test flow:
//  1. Bind the same warm key on slots 0 and 1 of one validator, then revoke it on slot 0 only.
//  2. Assert the key is refused on slot 0 from the revoke nonce on, although slot 1 still has it live: the sibling
//     rule must not undo a revoke on the slot it names. Slot 1 keeps accepting it.
func TestWarmKeyRevokeOnASlotIsNotUndoneByASibling(t *testing.T) {
	slots := map[uint32]string{0: "gonka1cold", 1: "gonka1cold"}
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1warm", 10, 10, 1000))
	require.NoError(t, h.Bind(1, "gonka1warm", 10, 10, 1000))
	require.NoError(t, h.Revoke(0, "gonka1warm", 20, 10, 2000))

	require.True(t, h.Allows(slots, 0, "gonka1warm", 19))
	require.False(t, h.Allows(slots, 0, "gonka1warm", 20))
	require.False(t, h.Allows(slots, 0, "gonka1warm", 99))
	require.True(t, h.Allows(slots, 1, "gonka1warm", 99))
}

// Test flow:
//  1. Try the refused operations: an empty key, a key that is already live, a revoke of a key that is not bound or
//     not live, and an entry at a nonce before the slot's latest change.
//  2. Assert each is refused with its own error and the history is unchanged.
func TestWarmKeyHistoryRefusesInvalidEntries(t *testing.T) {
	h := WarmKeyHistory{}
	require.ErrorIs(t, h.Bind(0, "", 10, 10, 1000), ErrWarmKeyEmpty)
	require.NoError(t, h.Bind(0, "gonka1warm", 10, 10, 1000))
	require.ErrorIs(t, h.Bind(0, "gonka1warm", 20, 10, 1000), ErrWarmKeyLive)
	require.ErrorIs(t, h.Revoke(0, "gonka1stranger", 20, 10, 2000), ErrWarmKeyNotFound)
	require.ErrorIs(t, h.Bind(0, "gonka1late", 5, 10, 1000), ErrWarmKeyNonce)
	require.NoError(t, h.Revoke(0, "gonka1warm", 20, 10, 2000))
	require.ErrorIs(t, h.Revoke(0, "gonka1warm", 25, 10, 2500), ErrWarmKeyNotLive)
	require.ErrorIs(t, h.Bind(0, "gonka1again", 15, 10, 1000), ErrWarmKeyNonce, "a bind before the revoke nonce")
	require.NoError(t, h.Bind(0, "gonka1warm", 30, 25, 2500), "a revoked key can be bound again with a later grant")

	require.Len(t, h[0], 2)
	require.Equal(t, []string{"gonka1warm"}, h.Live(0))
}

// Test flow:
//  1. Build a history with two keys on one slot and clone it.
//  2. Assert a revoke on the clone does not touch the original, and a nil history clones to nil.
func TestWarmKeyHistoryClonesIndependently(t *testing.T) {
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1old", 10, 10, 1000))
	require.NoError(t, h.Bind(0, "gonka1new", 30, 12, 1200))

	clone := h.Clone()
	require.NoError(t, clone.Revoke(0, "gonka1old", 40, 10, 4000))
	require.Equal(t, uint64(0), h[0][0].RevokedAtNonce, "the clone is independent")
	require.Nil(t, WarmKeyHistory(nil).Clone())
}

// Test flow:
//  1. Ask the rule about an empty key, a slot outside the group, and a stranger.
//  2. Assert each is refused; the cold key is always accepted.
func TestWarmKeyHistoryAllowsColdKeyAndRefusesStrangers(t *testing.T) {
	slots := map[uint32]string{0: "gonka1cold"}
	h := WarmKeyHistory{}

	require.True(t, h.Allows(slots, 0, "gonka1cold", 0))
	require.False(t, h.Allows(slots, 0, "", 0))
	require.False(t, h.Allows(slots, 9, "gonka1cold", 0))
	require.False(t, h.Allows(slots, 0, "gonka1stranger", 0))
}

// Test flow:
//  1. Bind a key by grant 10 (height 1000), revoke that grant at height 2000.
//  2. Assert the same grant cannot bind the key again, a revoke must name the live grant, a revoke height below the
//     grant height is refused, and a later grant binds the key again even in the same block as the revoke.
func TestWarmKeyGrantIDsOrderBindsAndRevokes(t *testing.T) {
	h := WarmKeyHistory{}
	require.NoError(t, h.Bind(0, "gonka1warm", 10, 10, 1000))
	require.ErrorIs(t, h.Revoke(0, "gonka1warm", 20, 11, 2000), ErrWarmKeyGrant, "a revoke naming another grant")
	require.ErrorIs(t, h.Revoke(0, "gonka1warm", 20, 10, 999), ErrWarmKeyHeight, "a revoke before the grant height")
	require.NoError(t, h.Revoke(0, "gonka1warm", 20, 10, 2000))

	require.ErrorIs(t, h.Bind(0, "gonka1warm", 30, 10, 1000), ErrWarmKeyGrant, "the old grant after the revoke")
	require.ErrorIs(t, h.Bind(0, "gonka1warm", 30, 9, 2000), ErrWarmKeyGrant, "an older grant id")
	require.NoError(t, h.Bind(0, "gonka1warm", 30, 11, 2000), "a replacement granted in the revoke's block")
	require.Equal(t, []string{"gonka1warm"}, h.Live(0))
	require.Equal(t, uint64(2000), h[0][0].RevokedAtHeight)
	require.Equal(t, uint64(11), h[0][1].GrantID)
}
