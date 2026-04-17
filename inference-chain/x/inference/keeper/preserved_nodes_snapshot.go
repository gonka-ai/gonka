package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) SetPreservedNodesSnapshot(ctx context.Context, snapshot types.PreservedNodesSnapshot) error {
	return k.PreservedNodesSnapshotItem.Set(ctx, snapshot)
}

func (k Keeper) GetPreservedNodesSnapshot(ctx context.Context) (types.PreservedNodesSnapshot, bool, error) {
	snapshot, err := k.PreservedNodesSnapshotItem.Get(ctx)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return types.PreservedNodesSnapshot{}, false, nil
		}
		return types.PreservedNodesSnapshot{}, false, err
	}
	return snapshot, true, nil
}

func PreservedNodeSetByModel(snapshot *types.PreservedNodesSnapshot, modelId string) map[string]struct{} {
	nodeSet := make(map[string]struct{})
	if snapshot == nil {
		return nodeSet
	}
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		if modelNodes.ModelId != modelId {
			continue
		}
		for _, nodeID := range modelNodes.PreservedNodeIds {
			nodeSet[nodeID] = struct{}{}
		}
		return nodeSet
	}
	return nodeSet
}
