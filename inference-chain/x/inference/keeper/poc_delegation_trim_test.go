package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const delegationModel = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"

func submittedDelegation() types.PoCDelegation {
	return types.PoCDelegation{
		ModelId:    delegationModel,
		Delegator:  testutil.Executor,
		DelegateTo: testutil.Executor2,
	}
}

func TestPoCDelegation_ValueOmitsKeyFields(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	want := submittedDelegation()

	gctx := ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.SetPoCDelegation(gctx, want))
	t.Logf("SetPoCDelegation gas: %d", gctx.GasMeter().GasConsumed())

	raw, err := k.PoCDelegations.Get(ctx, collections.Join(delegationModel, testutil.Executor))
	require.NoError(t, err)
	require.Equal(t, types.PoCDelegation{DelegateTo: testutil.Executor2}, raw)

	gctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	got, found := k.GetPoCDelegation(gctx, delegationModel, testutil.Executor)
	t.Logf("GetPoCDelegation gas: %d", gctx.GasMeter().GasConsumed())
	require.True(t, found)
	require.Equal(t, want, got)

	forModel, err := k.GetPoCDelegationsForModel(ctx, delegationModel)
	require.NoError(t, err)
	require.Equal(t, []types.PoCDelegation{want}, forModel)
	forParticipant, err := k.GetPoCDelegationsForParticipant(ctx, testutil.Executor)
	require.NoError(t, err)
	require.Equal(t, []types.PoCDelegation{want}, forParticipant)
}

func TestPoCDelegation_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	full := submittedDelegation()
	require.NoError(t, k.PoCDelegations.Set(ctx, collections.Join(delegationModel, testutil.Executor), full))

	got, found := k.GetPoCDelegation(ctx, delegationModel, testutil.Executor)
	require.True(t, found)
	require.Equal(t, full, got)
	all, err := k.GetAllPoCDelegations(ctx)
	require.NoError(t, err)
	require.Equal(t, []types.PoCDelegation{full}, all)
}
