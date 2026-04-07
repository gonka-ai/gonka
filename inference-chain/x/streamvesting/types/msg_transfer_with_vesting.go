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

	// DefaultVestingEpochs is the default number of epochs for vesting (180 epochs)
	DefaultVestingEpochs = uint64(180)

	// MaxVestingEpochs is the maximum allowed vesting epochs to prevent DoS (~10 years)
	MaxVestingEpochs = uint64(3650)

	// MaxCoinsInAmount is the maximum number of coin denominations in a single transfer
	MaxCoinsInAmount = 10

	// MaxBatchRecipients is the maximum number of recipients in a single batch transfer
	MaxBatchRecipients = 500

	// MaxBatchCoinEntries is the maximum total number of coin entries across all outputs
	MaxBatchCoinEntries = 2000
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

	if len(m.Amount) > MaxCoinsInAmount {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many coin denominations: %d, max allowed: %d", len(m.Amount), MaxCoinsInAmount)
	}

	if m.VestingEpochs > MaxVestingEpochs {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "vesting epochs %d exceeds maximum allowed: %d", m.VestingEpochs, MaxVestingEpochs)
	}

	return validateMinTransferAmounts(m.Amount)
}

// validateMinTransferAmounts rejects unknown denominations and dust amounts.
// Only gonka and ngonka are accepted; each entry must represent at least 10 gonka.
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
		default:
			return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins,
				"unsupported denomination %q: only gonka and ngonka are allowed", coin.Denom)
		}
	}
	return nil
}
