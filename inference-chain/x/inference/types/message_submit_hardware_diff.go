package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/utils"
)

var _ sdk.Msg = &MsgSubmitHardwareDiff{}

func NewMsgSubmitHardwareDiff(creator string) *MsgSubmitHardwareDiff {
	return &MsgSubmitHardwareDiff{
		Creator: creator,
	}
}

func (msg *MsgSubmitHardwareDiff) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Creator)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	if len(msg.Removed) > MaxRemoved {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "removed has more than %d elements", MaxRemoved)
	}
	if len(msg.NewOrModified) > MaxNewOrModified {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "newOrModified has more than %d elements", MaxNewOrModified)
	}
	for _, node := range msg.Removed {
		if node == nil {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "removed contains nil hardware node")
		}
		if err := node.ValidateBasic(); err != nil {
			return err
		}
	}
	for _, node := range msg.NewOrModified {
		if node == nil {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "newOrModified contains nil hardware node")
		}
		if err := node.ValidateBasic(); err != nil {
			return err
		}
	}
	return nil
}

func (node *HardwareNode) ValidateBasic() error {
	if node == nil {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "node is nil")
	}
	err := utils.ValidateNodeId(node.LocalId)
	if err != nil {
		return err
	}
	if len(node.Models) > MaxModelsPerNode {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "node %s has more than %d models", node.LocalId, MaxModelsPerNode)
	}
	if len(node.Hardware) > MaxHardwarePerNode {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "node %s has more than %d hardware", node.LocalId, MaxHardwarePerNode)
	}
	if len(node.Host) > 256 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "node host is too long")
	}
	if len(node.Port) > 100 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "node port is too long")
	}
	for _, model := range node.Models {
		if len(model) > 256 {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "model name is too long")
		}
	}
	for _, hardware := range node.Hardware {
		if hardware == nil {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "hardware is nil")
		}
		if len(hardware.Type) > 256 {
			return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "hardware type is too long")
		}
	}
	return nil
}

const MaxModelsPerNode = 1000
const MaxHardwarePerNode = 1000
const MaxRemoved = 1000
const MaxNewOrModified = 1000
