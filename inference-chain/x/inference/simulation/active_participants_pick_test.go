package simulation_test

import (
	"math/rand"
	"testing"

	"github.com/cosmos/cosmos-sdk/testutil/simsx"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/simulation"
)

// TestPickRandomActiveSimAccount_PicksFromCurrentEpoch — with a non-empty
// ActiveParticipantsSet for currentEpoch, the picker returns one of the
// registered sim accounts and does not Skip the reporter.
func TestPickRandomActiveSimAccount_PicksFromCurrentEpoch(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 4, 5)
	registerAsParticipants(t, k, ctx, accs)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx, k))

	cds := simsx.NewChainDataSource(ctx, rand.New(rand.NewSource(99)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	got, ok := simulation.PickRandomActiveSimAccount(ctx, k, cds, reporter)
	require.True(t, ok)
	require.False(t, reporter.IsSkipped(), "reporter unexpectedly skipped")

	addrs := map[string]bool{}
	for _, a := range accs {
		addrs[a.Address.String()] = true
	}
	require.True(t, addrs[got.AddressBech32],
		"picked address %s not in seeded set", got.AddressBech32)
}

// TestPickRandomActiveSimAccount_EmptySet_Skips — without seeding, the
// picker sets reporter.Skip and returns false.
func TestPickRandomActiveSimAccount_EmptySet_Skips(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 5, 3)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))

	cds := simsx.NewChainDataSource(ctx, rand.New(rand.NewSource(1)),
		nil, nil, gonkaBech32Codec(), accs...)
	reporter := simsx.NewBasicSimulationReporter()

	_, ok := simulation.PickRandomActiveSimAccount(ctx, k, cds, reporter)
	require.False(t, ok)
	require.True(t, reporter.IsSkipped(),
		"empty ActiveParticipantsSet should cause Skip")
}

// TestPickRandomActiveSimAccount_DeterministicWithSeed — same seed in the
// data-source's *rand.Rand must yield identical picks across calls. The
// helper is sim-only and must be deterministic for replayable simulation.
func TestPickRandomActiveSimAccount_DeterministicWithSeed(t *testing.T) {
	k1, ctx1 := keepertest.InferenceKeeper(t)
	k2, ctx2 := keepertest.InferenceKeeper(t)
	accs := newSimAccounts(t, 6, 5)
	registerAsParticipants(t, k1, ctx1, accs)
	registerAsParticipants(t, k2, ctx2, accs)
	require.NoError(t, k1.SetEffectiveEpochIndex(ctx1, 0))
	require.NoError(t, k2.SetEffectiveEpochIndex(ctx2, 0))
	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx1, k1))
	require.NoError(t, simulation.EnsureActiveParticipantsSeeded(ctx2, k2))

	const seed = int64(123)
	cds1 := simsx.NewChainDataSource(ctx1, rand.New(rand.NewSource(seed)),
		nil, nil, gonkaBech32Codec(), accs...)
	cds2 := simsx.NewChainDataSource(ctx2, rand.New(rand.NewSource(seed)),
		nil, nil, gonkaBech32Codec(), accs...)
	r1 := simsx.NewBasicSimulationReporter()
	r2 := simsx.NewBasicSimulationReporter()

	got1, ok1 := simulation.PickRandomActiveSimAccount(ctx1, k1, cds1, r1)
	got2, ok2 := simulation.PickRandomActiveSimAccount(ctx2, k2, cds2, r2)
	require.True(t, ok1)
	require.True(t, ok2)
	require.Equal(t, got1.AddressBech32, got2.AddressBech32)
}
