package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

const HardwareNodesKeysPrefix = "HardwareNodesValues/value/"

// HardwareNodeStatusKeysPrefix holds a participant's node statuses apart from
// the inventory blob. Status is what changes on almost every diff, so a
// status-only diff rewrites a few bytes per node instead of the whole blob.
// When present, it overrides the statuses stored in the blob.
const HardwareNodeStatusKeysPrefix = "HardwareNodesStatus/value/"

func HardwareNodesFullKey(participantId string) []byte {
	return types.StringKey(HardwareNodesKeysPrefix + participantId)
}

func HardwareNodesKey(participantId string) []byte {
	return types.StringKey(participantId)
}

func (k Keeper) SetHardwareNodes(ctx context.Context, hardwareNodes *types.HardwareNodes) error {
	key := HardwareNodesKey(hardwareNodes.Participant)

	SetValue(k, ctx, hardwareNodes, []byte(HardwareNodesKeysPrefix), key)
	// The blob now carries the current statuses.
	DeleteValue(&k, ctx, []byte(HardwareNodeStatusKeysPrefix), key)

	return nil
}

// SetHardwareNodesStatuses stores only the statuses of hardwareNodes. Use it
// when nothing but statuses differs from the stored inventory.
func (k Keeper) SetHardwareNodesStatuses(ctx context.Context, hardwareNodes *types.HardwareNodes) error {
	statuses := &types.HardwareNodes{HardwareNodes: make([]*types.HardwareNode, 0, len(hardwareNodes.HardwareNodes))}
	for _, node := range hardwareNodes.HardwareNodes {
		statuses.HardwareNodes = append(statuses.HardwareNodes, &types.HardwareNode{LocalId: node.LocalId, Status: node.Status})
	}
	return SetValue(k, ctx, statuses, []byte(HardwareNodeStatusKeysPrefix), HardwareNodesKey(hardwareNodes.Participant))
}

func (k Keeper) GetHardwareNodes(ctx context.Context, participantId string) (*types.HardwareNodes, bool) {
	key := HardwareNodesKey(participantId)
	hardwareNodes := types.HardwareNodes{}

	nodes, found := GetValue(&k, ctx, &hardwareNodes, []byte(HardwareNodesKeysPrefix), key)
	if found {
		k.applyHardwareNodeStatuses(ctx, participantId, nodes)
	}
	return nodes, found
}

func (k Keeper) applyHardwareNodeStatuses(ctx context.Context, participantId string, nodes *types.HardwareNodes) {
	statuses, found := GetValue(&k, ctx, &types.HardwareNodes{}, []byte(HardwareNodeStatusKeysPrefix), HardwareNodesKey(participantId))
	if !found {
		return
	}
	byId := make(map[string]types.HardwareNodeStatus, len(statuses.HardwareNodes))
	for _, s := range statuses.HardwareNodes {
		byId[s.LocalId] = s.Status
	}
	for _, node := range nodes.HardwareNodes {
		if status, ok := byId[node.LocalId]; ok {
			node.Status = status
		}
	}
}

func (k Keeper) GetAllHardwareNodes(ctx context.Context) ([]*types.HardwareNodes, error) {
	all, err := GetAllValues(ctx, &k, []byte(HardwareNodesKeysPrefix), func() *types.HardwareNodes {
		return &types.HardwareNodes{}
	})
	if err != nil {
		return nil, err
	}
	for _, nodes := range all {
		k.applyHardwareNodeStatuses(ctx, nodes.Participant, nodes)
	}
	return all, nil
}

func (k Keeper) GetHardwareNodesForParticipants(ctx context.Context, participantIds []string) ([]*types.HardwareNodes, error) {
	result := make([]*types.HardwareNodes, 0, len(participantIds))
	prefixStore := PrefixStore(ctx, &k, []byte(HardwareNodesKeysPrefix))

	for _, participantId := range participantIds {
		value := types.HardwareNodes{}
		hardwareNodes, found := GetValueFromStore(&k, &value, *prefixStore, HardwareNodesKey(participantId))
		if !found {
			hardwareNodes = &types.HardwareNodes{
				Participant:   participantId,
				HardwareNodes: make([]*types.HardwareNode, 0),
			}
		} else {
			k.applyHardwareNodeStatuses(ctx, participantId, hardwareNodes)
		}
		result = append(result, hardwareNodes)
	}

	return result, nil
}

func (k Keeper) GetNodesForModel(ctx context.Context, modelId string) ([]*types.HardwareNode, error) {
	// TODO: Optimize this to only get the nodes for the model from the KV store
	allNodes, err := k.GetAllHardwareNodes(ctx)
	if err != nil {
		return nil, err
	}

	var result []*types.HardwareNode
	for _, participantNodes := range allNodes {
		for _, node := range participantNodes.HardwareNodes {
			for _, model := range node.Models {
				if model == modelId {
					result = append(result, node)
					break
				}
			}
		}
	}

	return result, nil
}
