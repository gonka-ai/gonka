package v0_2_14

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.14", UpgradeName)
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

