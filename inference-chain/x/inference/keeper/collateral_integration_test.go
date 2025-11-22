package keeper_test

import (
	"testing"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/sample"
	blskeeper "github.com/productscience/inference/x/bls/keeper"
	collateralKeeper "github.com/productscience/inference/x/collateral/keeper"
	collateralTypes "github.com/productscience/inference/x/collateral/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"

	"cosmossdk.io/log"
	"cosmossdk.io/store"
	"cosmossdk.io/store/metrics"
	storetypes "cosmossdk.io/store/types"
	cmtproto "github.com/cometbft/cometbft/proto/tendermint/types"
	dbm "github.com/cosmos/cosmos-db"
	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
)

// This file is for integration-style tests that involve message servers and complex state.
// For simpler keeper tests, see keeper_test.go.

func setupKeeperWithMocksForIntegration(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	return k, keeper.NewMsgServerImpl(k), ctx, &mock
}

func setupRealKeepers(t testing.TB) (sdk.Context, keeper.Keeper, collateralKeeper.Keeper, types.MsgServer, collateralTypes.MsgServer, *keepertest.InferenceMocks) {
	// --- Store and Codec Setup ---
	inferenceStoreKey := storetypes.NewKVStoreKey(types.StoreKey)
	collateralStoreKey := storetypes.NewKVStoreKey(collateralTypes.StoreKey)

	db := dbm.NewMemDB()
	stateStore := store.NewCommitMultiStore(db, log.NewNopLogger(), metrics.NewNoOpMetrics())
	stateStore.MountStoreWithDB(inferenceStoreKey, storetypes.StoreTypeIAVL, db)
	stateStore.MountStoreWithDB(collateralStoreKey, storetypes.StoreTypeIAVL, db)
	require.NoError(t, stateStore.LoadLatestVersion())

	registry := codectypes.NewInterfaceRegistry()
	cdc := codec.NewProtoCodec(registry)
	ctx := sdk.NewContext(stateStore, cmtproto.Header{}, false, log.NewNopLogger())
	authority := authtypes.NewModuleAddress(govtypes.ModuleName)

	// --- Mock Keepers ---
	ctrl := gomock.NewController(t)
	bookkepingBankMock := keepertest.NewMockBookkeepingBankKeeper(ctrl)
	bankViewMock := keepertest.NewMockBankKeeper(ctrl)
	accountMock := keepertest.NewMockAccountKeeper(ctrl)
	validatorSetMock := keepertest.NewMockValidatorSet(ctrl)
	groupMock := keepertest.NewMockGroupMessageKeeper(ctrl)
	stakingMock := keepertest.NewMockStakingKeeper(ctrl)
	streamvestingMock := keepertest.NewMockStreamVestingKeeper(ctrl)
	authzMock := keepertest.NewMockAuthzKeeper(ctrl)
	mocks := &keepertest.InferenceMocks{
		BankKeeper:          bookkepingBankMock,
		AccountKeeper:       accountMock,
		GroupKeeper:         groupMock,
		StakingKeeper:       stakingMock,
		StreamVestingKeeper: streamvestingMock,
		BankViewKeeper:      bankViewMock,
		AuthzKeeper:         authzMock,
	}

	// --- Real Keepers ---
	cKeeper := collateralKeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(collateralStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
		nil,                // bank keeper
		bookkepingBankMock, // bookkeeping bank keeper
	)

	// Create a BLS keeper for testing (similar to testutil/keeper/inference.go)
	blsStoreKey := storetypes.NewKVStoreKey("bls")
	stateStore.MountStoreWithDB(blsStoreKey, storetypes.StoreTypeIAVL, db)
	blsKeeper := blskeeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(blsStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
	)

	upgradeMock := keepertest.NewMockUpgradeKeeper(ctrl)
	inferenceKeeper := keeper.NewKeeper(
		cdc,
		runtime.NewKVStoreService(inferenceStoreKey),
		keepertest.PrintlnLogger{},
		authority.String(),
		bookkepingBankMock,
		bankViewMock,
		groupMock,
		validatorSetMock,
		stakingMock,
		accountMock,
		blsKeeper,
		cKeeper,
		streamvestingMock,
		authzMock,
		nil,
		upgradeMock,
	)

	// Initialize default params for both keepers
	require.NoError(t, cKeeper.SetParams(ctx, collateralTypes.DefaultParams()))
	require.NoError(t, inferenceKeeper.SetParams(ctx, types.DefaultParams()))

	inferenceMsgSrv := keeper.NewMsgServerImpl(inferenceKeeper)
	collateralMsgSrv := collateralKeeper.NewMsgServerImpl(cKeeper)

	// Mock necessary bank calls
	bookkepingBankMock.EXPECT().SendCoinsFromAccountToModule(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkepingBankMock.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkepingBankMock.EXPECT().BurnCoins(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes().Return(nil)
	bookkepingBankMock.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
	return ctx, inferenceKeeper, cKeeper, inferenceMsgSrv, collateralMsgSrv, mocks
}

func TestSlashingForInvalidStatus_Integration(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForIntegration(t)

	// Set parameters for slashing
	params := types.DefaultParams()
	slashFraction := types.DecimalFromFloat(0.2)
	params.CollateralParams.SlashFractionInvalid = slashFraction
	k.SetParams(ctx, params)

	// Setup participant
	participantAddrStr := sample.AccAddress()
	participantAcc, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)

	participant := &types.Participant{
		Address: participantAddrStr,
		Status:  types.ParticipantStatus_INVALID, // The new status
	}

	// Mock the slash call on the collateral keeper
	expectedSlashFraction, err := slashFraction.ToLegacyDec()
	require.NoError(t, err)
	mocks.CollateralKeeper.EXPECT().
		Slash(gomock.Any(), participantAcc, expectedSlashFraction, types.SlashReasonInvalidation).
		Return(sdk.NewCoin(types.BaseCoin, math.NewInt(0)), nil).Times(1)

	// Execute the function under test directly
	k.SlashForInvalidStatus(ctx, participant, params)
}

func TestSlashingForDowntime_Integration(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocksForIntegration(t)

	// Set parameters for slashing
	params := types.DefaultParams()
	downtimeThreshold := types.DecimalFromFloat(0.5) // 50%
	slashFraction := types.DecimalFromFloat(0.1)     // 10%
	params.CollateralParams.DowntimeMissedPercentageThreshold = downtimeThreshold
	params.CollateralParams.SlashFractionDowntime = slashFraction
	k.SetParams(ctx, params)

	// Setup participant
	participantAddrStr := sample.AccAddress()
	participantAcc, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)

	participant := &types.Participant{
		Address: participantAddrStr,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 5,
			MissedRequests: 6, // 6 out of 11 total = ~54.5% > 50% threshold
		},
	}

	// Mock the slash call on the collateral keeper
	expectedSlashFraction, err := slashFraction.ToLegacyDec()
	require.NoError(t, err)
	mocks.CollateralKeeper.EXPECT().
		Slash(gomock.Any(), participantAcc, expectedSlashFraction, types.SlashReasonDowntime).
		Return(sdk.NewCoin(types.BaseCoin, math.NewInt(0)), nil).Times(1)

	// Execute the function under test directly
	k.SlashForDowntime(ctx, participant, params)
}

func TestInvalidateInference_FullFlow_WithStatefulMock(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocksForIntegration(t)

	// --- Test Setup ---
	// Set the epoch, which is critical for many keeper functions
	ee := setEffectiveEpoch(ctx, k, 1, mocks)
	require.NoError(t, ee)

	// Set parameters for slashing and validation
	params := types.DefaultParams()
	slashFraction := types.DecimalFromFloat(0.2)
	params.CollateralParams.SlashFractionInvalid = slashFraction
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	k.SetParams(ctx, params)

	// Setup participant and authority
	participantAddrStr := sample.AccAddress()
	participantAcc, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)
	authority := k.GetAuthority()

	// --- Stateful Mock Logic ---
	fakeCollateralAmount := math.NewInt(1000)

	// Mock GetCollateral to return the current value of our fake collateral
	mocks.CollateralKeeper.EXPECT().GetCollateral(gomock.Any(), participantAcc).DoAndReturn(
		func(ctx sdk.Context, pa sdk.AccAddress) (sdk.Coin, bool) {
			return sdk.NewCoin(types.BaseCoin, fakeCollateralAmount), true
		}).AnyTimes()
	mocks.BankKeeper.EXPECT().LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	// Mock Slash to modify our fake collateral
	expectedSlashFraction, err := slashFraction.ToLegacyDec()
	require.NoError(t, err)
	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), participantAcc, expectedSlashFraction, types.SlashReasonInvalidation).DoAndReturn(
		func(ctx sdk.Context, pa sdk.AccAddress, fraction math.LegacyDec, reason string) (sdk.Coin, error) {
			slashedAmount := fakeCollateralAmount.ToLegacyDec().Mul(fraction).TruncateInt()
			fakeCollateralAmount = fakeCollateralAmount.Sub(slashedAmount)
			return sdk.NewCoin(types.BaseCoin, slashedAmount), nil
		}).Times(1)
	// --- End Stateful Mock Logic ---

	// Set up the participant with 4 consecutive failures, just under the threshold
	k.SetParticipant(ctx, types.Participant{
		Index:                        participantAddrStr,
		Address:                      participantAddrStr,
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 4,
		CurrentEpochStats:            &types.CurrentEpochStats{},
	})
	// The authority also needs to be a registered participant to invalidate
	k.SetParticipant(ctx, types.Participant{Index: authority, Address: authority, CurrentEpochStats: &types.CurrentEpochStats{}})

	// Mock bank keeper for the refund logic, even though cost is 0
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any())
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any())
	// Setup the inference object that will be invalidated
	inferenceId := "test-inference-to-trigger-invalid"
	k.SetInference(ctx, types.Inference{
		Index:           inferenceId,
		InferenceId:     inferenceId,
		ExecutedBy:      participantAddrStr,
		RequestedBy:     authority,
		Status:          types.InferenceStatus_FINISHED,
		ActualCost:      0,
		ProposalDetails: &types.ProposalDetails{PolicyAddress: authority},
	})

	// Get initial collateral (will use our mock)
	initialCollateral, found := k.GetCollateralKeeper().GetCollateral(ctx, participantAcc)
	require.True(t, found)
	require.Equal(t, math.NewInt(1000), initialCollateral.Amount)

	// Execute the invalidation. This should increment ConsecutiveInvalidInferences to 5,
	// which will trigger calculateStatus to return INVALID, and then trigger the slash.
	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{
		Creator:     authority,
		InferenceId: inferenceId,
	})
	require.NoError(t, err)

	// Final check on participant status
	finalParticipant, found := k.GetParticipant(ctx, participantAddrStr)
	require.True(t, found)
	require.Equal(t, types.ParticipantStatus_INVALID, finalParticipant.Status)

	// Get final collateral (will also use our mock)
	finalCollateral, found := k.GetCollateralKeeper().GetCollateral(ctx, participantAcc)
	require.True(t, found)

	// Calculate expected result and assert
	expectedAmount := math.NewInt(800)
	require.Equal(t, expectedAmount, finalCollateral.Amount)
	// And also check the fake variable directly for good measure
	require.Equal(t, expectedAmount, fakeCollateralAmount)
}

func TestDoubleJeopardy_DowntimeThenInvalidSlash(t *testing.T) {
	ctx, k, ck, ms, collateralMsgSrv, mocks := setupRealKeepers(t)
	authority := k.GetAuthority()

	// --- Setup Parameters ---
	params := types.DefaultParams()
	params.CollateralParams.DowntimeMissedPercentageThreshold = types.DecimalFromFloat(0.5) // 50%
	params.CollateralParams.SlashFractionDowntime = types.DecimalFromFloat(0.1)             // 10%
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.2)              // 20%
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	k.SetParams(ctx, params)
	participantAddrStr := sample.AccAddress()
	participantAcc, err := sdk.AccAddressFromBech32(participantAddrStr)
	require.NoError(t, err)

	initialCollateralAmount := math.NewInt(1000)
	_, err = collateralMsgSrv.DepositCollateral(ctx, &collateralTypes.MsgDepositCollateral{
		Participant: participantAddrStr,
		Amount:      sdk.NewCoin(types.BaseCoin, initialCollateralAmount),
	})
	require.NoError(t, err)
	ee := setEffectiveEpoch(ctx, k, 1, mocks)
	require.NoError(t, ee)

	// --- 1. First Jeopardy: Downtime Slash ---
	// Set the participant's epoch stats to trigger downtime slashing.
	k.SetParticipant(ctx, types.Participant{
		Index:   participantAddrStr,
		Address: participantAddrStr,
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 10,
			MissedRequests: 90, // 90% missed requests
		},
	})
	participant, found := k.GetParticipant(ctx, participantAddrStr)
	require.True(t, found)

	// Manually call the downtime slashing logic.
	k.SlashForDowntime(ctx, &participant, params)

	// Verify collateral was slashed for downtime
	collateralAfterDowntimeCoin, found := ck.GetCollateral(ctx, participantAcc)
	require.True(t, found)

	downtimeSlash, err := params.CollateralParams.SlashFractionDowntime.ToLegacyDec()
	require.NoError(t, err)
	expectedAfterDowntime := initialCollateralAmount.ToLegacyDec().Mul(math.LegacyOneDec().Sub(downtimeSlash)).TruncateInt()

	require.Equal(t, expectedAfterDowntime, collateralAfterDowntimeCoin.Amount, "Collateral should be slashed for downtime")
	// expectedAfterDowntime is now 900

	// --- 2. Second Jeopardy: Invalid Status Slash ---
	// Update participant state for the next test stage. We fetch it again to get the
	// latest version after the potential downtime slash logic modified it.
	participant, found = k.GetParticipant(ctx, participantAddrStr)
	require.True(t, found)
	participant.Status = types.ParticipantStatus_ACTIVE
	participant.ConsecutiveInvalidInferences = 4
	participant.CurrentEpochStats = &types.CurrentEpochStats{} // Reset for the new epoch
	k.SetParticipant(ctx, participant)

	// The authority also needs to be a registered participant to invalidate
	k.SetParticipant(ctx, types.Participant{Index: authority, Address: authority, CurrentEpochStats: &types.CurrentEpochStats{}})

	// Setup the inference object to be invalidated
	inferenceId := "double-jeopardy-inference"
	k.SetInference(ctx, types.Inference{
		Index:       inferenceId,
		InferenceId: inferenceId,
		ExecutedBy:  participantAddrStr,
		RequestedBy: authority,
		Status:      types.InferenceStatus_FINISHED,
		ProposalDetails: &types.ProposalDetails{
			PolicyAddress: authority,
		},
	})

	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any())
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any())
	// Execute the invalidation to trigger the second slash
	_, err = ms.InvalidateInference(ctx, &types.MsgInvalidateInference{
		Creator:     authority,
		InferenceId: inferenceId,
	})
	require.NoError(t, err)

	// Verify the final collateral amount
	finalCollateralCoin, found := ck.GetCollateral(ctx, participantAcc)
	require.True(t, found)

	invalidSlash, err := params.CollateralParams.SlashFractionInvalid.ToLegacyDec()
	require.NoError(t, err)
	expectedFinalAmount := expectedAfterDowntime.ToLegacyDec().Mul(math.LegacyOneDec().Sub(invalidSlash)).TruncateInt()

	require.Equal(t, expectedFinalAmount, finalCollateralCoin.Amount, "Collateral should be slashed again for invalid status")
	// 900 * 0.8 = 720

	// For clarity, check the final value is 720
	require.Equal(t, math.NewInt(720), finalCollateralCoin.Amount)
}
