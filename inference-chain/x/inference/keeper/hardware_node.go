package keeper

import (
	"context"
	"fmt"
	"strings"

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

	SetValue(k, ctx, storedHardwareNodes(hardwareNodes), []byte(HardwareNodesKeysPrefix), key)

	return nil
}

// storedHardwareNodes drops the participant the key holds; restoredHardwareNodes fills it back.
// A record with no nodes is stored whole so the value is never empty.
func storedHardwareNodes(h *types.HardwareNodes) *types.HardwareNodes {
	if len(h.HardwareNodes) == 0 {
		return h
	}
	return &types.HardwareNodes{HardwareNodes: h.HardwareNodes}
}

func restoredHardwareNodes(participantId string, h *types.HardwareNodes) *types.HardwareNodes {
	if h.Participant == "" {
		h.Participant = participantId
	}
	return h
}

func (k Keeper) GetHardwareNodes(ctx context.Context, participantId string) (*types.HardwareNodes, bool) {
	key := HardwareNodesKey(participantId)
	hardwareNodes, found := GetValue(&k, ctx, &types.HardwareNodes{}, []byte(HardwareNodesKeysPrefix), key)
	if !found {
		return hardwareNodes, false
	}
	return restoredHardwareNodes(participantId, hardwareNodes), true
}

func (k Keeper) GetAllHardwareNodes(ctx context.Context) ([]*types.HardwareNodes, error) {
	iterator := PrefixStore(ctx, &k, []byte(HardwareNodesKeysPrefix)).Iterator(nil, nil)
	defer iterator.Close()

	var results []*types.HardwareNodes
	for ; iterator.Valid(); iterator.Next() {
		val := &types.HardwareNodes{}
		if err := k.cdc.Unmarshal(iterator.Value(), val); err != nil {
			return nil, fmt.Errorf("failed to unmarshal: %w", err)
		}
		results = append(results, restoredHardwareNodes(strings.TrimSuffix(string(iterator.Key()), "/"), val))
	}
	return results, nil
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
		hardwareNodes = restoredHardwareNodes(participantId, hardwareNodes)
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
