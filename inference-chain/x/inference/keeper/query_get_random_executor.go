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
		k.LogError("Received nil request", types.EpochGroup)
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	k.LogDebug("GetRandomExecutor: Starting executor selection", types.EpochGroup,
		"model_id", req.Model)

	filterFn, err := k.createFilterFn(goCtx, req.Model)
	if err != nil {
		k.LogError("GetRandomExecutor: failed to create filter function", types.EpochGroup,
			"model_id", req.Model, "error", err.Error())
		return nil, err
	}

	// Wrap filter to exclude participants in active maintenance windows.
	// Maintenance-covered participants remain in epoch groups but must not
	// receive new inference assignments during their maintenance window.
	originalFilter := filterFn
	filterFn = func(members []*group.GroupMember) []*group.GroupMember {
		filtered := originalFilter(members)
		return k.filterOutMaintenanceParticipants(goCtx, filtered)
	}

	epochGroup, err := k.GetCurrentEpochGroup(goCtx)
	if err != nil {
		k.LogError("GetRandomExecutor: failed to get current epoch group", types.EpochGroup,
			"model_id", req.Model, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.LogDebug("GetRandomExecutor: Retrieved epoch group", types.EpochGroup,
		"model_id", req.Model, "epoch_id", epochGroup.GroupData.EpochIndex)

	modelFound := false
	for _, m := range epochGroup.GroupData.GetSubGroupModels() {
		if m == req.Model {
			modelFound = true
			break
		}
	}
	if !modelFound {
		return nil, status.Error(codes.NotFound, fmt.Sprintf("model %s not registered", req.Model))
	}

	participant, err := epochGroup.GetRandomMemberForModel(goCtx, req.Model, filterFn)
	if err != nil {
		k.LogError("GetRandomExecutor: failed to get random member", types.EpochGroup,
			"model_id", req.Model, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.LogDebug("GetRandomExecutor: Selected participant", types.EpochGroup,
		"model_id", req.Model, "participant_address", participant.Address)

	return &types.QueryGetRandomExecutorResponse{
		Executor: *participant,
	}, nil
}

func (k Keeper) createFilterFn(goCtx context.Context, modelId string) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	sdkCtx := sdk.UnwrapSDKContext(goCtx)

	k.LogDebug("GetRandomExecutor: createFilterFn: Starting filter creation", types.EpochGroup,
		"model_id", modelId, "block_height", sdkCtx.BlockHeight())

	effectiveEpoch, found := k.GetEffectiveEpoch(goCtx)
	if !found || effectiveEpoch == nil {
		k.LogError("GetRandomExecutor: createFilterFn: no effective epoch found", types.EpochGroup,
			"model_id", modelId)
		return nil, status.Error(codes.Unavailable, "GetRandomExecutor: no effective epoch found")
	}

	epochParams, err := k.GetParams(goCtx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	if epochParams.EpochParams == nil {
		k.LogError("GetRandomExecutor: createFilterFn: epoch params are nil", types.EpochGroup,
			"model_id", modelId, "epoch_index", effectiveEpoch.Index)
		return nil, status.Error(codes.Unavailable, "GetRandomExecutor: epoch params are nil")
	}

	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*effectiveEpoch, *epochParams.EpochParams, sdkCtx.BlockHeight())
	if err != nil {
		k.LogError("GetRandomExecutor: createFilterFn: failed to create epoch context", types.EpochGroup,
			"model_id", modelId, "epoch_index", effectiveEpoch.Index, "error", err.Error())
		return nil, status.Error(codes.Internal, err.Error())
	}
	currentPhase := epochContext.GetCurrentPhase(sdkCtx.BlockHeight())

	k.LogDebug("GetRandomExecutor: createFilterFn: Determined current phase", types.EpochGroup,
		"model_id", modelId, "current_phase", string(currentPhase),
		"epoch_index", effectiveEpoch.Index, "latest_epoch_index", epochContext.EpochIndex,
		"block_height", sdkCtx.BlockHeight(), "set_new_validators_block_height", epochContext.SetNewValidators())

	_, isActive, err := k.GetActiveConfirmationPoCEvent(goCtx)
	if err != nil {
		k.LogError("GetRandomExecutor: createFilterFn: failed to check confirmation PoC", types.EpochGroup,
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

func (k Keeper) createIsAvailableDuringPoCFilterFn(
	ctx context.Context,
	epochId uint64,
	modelId string,
) (func(members []*group.GroupMember) []*group.GroupMember, error) {
	activeParticipants, found := k.GetActiveParticipants(ctx, epochId)
	if !found {
		return nil, status.Error(codes.Unavailable,
			fmt.Sprintf("GetRandomExecutor: no active participants for epoch %d", epochId))
	}
	if activeParticipants.Participants == nil {
		return nil, status.Error(codes.Internal, "GetRandomExecutor: active participants list is nil")
	}

	// Missing snapshot collapses the preserved set to empty: no node is routable for
	// inference during PoC, which is the expected steady state.
	preservedSnapshot, snapshotFound, err := k.GetPreservedNodesSnapshot(ctx)
	if err != nil {
		k.Logger().Warn("GetRandomExecutor: failed to read preserved snapshot, using empty set",
			"epoch_id", epochId, "model_id", modelId, "error", err)
	}
	var preservedNodeSet map[string]map[string]struct{}
	if snapshotFound {
		preservedNodeSet = PreservedNodeSetByModel(&preservedSnapshot, modelId)
	}

	isAvailableDuringPoc := make(map[string]bool)
	for _, participant := range activeParticipants.Participants {
		if participant == nil {
			continue
		}
		modelIndex := -1
		for i, m := range participant.Models {
			if m == modelId {
				modelIndex = i
				break
			}
		}
		if modelIndex < 0 || modelIndex >= len(participant.MlNodes) {
			continue
		}
		modelMLNodes := participant.MlNodes[modelIndex]
		if modelMLNodes == nil {
			continue
		}
		for _, node := range modelMLNodes.MlNodes {
			if node == nil {
				continue
			}
			if IsPreservedNode(preservedNodeSet, participant.Index, node.NodeId) {
				isAvailableDuringPoc[participant.Index] = true
				break
			}
		}
	}

	k.Logger().Info("GetRandomExecutor: PoC filter built",
		"epoch_id", epochId, "model_id", modelId,
		"participants", len(activeParticipants.Participants),
		"available", len(isAvailableDuringPoc))

	return func(members []*group.GroupMember) []*group.GroupMember {
		filtered := make([]*group.GroupMember, 0, len(members))
		for _, member := range members {
			if member == nil || member.Member == nil {
				continue
			}
			if isAvailableDuringPoc[member.Member.Address] {
				filtered = append(filtered, member)
			}
		}
		return filtered
	}, nil
}
