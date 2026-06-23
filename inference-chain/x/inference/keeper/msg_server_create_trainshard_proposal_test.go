package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/testutil/sample"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func makeCreateTrainshardProposalMsg(authority string) *types.MsgCreateTrainshardProposal {
	return &types.MsgCreateTrainshardProposal{
		Authority:         authority,
		Creator:           sample.AccAddress(),
		GpuProfileId:      "NVIDIA H100 x8",
		MaxNodes:          1,
		MaxDurationBlocks: types.DefaultTrainingMinDurationBlocks,
	}
}

func TestMsgServer_CreateTrainshardProposal_Permissions(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	wctx := sdk.UnwrapSDKContext(ctx)

	bad := &types.MsgCreateTrainshardProposal{Authority: testutil.Creator}
	err := keeper.CheckPermission(ms, wctx, bad, keeper.GovernancePermission)
	require.Error(t, err)

	ok := &types.MsgCreateTrainshardProposal{Authority: k.GetAuthority()}
	err = keeper.CheckPermission(ms, wctx, ok, keeper.GovernancePermission)
	require.NoError(t, err)
}

func TestMsgServer_CreateTrainshardProposal_CreatesOpenProposalAndIncrementsCounter(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	msg1 := makeCreateTrainshardProposalMsg(k.GetAuthority())
	resp1, err := ms.CreateTrainshardProposal(ctx, msg1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), resp1.ProposalId)

	p1, err := k.TrainshardProposals.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), p1.Id)
	require.Equal(t, msg1.Creator, p1.Creator)
	require.Equal(t, msg1.GpuProfileId, p1.GpuProfileId)
	require.Equal(t, msg1.MaxNodes, p1.MaxNodes)
	require.Equal(t, msg1.MaxDurationBlocks, p1.MaxDurationBlocks)
	require.Equal(t, types.TrainshardProposalStatus_TRAINSHARD_PROPOSAL_STATUS_OPEN, p1.Status)

	counter, err := k.TrainshardProposalCounter.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(1), counter)

	msg2 := makeCreateTrainshardProposalMsg(k.GetAuthority())
	msg2.Creator = sample.AccAddress()
	resp2, err := ms.CreateTrainshardProposal(ctx, msg2)
	require.NoError(t, err)
	require.Equal(t, uint64(2), resp2.ProposalId)

	counter, err = k.TrainshardProposalCounter.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, uint64(2), counter)
}

func TestMsgServer_CreateTrainshardProposal_RejectsInvalidCreator(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	msg := makeCreateTrainshardProposalMsg(k.GetAuthority())
	msg.Creator = "not-a-bech32-address"

	_, err := ms.CreateTrainshardProposal(ctx, msg)
	require.ErrorIs(t, err, types.ErrInvalidAddress)
}

func TestMsgServer_CreateTrainshardProposal_RejectsLimitsAndDisallowedProfile(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams.AllowedGpuProfileIds = []string{"NVIDIA A100 x8"}
	require.NoError(t, k.SetParams(ctx, params))

	msgProfile := makeCreateTrainshardProposalMsg(k.GetAuthority())
	msgProfile.GpuProfileId = "NVIDIA H100 x8"
	_, err = ms.CreateTrainshardProposal(ctx, msgProfile)
	require.ErrorIs(t, err, types.ErrTrainshardProfileNotAllowed)

	msgNodes := makeCreateTrainshardProposalMsg(k.GetAuthority())
	msgNodes.GpuProfileId = "NVIDIA A100 x8"
	msgNodes.MaxNodes = 0
	_, err = ms.CreateTrainshardProposal(ctx, msgNodes)
	require.ErrorIs(t, err, types.ErrTrainshardProposalLimits)

	msgDuration := makeCreateTrainshardProposalMsg(k.GetAuthority())
	msgDuration.GpuProfileId = "NVIDIA A100 x8"
	msgDuration.MaxDurationBlocks = params.TrainingParams.MinDurationBlocks - 1
	_, err = ms.CreateTrainshardProposal(ctx, msgDuration)
	require.ErrorIs(t, err, types.ErrTrainshardProposalLimits)
}
