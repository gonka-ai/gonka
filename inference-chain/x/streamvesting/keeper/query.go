package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/productscience/inference/x/streamvesting/types"
)

var _ types.QueryServer = Keeper{}

// VestingSchedule queries a participant's full vesting schedule
func (k Keeper) VestingSchedule(goCtx context.Context, req *types.QueryVestingScheduleRequest) (*types.QueryVestingScheduleResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.ParticipantAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "participant address cannot be empty")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	schedule, found := k.GetVestingSchedule(ctx, req.ParticipantAddress)
	if !found {
		emptySchedule := types.VestingSchedule{
			ParticipantAddress: req.ParticipantAddress,
			EpochAmounts:       []types.EpochCoins{},
		}
		return &types.QueryVestingScheduleResponse{
			VestingSchedule: &emptySchedule,
		}, nil
	}

	return &types.QueryVestingScheduleResponse{
		VestingSchedule: &schedule,
	}, nil
}

// TotalVestingAmount queries the total vesting amount for a participant
func (k Keeper) TotalVestingAmount(goCtx context.Context, req *types.QueryTotalVestingAmountRequest) (*types.QueryTotalVestingAmountResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.ParticipantAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "participant address cannot be empty")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	schedule, found := k.GetVestingSchedule(ctx, req.ParticipantAddress)
	if !found {
		return &types.QueryTotalVestingAmountResponse{
			TotalAmount: sdk.NewCoins(),
		}, nil
	}

	// Calculate total vesting amount across all epochs
	totalAmount := sdk.NewCoins()
	for _, epochAmount := range schedule.EpochAmounts {
		totalAmount = totalAmount.Add(epochAmount.Coins...)
	}

	return &types.QueryTotalVestingAmountResponse{
		TotalAmount: totalAmount,
	}, nil
}
