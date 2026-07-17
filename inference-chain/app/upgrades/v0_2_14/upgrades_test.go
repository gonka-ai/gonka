package v0_2_14

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	authtypes "github.com/cosmos/cosmos-sdk/x/auth/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.14", UpgradeName)
}

func TestBurnFeeCollectorBalance(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	burnCoins := sdk.NewCoins(sdk.NewInt64Coin(inferencetypes.BaseCoin, 12_345))
	balance := sdk.NewCoins(
		sdk.NewInt64Coin(inferencetypes.BaseCoin, 12_345),
		sdk.NewInt64Coin("uusdc", 99),
	)
	feeCollectorAddress := authtypes.NewModuleAddress(authtypes.FeeCollectorName)
	const memo = "v0.2.14: burn erroneously minted inflation"

	mocks.BankViewKeeper.EXPECT().
		GetAllBalances(gomock.Any(), feeCollectorAddress).
		Return(balance)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToModule(gomock.Any(), authtypes.FeeCollectorName, inferencetypes.ModuleName, burnCoins, memo).
		Return(nil)
	mocks.BankKeeper.EXPECT().
		BurnCoins(gomock.Any(), inferencetypes.ModuleName, burnCoins, memo).
		Return(nil)

	require.NoError(t, burnFeeCollectorBalance(ctx, k))
}

func TestBurnFeeCollectorBalance_NoBaseDenomBalanceIsNoOp(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	feeCollectorAddress := authtypes.NewModuleAddress(authtypes.FeeCollectorName)

	mocks.BankViewKeeper.EXPECT().
		GetAllBalances(gomock.Any(), feeCollectorAddress).
		Return(sdk.NewCoins(sdk.NewInt64Coin("uusdc", 99)))

	require.NoError(t, burnFeeCollectorBalance(ctx, k))
}

func TestSetDevshardAllowedCreatorAddressesAddsDahl(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = inferencetypes.DefaultDevshardEscrowParams()
	params.DevshardEscrowParams.AllowedCreatorAddresses = []string{"gonka1existing"}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, setDevshardAllowedCreatorAddresses(ctx, k))
	require.NoError(t, setDevshardAllowedCreatorAddresses(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{
		"gonka1existing",
		"gonka1t9akhsrqjkavh68c7cannlfdj58y25vsewfflt",
	}, got.DevshardEscrowParams.AllowedCreatorAddresses)
}

func TestBackfillDevshardEscrowParamDefaults_DefaultInferenceSealGraceNonces(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	params.DevshardEscrowParams.DefaultInferenceSealGraceNonces = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillDevshardEscrowParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.DevshardEscrowParams)
	expected := inferencetypes.DefaultDevshardInferenceSealGraceNonces(got.DevshardEscrowParams.GroupSize)
	require.Equal(t, expected, got.DevshardEscrowParams.DefaultInferenceSealGraceNonces)
	require.Equal(t, inferencetypes.DefaultDevshardInferenceSealGraceSeconds, got.DevshardEscrowParams.DefaultInferenceSealGraceSeconds)
}

func TestBackfillDevshardEscrowParamDefaults_DefaultInferenceSealGraceSeconds(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	params.DevshardEscrowParams.DefaultInferenceSealGraceSeconds = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillDevshardEscrowParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultDevshardInferenceSealGraceSeconds, got.DevshardEscrowParams.DefaultInferenceSealGraceSeconds)
}

func TestBackfillDevshardEscrowParamDefaults_Phase4Fields(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	params.DevshardEscrowParams.CreateDevshardFee = 0
	params.DevshardEscrowParams.FeePerNonce = 0
	params.DevshardEscrowParams.RefusalTimeout = 0
	params.DevshardEscrowParams.ExecutionTimeout = 0
	params.DevshardEscrowParams.ValidationRate = 0
	params.DevshardEscrowParams.VoteThresholdFactor = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillDevshardEscrowParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultDevshardCreateDevshardFee, got.DevshardEscrowParams.CreateDevshardFee)
	require.Equal(t, inferencetypes.DefaultDevshardFeePerNonce, got.DevshardEscrowParams.FeePerNonce)
	require.Equal(t, inferencetypes.DefaultDevshardRefusalTimeout, got.DevshardEscrowParams.RefusalTimeout)
	require.Equal(t, inferencetypes.DefaultDevshardExecutionTimeout, got.DevshardEscrowParams.ExecutionTimeout)
	require.Equal(t, inferencetypes.DefaultDevshardValidationRate, got.DevshardEscrowParams.ValidationRate)
	require.Equal(t, inferencetypes.DefaultDevshardVoteThresholdFactor, got.DevshardEscrowParams.VoteThresholdFactor)
	require.NoError(t, got.DevshardEscrowParams.Validate())
}

func TestBackfillDevshardEscrowParamDefaults_PreservesExistingPhase4Fields(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	params.DevshardEscrowParams.CreateDevshardFee = 42_000
	params.DevshardEscrowParams.FeePerNonce = 7
	params.DevshardEscrowParams.RefusalTimeout = 123
	params.DevshardEscrowParams.ExecutionTimeout = 4567
	params.DevshardEscrowParams.ValidationRate = 8800
	params.DevshardEscrowParams.VoteThresholdFactor = 77
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillDevshardEscrowParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(42_000), got.DevshardEscrowParams.CreateDevshardFee)
	require.Equal(t, uint64(7), got.DevshardEscrowParams.FeePerNonce)
	require.Equal(t, int64(123), got.DevshardEscrowParams.RefusalTimeout)
	require.Equal(t, int64(4567), got.DevshardEscrowParams.ExecutionTimeout)
	require.Equal(t, uint32(8800), got.DevshardEscrowParams.ValidationRate)
	require.Equal(t, uint32(77), got.DevshardEscrowParams.VoteThresholdFactor)
}

func TestBackfillDevshardEscrowFees(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	params.DevshardEscrowParams.CreateDevshardFee = 9_000
	params.DevshardEscrowParams.FeePerNonce = 250
	require.NoError(t, k.SetParams(ctx, params))

	legacy := &inferencetypes.DevshardEscrow{
		Creator: "gonka1legacy",
		Amount:  100,
		Slots:   []string{"s"},
	}
	_, err = k.StoreDevshardEscrow(ctx, legacy, 1)
	require.NoError(t, err)

	partial := &inferencetypes.DevshardEscrow{
		Creator:           "gonka1partial",
		Amount:            200,
		Slots:             []string{"s"},
		CreateDevshardFee: 1_111,
	}
	_, err = k.StoreDevshardEscrow(ctx, partial, 2)
	require.NoError(t, err)

	fresh := &inferencetypes.DevshardEscrow{
		Creator:           "gonka1fresh",
		Amount:            300,
		Slots:             []string{"s"},
		CreateDevshardFee: 5_000,
		FeePerNonce:       500,
	}
	_, err = k.StoreDevshardEscrow(ctx, fresh, 3)
	require.NoError(t, err)

	require.NoError(t, backfillDevshardEscrowFees(ctx, k))

	gotLegacy, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
	require.Equal(t, uint64(9_000), gotLegacy.CreateDevshardFee)
	require.Equal(t, uint64(250), gotLegacy.FeePerNonce)

	gotPartial, found := k.GetDevshardEscrow(ctx, 2)
	require.True(t, found)
	require.Equal(t, uint64(1_111), gotPartial.CreateDevshardFee, "non-zero create_devshard_fee must be preserved")
	require.Equal(t, uint64(250), gotPartial.FeePerNonce, "zero fee_per_nonce must be backfilled")

	gotFresh, found := k.GetDevshardEscrow(ctx, 3)
	require.True(t, found)
	require.Equal(t, uint64(5_000), gotFresh.CreateDevshardFee)
	require.Equal(t, uint64(500), gotFresh.FeePerNonce)
}

func TestBackfillDevshardEscrowFees_NoEscrowsIsNoOp(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, backfillDevshardEscrowFees(ctx, k))
}

func TestBackfillDevshardEscrowParamDefaults_PreservesExistingDefaultInferenceSealGraceNonces(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, params.DevshardEscrowParams)
	const customGrace uint32 = 12345
	params.DevshardEscrowParams.DefaultInferenceSealGraceNonces = customGrace
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillDevshardEscrowParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, customGrace, got.DevshardEscrowParams.DefaultInferenceSealGraceNonces)
}

func TestSeedDelegationRewardSnapshotForEffectiveEpoch(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))

	_, found := k.GetDelegationRewardTransferSnapshot(ctx)
	require.False(t, found)

	require.NoError(t, seedDelegationRewardSnapshotForEffectiveEpoch(ctx, k))

	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, uint64(7), snapshot.EpochIndex)
	require.Empty(t, snapshot.Transfers)
	require.Empty(t, snapshot.Penalties)
}

func TestSeedDelegationRewardSnapshotForEffectiveEpoch_DoesNotOverrideExisting(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 8))
	require.NoError(t, k.SetDelegationRewardTransferSnapshot(ctx, inferencetypes.DelegationRewardTransferSnapshot{
		EpochIndex: 8,
		Transfers: []*inferencetypes.DelegationRewardTransfer{
			{
				ModelId: "m",
				From:    "a",
				To:      "b",
				Share:   inferencetypes.DecimalFromFloat(0.1),
			},
		},
		Penalties: []*inferencetypes.DelegationRewardPenalty{
			{
				Participant:     "a",
				PenaltyFraction: inferencetypes.DecimalFromFloat(0.2),
			},
		},
	}))

	require.NoError(t, seedDelegationRewardSnapshotForEffectiveEpoch(ctx, k))

	snapshot, found := k.GetDelegationRewardTransferSnapshot(ctx)
	require.True(t, found)
	require.Equal(t, uint64(8), snapshot.EpochIndex)
	require.Len(t, snapshot.Transfers, 1)
	require.Len(t, snapshot.Penalties, 1)
}

// TestInitMaintenanceParams_NilParams simulates mainnet state: the chain
// upgraded past v0.2.12 before maintenance landed, so MaintenanceParams is
// nil. The init must install defaults with the feature disabled.
func TestInitMaintenanceParams_NilParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.MaintenanceParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, initMaintenanceParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.MaintenanceParams)
	require.False(t, got.MaintenanceParams.MaintenanceEnabled, "maintenance must ship disabled")
	require.Equal(t, inferencetypes.DefaultMaintenanceParams(), got.MaintenanceParams)
	require.NoError(t, got.MaintenanceParams.Validate())
}

// TestInitMaintenanceParams_PreservesExisting ensures chains that
// already carry deliberate maintenance settings (e.g. testnets with the
// feature enabled) are not stomped by the upgrade.
func TestInitMaintenanceParams_PreservesExisting(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	existing := inferencetypes.DefaultMaintenanceParams()
	existing.MaintenanceEnabled = true
	existing.MaintenanceMaxWindowBlocks = 999
	params.MaintenanceParams = existing
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, initMaintenanceParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.MaintenanceParams)
	require.True(t, got.MaintenanceParams.MaintenanceEnabled)
	require.Equal(t, uint64(999), got.MaintenanceParams.MaintenanceMaxWindowBlocks)
}

func TestUpdateGenesisTransferParams(t *testing.T) {
	gtKeeper, ctx := keepertest.GenesistransferKeeper(t)

	// Ensure initial params are default (whitelist disabled, empty list)
	initParams, err := gtKeeper.GetParams(ctx)
	require.NoError(t, err)
	require.False(t, initParams.RestrictToList)
	require.Empty(t, initParams.AllowedAccounts)

	// Run the migration function
	err = updateGenesisTransferParams(ctx, gtKeeper)
	require.NoError(t, err)

	// Verify params were updated correctly
	migratedParams, err := gtKeeper.GetParams(ctx)
	require.NoError(t, err)
	require.True(t, migratedParams.RestrictToList)
	require.Len(t, migratedParams.AllowedAccounts, 43)
}
