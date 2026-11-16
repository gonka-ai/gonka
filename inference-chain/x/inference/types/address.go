package types

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

// CanonicalAccAddress decodes a bech32 account and rejects any spelling other
// than sdk.AccAddress.String(). AccAddressFromBech32 accepts an all-uppercase
// spelling of the same account; storing that spelling creates a second identity.
func CanonicalAccAddress(bech32 string) (sdk.AccAddress, error) {
	addr, err := sdk.AccAddressFromBech32(bech32)
	if err != nil {
		return nil, err
	}
	if bech32 != addr.String() {
		return nil, sdkerrors.ErrInvalidAddress.Wrap("address is not canonical")
	}
	return addr, nil
}
