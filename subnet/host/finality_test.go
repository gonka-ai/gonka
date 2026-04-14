package host

import (
	"testing"

	"github.com/stretchr/testify/require"

	"subnet/storage"
	"subnet/types"
)

func makeSlotGroup(n int) []types.SlotAssignment {
	g := make([]types.SlotAssignment, n)
	for i := 0; i < n; i++ {
		g[i] = types.SlotAssignment{SlotID: uint32(i), ValidatorAddress: "v"}
	}
	return g
}

// newMemorySessionWithNonces creates an escrow session and appends empty diffs for nonces 1..latestNonce.
func newMemorySessionWithNonces(t *testing.T, escrowID string, group []types.SlotAssignment, latestNonce uint64) *storage.Memory {
	t.Helper()
	store := storage.NewMemory()
	err := store.CreateSession(storage.CreateSessionParams{
		EscrowID: escrowID,
		Group:    group,
	})
	require.NoError(t, err)
	for n := uint64(1); n <= latestNonce; n++ {
		err := store.AppendDiff(escrowID, types.DiffRecord{Diff: types.Diff{Nonce: n}})
		require.NoError(t, err)
	}
	return store
}

func addSignatures(t *testing.T, store *storage.Memory, escrowID string, byNonce map[uint64][]uint32) {
	t.Helper()
	for nonce, slots := range byNonce {
		for _, slotID := range slots {
			err := store.AddSignature(escrowID, nonce, slotID, []byte{1})
			require.NoError(t, err)
		}
	}
}

// F=0 when no nonce reaches the 2/3 slot threshold (4 slots → need 3; only 1 signs nonce 1).
func TestComputeFinalizedNonce_F0_insufficientSigners(t *testing.T) {
	group := makeSlotGroup(4)
	store := newMemorySessionWithNonces(t, "e1", group, 1)
	addSignatures(t, store, "e1", map[uint64][]uint32{1: {0}})

	f := computeFinalizedNonce(store, "e1", 1, group)
	require.Equal(t, uint64(0), f)
}

// F=N when ≥2/3 slots have signed at some nonce ≥ N; a later partial nonce does not raise F past the
// last nonce that still had a supermajority at that height.
func TestComputeFinalizedNonce_F3_partialNonce4DoesNotRaise(t *testing.T) {
	group := makeSlotGroup(4)
	store := newMemorySessionWithNonces(t, "e1", group, 4)
	addSignatures(t, store, "e1", map[uint64][]uint32{
		3: {0, 1, 2},
		4: {0, 1},
	})

	f := computeFinalizedNonce(store, "e1", 4, group)
	require.Equal(t, uint64(3), f)
}

// Signing only at a high nonce implies confirmation of all lower nonces (transitivity).
func TestComputeFinalizedNonce_transitivity_onlyHighNonceSupermajority(t *testing.T) {
	group := makeSlotGroup(4)
	store := newMemorySessionWithNonces(t, "e1", group, 5)
	// No signatures on nonces 1–4; slots 0,1,2 sign only at nonce 5 → confirms 1..5.
	addSignatures(t, store, "e1", map[uint64][]uint32{
		5: {0, 1, 2},
	})

	f := computeFinalizedNonce(store, "e1", 5, group)
	require.Equal(t, uint64(5), f)
}

func TestComputeFinalizedNonce_threshold_groupOf3(t *testing.T) {
	group := makeSlotGroup(3) // ceil(2/3 * 3) = 2
	store := newMemorySessionWithNonces(t, "e1", group, 1)
	addSignatures(t, store, "e1", map[uint64][]uint32{1: {0, 1}})

	f := computeFinalizedNonce(store, "e1", 1, group)
	require.Equal(t, uint64(1), f)
}

func TestComputeFinalizedNonce_threshold_groupOf6(t *testing.T) {
	group := makeSlotGroup(6) // ceil(2/3 * 6) = 4
	store := newMemorySessionWithNonces(t, "e1", group, 1)
	addSignatures(t, store, "e1", map[uint64][]uint32{1: {0, 1, 2, 3}})

	f := computeFinalizedNonce(store, "e1", 1, group)
	require.Equal(t, uint64(1), f)
}

func TestComputeFinalizedNonce_latestNonceZero(t *testing.T) {
	group := makeSlotGroup(4)
	store := newMemorySessionWithNonces(t, "e1", group, 1)

	f := computeFinalizedNonce(store, "e1", 0, group)
	require.Equal(t, uint64(0), f)
}
