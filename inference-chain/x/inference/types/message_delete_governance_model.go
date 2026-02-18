package types

import (
	"strings"

	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgDeleteGovernanceModel{}

func (msg *MsgDeleteGovernanceModel) ValidateBasic() error {
	_, err := sdk.AccAddressFromBech32(msg.Authority)
	if err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid authority address (%s)", err)
	}
	if strings.TrimSpace(msg.Id) == "" {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "id is required")
	}
	return nil
}
