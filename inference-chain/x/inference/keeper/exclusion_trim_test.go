package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestExclusion_ValueOmitsKeyFields(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(6399374)
	epochIndex := uint64(415)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: epochIndex, PocStartBlockHeight: 100})
	require.NoError(t, k.SetEffectiveEpochIndex(sdkCtx, epochIndex))
	mocks.ExpectCreateGroupWithPolicyCall(sdkCtx, epochIndex)
	eg, err := k.CreateEpochGroup(sdkCtx, epochIndex, epochIndex)
	require.NoError(t, err)
	require.NoError(t, eg.CreateGroup(sdkCtx))
	params := types.DefaultParams()
	params.ValidationParams.FalsePositiveRate = types.DecimalFromFloat(0.05)
	require.NoError(t, k.SetParams(sdkCtx, params))

	mocks.CollateralKeeper.EXPECT().Slash(gomock.Any(), gomock.Any(), gomock.Any(), types.SlashReasonInvalidation, gomock.Any()).Return(sdk.Coin{}, nil)
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMetadataResponse{}, nil)
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMembersResponse{}, nil)

	addr := testutil.Bech32Addr(1)
	participant := types.Participant{
		Index:                        addr,
		Address:                      addr,
		Status:                       types.ParticipantStatus_ACTIVE,
		ConsecutiveInvalidInferences: 20,
		CurrentEpochStats:            &types.CurrentEpochStats{InvalidLLR: types.DecimalFromFloat(0), InactiveLLR: types.DecimalFromFloat(0)},
	}
	require.NoError(t, k.UpdateParticipantStatus(sdkCtx, &participant))
	require.Equal(t, types.ParticipantStatus_INVALID, participant.Status)

	acc := sdk.MustAccAddressFromBech32(addr)
	key := collections.Join(epochIndex, acc)
	raw, err := k.ExcludedParticipantsMap.Get(sdkCtx, key)
	require.NoError(t, err)
	require.Empty(t, raw.Address)
	require.Zero(t, raw.EpochIndex)
	require.NotEmpty(t, raw.Reason)

	want := types.ExcludedParticipant{Address: addr, EpochIndex: epochIndex, Reason: raw.Reason, ExclusionBlockHeight: 6399374}
	resp, err := k.ExcludedParticipants(sdkCtx, &types.QueryExcludedParticipantsRequest{EpochIndex: epochIndex})
	require.NoError(t, err)
	require.Equal(t, []*types.ExcludedParticipant{&want}, resp.Items)

	// Write cost of the record before (full value) and after (key fields dropped).
	gctx := sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.ExcludedParticipantsMap.Set(gctx, key, want))
	full := gctx.GasMeter().GasConsumed()
	gctx = sdkCtx.WithGasMeter(storetypes.NewInfiniteGasMeter())
	require.NoError(t, k.ExcludedParticipantsMap.Set(gctx, key, raw))
	t.Logf("ExcludedParticipantsMap.Set gas: full %d, trimmed %d", full, gctx.GasMeter().GasConsumed())
}

func TestExclusion_LegacyFullRecord(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	addr := testutil.Bech32Addr(2)
	full := types.ExcludedParticipant{Address: addr, EpochIndex: 9, Reason: "failed_confirmation_poc", ExclusionBlockHeight: 77}
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx, collections.Join(uint64(9), sdk.MustAccAddressFromBech32(addr)), full))

	resp, err := k.ExcludedParticipants(ctx, &types.QueryExcludedParticipantsRequest{EpochIndex: 9})
	require.NoError(t, err)
	require.Equal(t, []*types.ExcludedParticipant{&full}, resp.Items)
}
