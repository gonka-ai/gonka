package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// devshardVersionPoliciesNamespace is the on-chain prefix byte for version
// policies. It is a literal, not DevshardVersionPoliciesPrefix, so moving that
// constant hides rows written by an older binary. A miss scores as DERIVED.
const devshardVersionPoliciesNamespace = 114

func TestVersionPolicyPrefix_ReadsLiteralNamespace(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	require.Equal(t,
		collections.NewPrefix(devshardVersionPoliciesNamespace),
		types.DevshardVersionPoliciesPrefix,
	)

	const name = "v-sampled"
	policy := types.DevshardVersionPolicy{
		Name:      name,
		PassCount: types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED,
	}
	bz, err := k.Codec().Marshal(&policy)
	require.NoError(t, err)

	key, err := collections.EncodeKeyWithPrefix(
		collections.NewPrefix(devshardVersionPoliciesNamespace),
		collections.StringKey,
		name,
	)
	require.NoError(t, err)
	require.NotEmpty(t, key)
	require.Equal(t, byte(devshardVersionPoliciesNamespace), key[0])

	require.NoError(t, keeper.SetRawStoreBytesForTesting(k, ctx, key, bz))

	got, found, err := k.GetVersionPolicy(ctx, name)
	require.NoError(t, err)
	require.True(t, found, "policy bytes under prefix 114 must stay visible")
	require.Equal(t, policy, got)

	pass, err := k.PassCountFor(ctx, name)
	require.NoError(t, err)
	require.Equal(t, types.DevshardPassCount_DEVSHARD_PASS_COUNT_SAMPLED, pass,
		"a moved prefix misses the row and PassCountFor falls back to DERIVED")
}
