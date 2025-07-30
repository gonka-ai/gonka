package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

const HardwareNodesKeysPrefix = "HardwareNodesValues/value/"

func HardwareNodesFullKey(participantId string) []byte {
	return types.StringKey(HardwareNodesKeysPrefix + participantId)
}

func HardwareNodesKey(participantId string) []byte {
	return types.StringKey(participantId)
}

func (k Keeper) SetHardwareNodes(ctx context.Context, hardwareNodes *types.HardwareNodes) error {
	key := HardwareNodesKey(hardwareNodes.Participant)

	SetValue(k, ctx, hardwareNodes, []byte(HardwareNodesKeysPrefix), key)

	return nil
}

func (k Keeper) GetHardwareNodes(ctx context.Context, participantId string) (*types.HardwareNodes, bool) {
	key := HardwareNodesKey(participantId)
	hardwareNodes := types.HardwareNodes{}

	return GetValue(&k, ctx, &hardwareNodes, []byte(HardwareNodesKeysPrefix), key)
}

func (k Keeper) GetAllHardwareNodes(ctx context.Context) ([]*types.HardwareNodes, error) {
	return GetAllValues(ctx, &k, []byte(HardwareNodesKeysPrefix), func() *types.HardwareNodes {
		return &types.HardwareNodes{}
	})
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
