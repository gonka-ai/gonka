package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetCurrentEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	effectiveEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return nil, types.ErrEffectiveEpochNotFound
	}

	return k.GetEpochGroup(ctx, effectiveEpochIndex, "")
}

func (k Keeper) GetUpcomingEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	upcomingEpochIndex, found := k.GetUpcomingEpochIndex(ctx)
	if !found {
		return nil, types.ErrUpcomingEpochNotFound
	}

	return k.GetEpochGroup(ctx, upcomingEpochIndex, "")
}

func (k Keeper) GetPreviousEpochGroup(ctx context.Context) (*epochgroup.EpochGroup, error) {
	previousEpochIndex, found := k.GetPreviousEpochIndex(ctx)
	if !found {
		return nil, types.ErrPreviousEpochNotFound
	}

	return k.GetEpochGroup(ctx, previousEpochIndex, "")
}

func (k Keeper) GetEpochGroupForEpoch(ctx context.Context, epoch types.Epoch) (*epochgroup.EpochGroup, error) {
	return k.GetEpochGroup(ctx, epoch.Index, "")
}

func (k Keeper) GetEpochGroup(ctx context.Context, epochIndex uint64, modelId string) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, epochIndex, modelId)
	if !found {
		return nil, types.ErrEpochGroupDataNotFound
	}

	return k.epochGroupFromData(data), nil
}

func (k Keeper) CreateEpochGroup(ctx context.Context, pocStartHeight uint64, epochIndex uint64) (*epochgroup.EpochGroup, error) {
	data, found := k.GetEpochGroupData(ctx, epochIndex, "")
	if found {
		k.LogError("CreateEpochGroup: Root epoch group data already exists", types.EpochGroup, "epochIndex", epochIndex)
		return nil, types.ErrEpochGroupDataAlreadyExists
	} else {
		data = types.EpochGroupData{
			PocStartBlockHeight: pocStartHeight,
			ModelId:             "",
			EpochIndex:          epochIndex,
		}
		k.SetEpochGroupData(ctx, data)
	}

	return k.epochGroupFromData(data), nil
}

// GetSubGroupDataWithLiveMembers returns the EpochGroupData for a model subgroup
// together with the set of live SDK-group members for that subgroup, in a single pass.
func (k Keeper) GetSubGroupDataWithLiveMembers(ctx context.Context, modelId string) (types.EpochGroupData, map[string]bool, error) {
	currentGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return types.EpochGroupData{}, nil, err
	}
	subGroup, err := currentGroup.GetSubGroup(ctx, modelId)
	if err != nil {
		return types.EpochGroupData{}, nil, err
	}
	members, err := subGroup.GetGroupMembers(ctx)
	if err != nil {
		return types.EpochGroupData{}, nil, err
	}
	liveSet := make(map[string]bool, len(members))
	for _, m := range members {
		liveSet[m.Member.Address] = true
	}
	return *subGroup.GroupData, liveSet, nil
}

func (k Keeper) epochGroupFromData(data types.EpochGroupData) *epochgroup.EpochGroup {
	return epochgroup.NewEpochGroup(
		k.group,
		k,
		k,
		k,
		k.GetAuthority(),
		k,
		k,
		&data,
	)
}
