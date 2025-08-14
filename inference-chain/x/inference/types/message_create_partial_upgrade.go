package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"strings"
)

var _ sdk.Msg = &MsgCreatePartialUpgrade{}

func NewMsgCreatePartialUpgrade(creator string, height uint64, nodeVersion string, apiBinariesJson string) *MsgCreatePartialUpgrade {
	return &MsgCreatePartialUpgrade{
		Authority:       creator,
		Height:          height,
		NodeVersion:     nodeVersion,
		ApiBinariesJson: apiBinariesJson,
	}
}

func (msg *MsgCreatePartialUpgrade) ValidateBasic() error {
	// signer
	if _, err := sdk.AccAddressFromBech32(msg.Authority); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid creator address (%s)", err)
	}
	// height must be > 0
	if msg.Height == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "height must be > 0")
	}
	// apiBinariesJson required (no schema validation here)
	if strings.TrimSpace(msg.ApiBinariesJson) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "apiBinariesJson is required")
	}
	return nil
}
