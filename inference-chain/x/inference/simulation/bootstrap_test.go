package simulation_test

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/simulation"
)

// TestEnsureActiveParticipantsSeeded_SeedsCurrentEpoch — with N Participants
// registered and ActiveParticipantsSet empty for currentEpoch, the helper
// promotes all of them.
func TestEnsureActiveParticipantsSeeded_SeedsCurrentEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 1, 5)
	regs := registerAsParticipants(t, k, ctx, accs)
	const epoch = uint64(7)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))

	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))

	got := collectActiveAddrs(t, ctx, k, epoch)
	sort.Strings(got)
	want := append([]string{}, regs...)
	sort.Strings(want)
	require.Equal(t, want, got)
}

// TestEnsureActiveParticipantsSeeded_Idempotent — a second call with
// ActiveParticipantsSet non-empty for currentEpoch returns nil and does
// not change the set.
func TestEnsureActiveParticipantsSeeded_Idempotent(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 2, 3)
	registerAsParticipants(t, k, ctx, accs)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))

	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))
	first := collectActiveAddrs(t, ctx, k, 0)

	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))
	second := collectActiveAddrs(t, ctx, k, 0)

	// collectActiveAddrs returns store-ordered (deterministic) addresses;
	// first and second use the same helper, so a direct Equal is valid.
	require.Equal(t, first, second)
}

// TestEnsureActiveParticipantsSeeded_NoParticipants_NoOp — empty Participants
// collection ⇒ helper returns nil without populating ActiveParticipantsSet.
func TestEnsureActiveParticipantsSeeded_NoParticipants_NoOp(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))

	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))

	require.Empty(t, collectActiveAddrs(t, ctx, k, 0))
}

// TestEnsureModelsInEpochGroup_TolerantOnMissingEpoch — without an
// EffectiveEpochIndex set, GetCurrentEpochGroup returns
// ErrEffectiveEpochNotFound; the helper must treat that as a no-op
// (returns nil) so unit-test keepers without full InitGenesisEpochGroup
// wiring still let the factories run.
func TestEnsureModelsInEpochGroup_TolerantOnMissingEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	// No SetEffectiveEpochIndex call — GetCurrentEpochGroup will fail.
	require.NoError(t, simulation.EnsureModelsInEpochGroup(ctx, k))
}

// TestEnsureModelsInEpochGroup_TolerantOnMissingEpochGroupData — index
// is set but no EpochGroupData exists yet; helper must also no-op for
// this branch (ErrEpochGroupDataNotFound).
func TestEnsureModelsInEpochGroup_TolerantOnMissingEpochGroupData(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	require.NoError(t, simulation.EnsureModelsInEpochGroup(ctx, k))
}

// TestEnsureMembersInEpochGroup_TolerantOnMissingEpoch — no
// EffectiveEpochIndex set ⇒ GetCurrentEpochGroup fails with
// ErrEffectiveEpochNotFound; helper no-ops. Same tolerance contract as
// EnsureModelsInEpochGroup.
func TestEnsureMembersInEpochGroup_TolerantOnMissingEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, simulation.EnsureMembersInEpochGroup(ctx, k))
}

// TestEnsureMembersInEpochGroup_TolerantOnMissingEpochGroupData — index
// is set but no EpochGroupData exists yet; helper no-ops.
func TestEnsureMembersInEpochGroup_TolerantOnMissingEpochGroupData(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	require.NoError(t, simulation.EnsureMembersInEpochGroup(ctx, k))
}

// TestEnsureActiveParticipantsSeeded_RespectsEffectiveEpoch — the helper
// writes to currentEpoch (= EffectiveEpochIndex) and leaves other epoch
// slots untouched.
func TestEnsureActiveParticipantsSeeded_RespectsEffectiveEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 3, 4)
	registerAsParticipants(t, k, ctx, accs)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 42))

	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))

	require.Len(t, collectActiveAddrs(t, ctx, k, 42), len(accs))
	require.Empty(t, collectActiveAddrs(t, ctx, k, 0))
	require.Empty(t, collectActiveAddrs(t, ctx, k, 41))
	require.Empty(t, collectActiveAddrs(t, ctx, k, 43))
}
