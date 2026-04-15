package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPreservedNodesSnapshot(ctx context.Context, snapshot types.PreservedNodesSnapshot) error {
	return k.PreservedNodesSnapshots.Set(ctx, snapshot.EpisodeAnchorHeight, snapshot)
}

func (k Keeper) GetPreservedNodesSnapshot(ctx context.Context, episodeAnchorHeight int64) (types.PreservedNodesSnapshot, bool, error) {
	snapshot, err := k.PreservedNodesSnapshots.Get(ctx, episodeAnchorHeight)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PreservedNodesSnapshot{}, false, nil
		}
		return types.PreservedNodesSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func (k Keeper) DeletePreservedNodesSnapshot(ctx context.Context, episodeAnchorHeight int64) error {
	return k.PreservedNodesSnapshots.Remove(ctx, episodeAnchorHeight)
}

func PreservedNodeSetByModel(snapshot *types.PreservedNodesSnapshot, modelId string) map[string]struct{} {
	nodeSet := make(map[string]struct{})
	if snapshot == nil {
		return nodeSet
	}

	for _, modelNodes := range snapshot.ModelPreservedNodes {
		if modelNodes == nil || modelNodes.ModelId != modelId {
			continue
		}
		for _, nodeID := range modelNodes.PreservedNodeIds {
			nodeSet[nodeID] = struct{}{}
		}
		break
	}

	return nodeSet
}

func IsPreservedNode(snapshot *types.PreservedNodesSnapshot, modelId, nodeId string) bool {
	_, ok := PreservedNodeSetByModel(snapshot, modelId)[nodeId]
	return ok
}
