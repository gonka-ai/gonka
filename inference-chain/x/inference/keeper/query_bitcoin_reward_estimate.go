package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EstimateBitcoinReward(ctx context.Context, req *types.QueryEstimateBitcoinRewardRequest) (*types.QueryEstimateBitcoinRewardResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if _, err := sdk.AccAddressFromBech32(req.Participant); err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid participant address")
	}

	snapshot, snapshotFound := k.GetDelegationRewardTransferSnapshot(ctx)
	if !snapshotFound || snapshot.EpochIndex == 0 {
		return nil, status.Error(codes.NotFound, "delegation reward snapshot not found")
	}
	epochIndex := snapshot.EpochIndex

	activeParticipants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return nil, status.Error(codes.NotFound, "active participants not found for epoch")
	}
	activeParticipantAddresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		activeParticipantAddresses[i] = participant.Index
	}
	allParticipants := k.GetParticipants(ctx, activeParticipantAddresses)

	epochGroupData, found := k.GetEpochGroupData(ctx, epochIndex, "")
	if !found {
		return nil, status.Error(codes.NotFound, "epoch group data not found for epoch")
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	settleParameters, err := k.GetSettleParameters(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	validationParams := params.ValidationParams
	if validationParams == nil {
		validationParams = types.DefaultValidationParams()
	}
	if graceParams, ok := k.GetPunishmentGraceEpoch(ctx, epochIndex); ok && graceParams.BinomTestP0 != nil {
		graceValidationParams := *validationParams
		graceValidationParams.BinomTestP0 = graceParams.BinomTestP0
		validationParams = &graceValidationParams
	}
	participantMLNodes := k.AggregateMLNodesFromModelSubgroups(ctx, epochIndex, epochGroupData.ValidationWeights)
	rewardTransfers, err := k.GetDelegationRewardTransfersForEpoch(ctx, epochIndex)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	rewardPenalties, err := k.GetDelegationRewardPenaltiesForEpoch(ctx, epochIndex)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	reservedNodes := k.CollectEpochReservedNodeWeights(ctx, epochIndex, ReservationScopeReward)

	amounts, _, err := GetBitcoinSettleAmountsWithTransfers(
		allParticipants,
		&epochGroupData,
		params.BitcoinRewardParams,
		validationParams,
		settleParameters,
		participantMLNodes,
		reservedNodes,
		rewardTransfers,
		rewardPenalties,
		k.Logger(),
	)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	for _, amount := range amounts {
		if amount == nil || amount.Settle == nil || amount.Settle.Participant != req.Participant {
			continue
		}
		settleAmount := *amount.Settle
		settleAmount.EpochIndex = epochIndex

		if amount.Error != nil {
			return nil, status.Error(codes.Internal, amount.Error.Error())
		}

		return &types.QueryEstimateBitcoinRewardResponse{
			SettleAmount: settleAmount,
		}, nil
	}

	return nil, status.Error(codes.NotFound, "participant not found in epoch reward estimate")
}
