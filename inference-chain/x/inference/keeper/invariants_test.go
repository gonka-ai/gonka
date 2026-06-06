package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdkmath "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// --- A1: BankBacksPositiveBalance ----------------------------------------

func TestInvariant_BankBacksPositiveBalance_Holds(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	// Two participants, total positive CoinBalance = 300.
	setParticipant(t, k, ctx, "gonka1aaa", 100)
	setParticipant(t, k, ctx, "gonka1bbb", 200)
	// Module account has enough to back.
	expectModuleBalance(mocks, 500)

	msg, broken := keeper.BankBacksPositiveBalanceInvariant(k)(ctx)
	require.False(t, broken, "should hold; msg=%q", msg)
}

func TestInvariant_BankBacksPositiveBalance_Broken(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	setParticipant(t, k, ctx, "gonka1aaa", 1000)
	// Module account has only 100 — owed 1000.
	expectModuleBalance(mocks, 100)

	msg, broken := keeper.BankBacksPositiveBalanceInvariant(k)(ctx)
	require.True(t, broken, "should be broken")
	require.Contains(t, msg, "owed 1000")
}

// --- B: NoStuckVoting ----------------------------------------------------

func TestInvariant_NoStuckVoting_Holds(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	// VOTING inference from epoch 4: 4 + 2 = 6 >= 5 → holds.
	setInference(t, k, ctx, "id-fresh", types.InferenceStatus_VOTING, 4)

	msg, broken := keeper.NoStuckVotingInvariant(k)(ctx)
	require.False(t, broken, "should hold; msg=%q", msg)
}

func TestInvariant_NoStuckVoting_Broken(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	// VOTING from epoch 2: 2 + 2 = 4 < 5 → stuck.
	setInference(t, k, ctx, "id-stuck", types.InferenceStatus_VOTING, 2)

	msg, broken := keeper.NoStuckVotingInvariant(k)(ctx)
	require.True(t, broken)
	require.Contains(t, msg, "id-stuck")
	require.Contains(t, msg, "epoch 2")
}

// --- C: EffectiveEpochFresh ----------------------------------------------

func TestInvariant_EffectiveEpochFresh_Holds(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	require.NoError(t, k.Epochs.Set(ctx, 3, types.Epoch{Index: 3}))
	require.NoError(t, k.Epochs.Set(ctx, 5, types.Epoch{Index: 5}))

	msg, broken := keeper.EffectiveEpochFreshInvariant(k)(ctx)
	require.False(t, broken, "should hold; msg=%q", msg)
}

func TestInvariant_EffectiveEpochFresh_Broken(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 3))
	// Stored epoch 7 > current 3 → broken.
	require.NoError(t, k.Epochs.Set(ctx, 7, types.Epoch{Index: 7}))

	msg, broken := keeper.EffectiveEpochFreshInvariant(k)(ctx)
	require.True(t, broken)
	require.Contains(t, msg, "EffectiveEpochIndex=3")
	require.Contains(t, msg, "max Epoch.Index=7")
}

// --- D: ActiveInvalidationsRefLive ---------------------------------------

func TestInvariant_ActiveInvalidationsRefLive_Holds(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	setInference(t, k, ctx, "live-inf", types.InferenceStatus_VOTING, 1)
	validator := sdk.AccAddress("validator-aaaa")
	require.NoError(t, k.ActiveInvalidations.Set(ctx, collections.Join(validator, "live-inf")))

	msg, broken := keeper.ActiveInvalidationsRefLiveInvariant(k)(ctx)
	require.False(t, broken, "should hold; msg=%q", msg)
}

func TestInvariant_ActiveInvalidationsRefLive_Broken(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	validator := sdk.AccAddress("validator-bbbb")
	// No Inferences entry for this id → dangling reference.
	require.NoError(t, k.ActiveInvalidations.Set(ctx, collections.Join(validator, "ghost-inf")))

	msg, broken := keeper.ActiveInvalidationsRefLiveInvariant(k)(ctx)
	require.True(t, broken)
	require.Contains(t, msg, "ghost-inf")
}

// --- AllInvariants composite ---------------------------------------------

func TestAllInvariants_StopsOnFirstBroken(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	// A1 always reaches SpendableCoin regardless of whether it trips;
	// mock it generously so we can isolate the C violation we want to fire.
	expectModuleBalance(mocks, 1_000_000)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 3))
	// Trigger C: stored epoch ahead of current.
	require.NoError(t, k.Epochs.Set(ctx, 9, types.Epoch{Index: 9}))

	msg, broken := keeper.AllInvariants(k)(ctx)
	require.True(t, broken)
	require.Contains(t, msg, "effective-epoch-fresh")
	require.Contains(t, msg, "invariant")
}

// --- helpers -------------------------------------------------------------

func setParticipant(t *testing.T, k keeper.Keeper, ctx sdk.Context, addr string, balance int64) {
	t.Helper()
	accAddr, err := sdk.AccAddressFromBech32(addr)
	if err != nil {
		// Allow non-bech32 test strings; use raw bytes.
		accAddr = sdk.AccAddress(addr)
	}
	require.NoError(t, k.Participants.Set(ctx, accAddr, types.Participant{
		Address:           accAddr.String(),
		CoinBalance:       balance,
		CurrentEpochStats: &types.CurrentEpochStats{},
	}))
}

func setInference(t *testing.T, k keeper.Keeper, ctx sdk.Context, id string, status types.InferenceStatus, epoch uint64) {
	t.Helper()
	require.NoError(t, k.Inferences.Set(ctx, id, types.Inference{
		InferenceId: id,
		Status:      status,
		EpochId:     epoch,
	}))
}

func expectModuleBalance(mocks keepertest.InferenceMocks, amount int64) {
	mocks.BankViewKeeper.EXPECT().
		SpendableCoin(gomock.Any(),
			authtypes.NewModuleAddress(types.ModuleName),
			types.BaseCoin).
		Return(sdk.NewCoin(types.BaseCoin, sdkmath.NewInt(amount))).
		AnyTimes()
}
