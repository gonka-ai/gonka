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
		k.Logger().Error("GetRandomExecutor: received nil request")
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	k.Logger().Info("GetRandomExecutor: Starting executor selection",
		"model_id", req.Model)

	filterFn, err := k.createFilterFn(goCtx, req.Model)
	if err != nil {
		k.Logger().Error("GetRandomExecutor: failed to create filter function",
			"model_id", req.Model, "error", err.Error())
		return nil, err
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		k.Logger().Error("GetRandomExecutor: failed to get current epoch group",
			"model_id", req.Model, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.Logger().Info("GetRandomExecutor: Retrieved epoch group",
		"model_id", req.Model, "epoch_id", epochGroup.GroupData.EpochIndex)

	participant, err := epochGroup.GetRandomMemberForModel(goCtx, req.Model, filterFn)
	if err != nil {
		k.Logger().Error("GetRandomExecutor: failed to get random member",
			"model_id", req.Model, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.Logger().Info("GetRandomExecutor: Selected participant",
		"model_id", req.Model, "participant_address", participant.Address)

	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}

func (k Keeper) createFilterFn(goCtx context.Context, modelId string) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	sdkCtx := sdk.UnwrapSDKContext(goCtx)

	k.Logger().Info("GetRandomExecutor: createFilterFn: Starting filter creation",
		"model_id", modelId, "block_height", sdkCtx.BlockHeight())

	effectiveEpoch, found := k.GetEffectiveEpoch(goCtx)
	if !found || effectiveEpoch == nil {
		k.Logger().Error("GetRandomExecutor: createFilterFn: no effective epoch found",
			"model_id", modelId)
		return nil, status.Error(codes.NotFound, "GetRandomExecutor: no effective epoch found")
	}

	epochParams := k.GetParams(goCtx)
	if epochParams.EpochParams == nil {
		k.Logger().Error("GetRandomExecutor: createFilterFn: epoch params are nil",
			"model_id", modelId, "epoch_index", effectiveEpoch.Index)
		return nil, status.Error(codes.NotFound, "GetRandomExecutor: epoch params are nill")
	}

	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*effectiveEpoch, *epochParams.EpochParams, sdkCtx.BlockHeight())
	if err != nil {
		k.Logger().Error("GetRandomExecutor: createFilterFn: failed to create epoch context",
			"model_id", modelId, "epoch_index", effectiveEpoch.Index, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}
	currentPhase := epochContext.GetCurrentPhase(sdkCtx.BlockHeight())

	k.Logger().Info("GetRandomExecutor: createFilterFn: Determined current phase",
		"model_id", modelId, "current_phase", string(currentPhase),
		"epoch_index", effectiveEpoch.Index, "latest_epoch_index", epochContext.EpochIndex,
		"block_height", sdkCtx.BlockHeight(), "set_new_validators_block_height", epochContext.SetNewValidators())

	_, isActive, err := k.GetActiveConfirmationPoCEvent(goCtx)
	if err != nil {
		k.Logger().Error("GetRandomExecutor: createFilterFn: failed to check confirmation PoC",
			"model_id", modelId, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	if isActive {
		return k.createIsAvailableDuringPoCFilterFn(goCtx, effectiveEpoch.Index, modelId)
	}

	if currentPhase == types.InferencePhase && sdkCtx.BlockHeight() > epochContext.SetNewValidators() {
		return func(members []*group.GroupMember) []*group.GroupMember {
			return members
		}, nil
	}

	return k.createIsAvailableDuringPoCFilterFn(goCtx, effectiveEpoch.Index, modelId)
}

func (k Keeper) createIsAvailableDuringPoCFilterFn(ctx context.Context, epochId uint64, modelId string) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	k.Logger().Info("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Starting PoC availability filter creation",
		"epoch_id", epochId, "model_id", modelId)

	activeParticipants, found := k.GetActiveParticipants(ctx, epochId)
	if !found {
		msg := fmt.Sprintf("GetRandomExecutor: createIsAvailableDuringPocFilterFn failed, can't find active participants. epochId = %d", epochId)
		k.Logger().Error("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: active participants not found",
			"epoch_id", epochId, "model_id", modelId)
		return nil, status.Error(codes.NotFound, msg)
	}

	if activeParticipants.Participants == nil {
		k.Logger().Error("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: participants list is nil",
			"epoch_id", epochId, "model_id", modelId)
		return nil, status.Error(codes.Internal, "participants list is nil")
	}

	k.Logger().Info("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Found active participants",
		"epoch_id", epochId, "model_id", modelId, "participant_count", len(activeParticipants.Participants))

	isAvailableDuringPoc := make(map[string]bool)
	totalParticipantsChecked := 0
	participantsWithModel := 0
	participantsWithAvailableNodes := 0

	for _, participant := range activeParticipants.Participants {
		totalParticipantsChecked++

		if participant == nil {
			k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: found nil participant",
				"epoch_id", epochId, "model_id", modelId, "participant_index", totalParticipantsChecked-1)
			continue
		}

		k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Processing participant",
			"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
			"participant_models", participant.Models, "ml_nodes_arrays", len(participant.MlNodes))

		// Find the model index
		var participantModelIndex = -1
		for i, model := range participant.Models {
			if model == modelId {
				participantModelIndex = i
				break
			}
		}

		if participantModelIndex == -1 {
			k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: participant doesn't support model",
				"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
				"participant_models", participant.Models)
			continue
		}

		participantsWithModel++
		k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: participant supports model",
			"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
			"model_index", participantModelIndex)

		// Defensive programming: check bounds
		if len(participant.MlNodes) <= participantModelIndex {
			k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: model index out of bounds",
				"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
				"model_index", participantModelIndex, "ml_nodes_length", len(participant.MlNodes))
			continue
		}

		// Defensive programming: check for nil model MLNodes array
		modelMLNodes := participant.MlNodes[participantModelIndex]
		if modelMLNodes == nil {
			k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: model MLNodes array is nil",
				"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
				"model_index", participantModelIndex)
			continue
		}

		if modelMLNodes.MlNodes == nil {
			k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: MlNodes slice is nil",
				"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
				"model_index", participantModelIndex)
			continue
		}

		k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Checking MLNodes for POC_SLOT availability",
			"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
			"ml_nodes_count", len(modelMLNodes.MlNodes))

		nodeCount := 0
		availableNodeCount := 0
		for _, node := range modelMLNodes.MlNodes {
			nodeCount++

			if node == nil {
				k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: found nil MLNode",
					"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
					"node_index", nodeCount-1)
				continue
			}

			k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Checking node timeslot allocation",
				"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
				"node_id", node.NodeId, "timeslot_allocation", node.TimeslotAllocation,
				"timeslot_length", len(node.TimeslotAllocation))

			// Defensive programming: check timeslot allocation bounds and values
			if len(node.TimeslotAllocation) <= 1 {
				k.Logger().Warn("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: invalid timeslot allocation length",
					"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
					"node_id", node.NodeId, "timeslot_allocation", node.TimeslotAllocation,
					"expected_min_length", 2)
				continue
			}

			// Check POC_SLOT availability (index 1)
			if node.TimeslotAllocation[1] {
				availableNodeCount++
				isAvailableDuringPoc[participant.Index] = true
				k.Logger().Info("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Found node available during PoC",
					"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
					"node_id", node.NodeId, "timeslot_allocation", node.TimeslotAllocation)
				// Break after finding first available node for this participant
				break
			}
		}

		if availableNodeCount > 0 {
			participantsWithAvailableNodes++
		}

		k.Logger().Debug("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Participant node analysis complete",
			"epoch_id", epochId, "model_id", modelId, "participant_address", participant.Index,
			"total_nodes", nodeCount, "available_nodes", availableNodeCount,
			"participant_available", isAvailableDuringPoc[participant.Index])
	}

	k.Logger().Info("GetRandomExecutor: createIsAvailableDuringPoCFilterFn: Analysis complete",
		"epoch_id", epochId, "model_id", modelId,
		"total_participants_checked", totalParticipantsChecked,
		"participants_with_model", participantsWithModel,
		"participants_with_available_nodes", participantsWithAvailableNodes,
		"available_participants", len(isAvailableDuringPoc))

	return func(members []*group.GroupMember) []*group.GroupMember {
		k.Logger().Debug("GetRandomExecutor: PoC filter function: Starting member filtering",
			"epoch_id", epochId, "model_id", modelId, "input_member_count", len(members))

		filtered := make([]*group.GroupMember, 0, len(members))
		for _, member := range members {
			if member == nil {
				k.Logger().Warn("GetRandomExecutor: PoC filter function: found nil group member",
					"epoch_id", epochId, "model_id", modelId)
				continue
			}

			if member.Member == nil {
				k.Logger().Warn("GetRandomExecutor: PoC filter function: group member has nil Member field",
					"epoch_id", epochId, "model_id", modelId)
				continue
			}

			if isAvailable, exists := isAvailableDuringPoc[member.Member.Address]; exists && isAvailable {
				filtered = append(filtered, member)
				k.Logger().Debug("GetRandomExecutor: PoC filter function: included member",
					"epoch_id", epochId, "model_id", modelId,
					"member_address", member.Member.Address)
			} else {
				k.Logger().Debug("GetRandomExecutor: PoC filter function: excluded member",
					"epoch_id", epochId, "model_id", modelId,
					"member_address", member.Member.Address, "exists", exists, "available", isAvailable)
			}
		}

		k.Logger().Info("GetRandomExecutor: PoC filter function: Filtering complete",
			"epoch_id", epochId, "model_id", modelId,
			"input_member_count", len(members), "filtered_member_count", len(filtered))

		return filtered
	}, nil
}
