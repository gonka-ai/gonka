package inference

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
)

func TestAddEpochMembers_IndexesAdmittedTrustForFirstEpoch(t *testing.T) {
	k, ctx, _ := newMinimalInferenceKeeperWithStub(t)
	accounts := newFormationAccountKeeper(t, testutil.Validator, testutil.Validator2, testutil.Executor)
	am := NewAppModule(nil, k, accounts, nil, nil, nil)

	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 0))
	_, err := k.GetEpochTrust(ctx, 0)
	require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)

	firstEpoch, err := k.CreateEpochGroup(ctx, 100, 1)
	require.NoError(t, err)
	require.NoError(t, firstEpoch.CreateGroup(ctx))

	participants := []*types.ActiveParticipant{
		{Index: testutil.Validator, Weight: 400, CapWeight: 400, Seed: &types.RandomSeed{Signature: "seed-1"}},
		{Index: testutil.Validator2, Weight: 350, CapWeight: 350, Seed: &types.RandomSeed{Signature: "seed-2"}},
		{Index: testutil.Executor, Weight: 250, CapWeight: 250},
	}
	am.addEpochMembers(ctx, firstEpoch, participants)

	total, err := k.EpochTrustWeightTotals.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, int64(750), total)
	trust, err := k.GetEpochTrust(ctx, 1)
	require.NoError(t, err)
	for address, expected := range map[string]int64{testutil.Validator: 400, testutil.Validator2: 350, testutil.Executor: 0} {
		weight, err := k.EpochTrustWeight(ctx, trust, sdk.MustAccAddressFromBech32(address))
		require.NoError(t, err)
		require.Equal(t, expected, weight)
	}

	_, err = k.GetEpochTrust(ctx, 0)
	require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)
}
