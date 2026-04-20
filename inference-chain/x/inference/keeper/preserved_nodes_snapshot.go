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

// PreservedNodeSetByModel returns preserved nodes for a model keyed by
// participant_id -> node_id set. HardwareNode.LocalId is only unique within a
// single participant, so lookups must carry the participant context.
func PreservedNodeSetByModel(snapshot *types.PreservedNodesSnapshot, modelId string) map[string]map[string]struct{} {
	byParticipant := make(map[string]map[string]struct{})
	if snapshot == nil {
		return byParticipant
	}
	for _, modelNodes := range snapshot.ModelPreservedNodes {
		if modelNodes.ModelId != modelId {
			continue
		}
		for _, p := range modelNodes.Participants {
			if p == nil {
				continue
			}
			nodeSet, ok := byParticipant[p.ParticipantId]
			if !ok {
				nodeSet = make(map[string]struct{}, len(p.NodeIds))
				byParticipant[p.ParticipantId] = nodeSet
			}
			for _, nodeID := range p.NodeIds {
				nodeSet[nodeID] = struct{}{}
			}
		}
		return byParticipant
	}
	return byParticipant
}

// IsPreservedNode reports whether (participantId, nodeId) is in the set.
// The set is the value returned by PreservedNodeSetByModel.
func IsPreservedNode(set map[string]map[string]struct{}, participantId, nodeId string) bool {
	nodes, ok := set[participantId]
	if !ok {
		return false
	}
	_, ok = nodes[nodeId]
	return ok
}
