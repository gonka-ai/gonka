package keeper_test

import (
	"context"
	"strconv"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/testutil/nullify"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Prevent strconv unused error
var _ = strconv.IntSize

func createNParticipant(keeper keeper.Keeper, ctx context.Context, n int) []types.Participant {
	items := make([]types.Participant, n)
	for i := range items {
		items[i].Index = testutil.Bech32Addr(i)
		// To test counter
		items[i].Status = types.ParticipantStatus_ACTIVE
		items[i].CurrentEpochStats = types.NewCurrentEpochStats()
		keeper.SetParticipant(ctx, items[i])
	}
	return items
}

func TestParticipantGet(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNParticipant(keeper, ctx, 10)
	var expectedCounter uint32 = 0
	for _, item := range items {
		rst, found := keeper.GetParticipant(ctx,
			item.Index,
		)
		require.True(t, found)
		require.Equal(t,
			nullify.Fill(&item),
			nullify.Fill(&rst),
		)
		expectedCounter++
	}
}

func TestParticipantRemove(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNParticipant(keeper, ctx, 10)
	for _, item := range items {
		keeper.RemoveParticipant(ctx,
			item.Index,
		)
		_, found := keeper.GetParticipant(ctx,
			item.Index,
		)
		require.False(t, found)
	}
}

func TestParticipantGetAll(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)
	items := createNParticipant(keeper, ctx, 1000)
	require.ElementsMatch(t,
		nullify.Fill(items),
		nullify.Fill(keeper.GetAllParticipant(ctx)),
	)
}

func TestUpdateParticipantStatus_NoTransition(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Set params with validation parameters
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	params.ValidationParams.DowntimeReputationPreserve = types.DecimalFromFloat(0.8)
	params.ValidationParams.InvalidReputationPreserve = types.DecimalFromFloat(0.5)
	k.SetParams(ctx, params)

	// Create participant with ACTIVE status
	participant := types.Participant{
		Index:   testutil.Bech32Addr(1),
		Address: testutil.Bech32Addr(1),
		Status:  types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			ValidatedInferences:   95,
			InvalidatedInferences: 5,
			InferenceCount:        100,
			MissedRequests:        5,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 10,
	}

	// Call UpdateParticipantStatus
	err := k.UpdateParticipantStatus(ctx, &participant)
	require.NoError(t, err)

	// Status should remain ACTIVE
	require.Equal(t, types.ParticipantStatus_ACTIVE, participant.Status)
	// EpochsCompleted should not change
	require.Equal(t, uint32(10), participant.EpochsCompleted)
}

func TestUpdateParticipantStatus_TransitionToInvalid(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Setup epoch
	epochIndex := uint64(1)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 100})
	k.SetEffectiveEpochIndex(sdkCtx, epochIndex)
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochIndex)
	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	require.NoError(t, err)
	err = eg.CreateGroup(ctx)
	require.NoError(t, err)

	// Set params with validation parameters
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	params.ValidationParams.InvalidReputationPreserve = types.DecimalFromFloat(0.5)
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.1)
	k.SetParams(ctx, params)

	// Create participant with too many consecutive failures
	participant := types.Participant{
		Index:                        testutil.Bech32Addr(1),
		Address:                      testutil.Bech32Addr(1),
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 20, // This will trigger INVALID
		CurrentEpochStats: &types.CurrentEpochStats{
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 10,
	}

	// Expect slashing to be called
	mocks.CollateralKeeper.EXPECT().
		Slash(ctx, gomock.Any(), gomock.Any(), types.SlashReasonInvalidation).
		Return(sdk.Coin{}, nil).
		Times(1)

	// Expect UpdateGroupMetadata and UpdateGroupMembers to be called when removing from epoch group
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMetadata(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMetadataResponse{}, nil).
		Times(1)
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMembers(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMembersResponse{}, nil).
		Times(1)

	// Call UpdateParticipantStatus
	err = k.UpdateParticipantStatus(ctx, &participant)
	require.NoError(t, err)

	// Status should transition to INVALID
	require.Equal(t, types.ParticipantStatus_INVALID, participant.Status)
	// EpochsCompleted should be reduced by InvalidReputationPreserve (10 * 0.5 = 5)
	require.Equal(t, uint32(5), participant.EpochsCompleted)
}

func TestUpdateParticipantStatus_AlreadyInvalid(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set params
	params := types.DefaultParams()
	k.SetParams(ctx, params)

	// Create participant already INVALID
	participant := types.Participant{
		Index:                        testutil.Bech32Addr(1),
		Address:                      testutil.Bech32Addr(1),
		Status:                       types.ParticipantStatus_INVALID,
		ConsecutiveInvalidInferences: 20,
		CurrentEpochStats: &types.CurrentEpochStats{
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 5,
	}

	// No slashing should be called (already INVALID)
	mocks.CollateralKeeper.EXPECT().
		Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	// Call UpdateParticipantStatus
	err := k.UpdateParticipantStatus(ctx, &participant)
	require.NoError(t, err)

	// Status should remain INVALID
	require.Equal(t, types.ParticipantStatus_INVALID, participant.Status)
	// EpochsCompleted should not change (no duplicate side effects)
	require.Equal(t, uint32(5), participant.EpochsCompleted)
}

func TestUpdateParticipantStatus_AlreadyInactive(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set params
	params := types.DefaultParams()
	k.SetParams(ctx, params)

	// Create participant already INACTIVE
	participant := types.Participant{
		Index:   testutil.Bech32Addr(1),
		Address: testutil.Bech32Addr(1),
		Status:  types.ParticipantStatus_INACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount:        50,
			MissedRequests:        60,
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 8,
	}

	// No slashing should be called (already INACTIVE)
	mocks.CollateralKeeper.EXPECT().
		Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	// Call UpdateParticipantStatus
	err := k.UpdateParticipantStatus(ctx, &participant)
	require.NoError(t, err)

	// Status should remain INACTIVE
	require.Equal(t, types.ParticipantStatus_INACTIVE, participant.Status)
	// EpochsCompleted should not change (no duplicate side effects)
	require.Equal(t, uint32(8), participant.EpochsCompleted)
}

func TestInvalidParticipant_ReputationReduced(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Setup epoch
	epochIndex := uint64(1)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 100})
	k.SetEffectiveEpochIndex(sdkCtx, epochIndex)
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochIndex)
	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	require.NoError(t, err)
	err = eg.CreateGroup(ctx)
	require.NoError(t, err)

	// Set params with reputation preservation
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	params.ValidationParams.InvalidReputationPreserve = types.DecimalFromFloat(0.6) // 60% preserved
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.1)
	k.SetParams(ctx, params)

	participant := types.Participant{
		Index:                        testutil.Bech32Addr(1),
		Address:                      testutil.Bech32Addr(1),
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 20, // Triggers INVALID
		EpochsCompleted:              50, // 50 completed epochs
		CurrentEpochStats: &types.CurrentEpochStats{
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
	}

	// Mock slashing
	mocks.CollateralKeeper.EXPECT().
		Slash(ctx, gomock.Any(), gomock.Any(), types.SlashReasonInvalidation).
		Return(sdk.Coin{}, nil).
		Times(1)

	// Expect UpdateGroupMetadata and UpdateGroupMembers to be called when removing from epoch group
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMetadata(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMetadataResponse{}, nil).
		Times(1)
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMembers(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMembersResponse{}, nil).
		Times(1)

	// Call UpdateParticipantStatus (which calls invalidateParticipant)
	err = k.UpdateParticipantStatus(ctx, &participant)
	require.NoError(t, err)

	// EpochsCompleted should be reduced: 50 * 0.6 = 30
	require.Equal(t, uint32(30), participant.EpochsCompleted)
	require.Equal(t, types.ParticipantStatus_INVALID, participant.Status)
}

func TestParticipantStatusFlow_ActiveToInvalid(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Setup epoch
	epochIndex := uint64(1)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 100})
	k.SetEffectiveEpochIndex(sdkCtx, epochIndex)
	mocks.ExpectCreateGroupWithPolicyCall(ctx, epochIndex)
	eg, err := k.CreateEpochGroup(ctx, epochIndex, epochIndex)
	require.NoError(t, err)
	err = eg.CreateGroup(ctx)
	require.NoError(t, err)

	// Set params
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	params.ValidationParams.InvalidReputationPreserve = types.DecimalFromFloat(0.5)
	params.CollateralParams.SlashFractionInvalid = types.DecimalFromFloat(0.1)
	k.SetParams(ctx, params)

	// Create participant with consecutive failures
	participant := types.Participant{
		Index:                        testutil.Bech32Addr(1),
		Address:                      testutil.Bech32Addr(1),
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 20, // Triggers INVALID
		CurrentEpochStats: &types.CurrentEpochStats{
			ValidatedInferences:   0,
			InvalidatedInferences: 0,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 30,
	}

	// Mock slashing
	mocks.CollateralKeeper.EXPECT().
		Slash(ctx, gomock.Any(), gomock.Any(), types.SlashReasonInvalidation).
		Return(sdk.Coin{}, nil).
		Times(1)

	// Expect UpdateGroupMetadata and UpdateGroupMembers to be called when removing from epoch group
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMetadata(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMetadataResponse{}, nil).
		Times(1)
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMembers(ctx, gomock.Any()).
		Return(&group.MsgUpdateGroupMembersResponse{}, nil).
		Times(1)

	// Save participant (triggers status update)
	err = k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	// Retrieve and verify
	saved, found := k.GetParticipant(ctx, participant.Address)
	require.True(t, found)
	require.Equal(t, types.ParticipantStatus_INVALID, saved.Status)
	require.Equal(t, uint32(15), saved.EpochsCompleted) // 30 * 0.5 = 15
}

func TestParticipantStatusFlow_InactiveStaysInactive(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)

	// Set params
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	k.SetParams(ctx, params)

	// Create participant already INACTIVE
	participant := types.Participant{
		Index:   testutil.Bech32Addr(1),
		Address: testutil.Bech32Addr(1),
		Status:  types.ParticipantStatus_INACTIVE,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount:        100,
			MissedRequests:        5, // Good stats
			ValidatedInferences:   95,
			InvalidatedInferences: 5,
			InvalidLLR:            types.DecimalFromFloat(0),
			InactiveLLR:           types.DecimalFromFloat(0),
		},
		EpochsCompleted: 8,
	}

	// No slashing expected (already INACTIVE)
	mocks.CollateralKeeper.EXPECT().
		Slash(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Times(0)

	// Save participant
	err := k.SetParticipant(ctx, participant)
	require.NoError(t, err)

	// Retrieve and verify - should remain INACTIVE despite good stats
	saved, found := k.GetParticipant(ctx, participant.Address)
	require.True(t, found)
	require.Equal(t, types.ParticipantStatus_INACTIVE, saved.Status)
	require.Equal(t, uint32(8), saved.EpochsCompleted) // Unchanged
}
