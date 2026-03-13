package types

import (
	errorsmod "cosmossdk.io/errors"
	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

const (
	// MinTransferGonka is the minimum transfer amount in gonka to prevent dust/spam.
	MinTransferGonka int64 = 10

	// MinTransferNgonka is the equivalent minimum in ngonka (10 gonka × 10^9).
	MinTransferNgonka int64 = 10_000_000_000
)

var _ sdk.Msg = &MsgTransferWithVesting{}

func (m *MsgTransferWithVesting) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	if _, err := sdk.AccAddressFromBech32(m.Recipient); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient address: %s", err)
	}

	if m.Amount.IsZero() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "amount cannot be zero")
	}

	if !m.Amount.IsValid() {
		return errorsmod.Wrap(sdkerrors.ErrInvalidCoins, "invalid coins")
	}

	return validateMinTransferAmounts(m.Amount)
}

// validateMinTransferAmounts rejects dust amounts for native denominations.
// Each gonka/ngonka entry must represent at least 10 gonka.
func validateMinTransferAmounts(coins sdk.Coins) error {
	for _, coin := range coins {
		switch coin.Denom {
		case "gonka":
			if coin.Amount.LT(math.NewInt(MinTransferGonka)) {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins,
					"transfer amount %s is below minimum of %d gonka", coin.String(), MinTransferGonka)
			}
		case "ngonka":
			if coin.Amount.LT(math.NewInt(MinTransferNgonka)) {
				return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins,
					"transfer amount %s is below minimum of %d ngonka (equivalent to %d gonka)",
					coin.String(), MinTransferNgonka, MinTransferGonka)
			}
		}
	}
	return nil
}
