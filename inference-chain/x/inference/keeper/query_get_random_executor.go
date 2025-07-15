package keeper

import (
	"context"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetRandomExecutor(goCtx context.Context, req *types.QueryGetRandomExecutorRequest) (*types.QueryGetRandomExecutorResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	filterFn, err := k.createFilterFn(goCtx, req.Model)
	if err != nil {
		return nil, err
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	participant, err := epochGroup.GetRandomMemberForModel(goCtx, req.Model, filterFn)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}

func (k Keeper) createFilterFn(goCtx context.Context, modelId string) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	sdkCtx := sdk.UnwrapSDKContext(goCtx)

	effectiveEpoch, found := k.GetEffectiveEpoch(goCtx)
	if !found || effectiveEpoch == nil {
		return nil, status.Error(codes.NotFound, "GetRandomExecutor: no effective epoch found")
	}
	epochParams := k.GetParams(goCtx)
	if epochParams.EpochParams == nil {
		return nil, status.Error(codes.NotFound, "GetRandomExecutor: epoch params are nill")
	}
	epochContext := types.NewEpochContextFromEffectiveEpoch(*effectiveEpoch, *epochParams.EpochParams, sdkCtx.BlockHeight())
	currentPhase := epochContext.GetCurrentPhase(sdkCtx.BlockHeight())
	_ = currentPhase

	if currentPhase == types.InferencePhase {
		// Everyone is expected to be available during the inference phase
		return func(members []*group.GroupMember) []*group.GroupMember {
			return members
		}, nil
	} else {
		return k.createIsAvailableDuringPoCFilterFn(goCtx, effectiveEpoch.Index, modelId)
	}
}

func (k Keeper) createIsAvailableDuringPoCFilterFn(ctx context.Context, epochId uint64, modelId string) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	activeParticipants, found := k.GetActiveParticipants(ctx, epochId)
	if !found {
		msg := fmt.Sprintf("GetRandomExecutor: createIsAvailableDuringPocFilterFn failed, can't find active participants. epochId = %d", epochId)
		return nil, status.Error(codes.NotFound, msg)
	}

	isAvailableDuringPoc := make(map[string]bool)
	for _, participant := range activeParticipants.Participants {
		var participantModelIndex = -1
		for i, model := range participant.Models {
			if model == modelId {
				participantModelIndex = i
				break
			}
		}

		if participantModelIndex == -1 {
			continue
		}

		if len(participant.MlNodes) <= participantModelIndex {
			continue
		}

		for _, node := range participant.MlNodes[participantModelIndex].MlNodes {
			if len(node.TimeslotAllocation) > 1 && node.TimeslotAllocation[1] {
				isAvailableDuringPoc[participant.Index] = true
			}
		}
	}

	return func(members []*group.GroupMember) []*group.GroupMember {
		filtered := make([]*group.GroupMember, 0, len(members))
		for _, member := range members {
			if _, ok := isAvailableDuringPoc[member.Member.Address]; ok {
				filtered = append(filtered, member)
			}
		}
		return filtered
	}, nil
}
