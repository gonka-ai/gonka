package keeper

import (
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const HardwareNodesKeysPrefix = "HardwareNodesValues/value/"

func HardwareNodesFullKey(participantId string) []byte {
	return types.StringKey(HardwareNodesKeysPrefix + participantId)
}

func HardwareNodesKey(participantId string) []byte {
	return types.StringKey(participantId)
}

func (k Keeper) SetHardwareNodes(ctx sdk.Context, participantId string, hardwareNodes *types.HardwareNodes) error {
	for i, hardwareNode := range hardwareNodes.HardwareNodes {
		if hardwareNode.Participant != participantId {
			return fmt.Errorf("hardware node participant id does not match hardware nodes participant id. participantId = %s. participantIdAt%d = %s", participantId, i, hardwareNode.Participant)
		}
	}

	key := HardwareNodesKey(participantId)

	SetValue(k, ctx, hardwareNodes, []byte(HardwareNodesKeysPrefix), key)

	return nil
}

func (k Keeper) GetHardwareNodes(ctx sdk.Context, participantId string) (*types.HardwareNodes, bool) {
	key := HardwareNodesKey(participantId)
	hardwareNodes := types.HardwareNodes{}

	return GetValue(k, ctx, &hardwareNodes, []byte(HardwareNodesKeysPrefix), key)
}
