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

	inputs, found, err := k.loadBitcoinRewardInputs(ctx, epochIndex)
	if !found {
		return nil, status.Error(codes.NotFound, "active participants not found for epoch")
	}
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	amounts, _, err := GetBitcoinSettleAmountsWithTransfers(
		inputs.Participants,
		&inputs.EpochGroupData,
		inputs.Params.BitcoinRewardParams,
		inputs.ValidationParams,
		inputs.SettleParameters,
		inputs.ParticipantMLNodes,
		inputs.RewardTransfers,
		inputs.RewardPenalties,
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
