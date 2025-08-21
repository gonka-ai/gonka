package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgInvalidateInference{}

func NewMsgInvalidateInference(creator string, inferenceID string) *MsgInvalidateInference {
	return &MsgInvalidateInference{
		Creator:     creator,
		InferenceId: inferenceID,
	}
}

func (msg *MsgInvalidateInference) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Creator); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// required id
	if strings.TrimSpace(msg.InferenceId) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "inference_id is required")
	}
	return nil
}
