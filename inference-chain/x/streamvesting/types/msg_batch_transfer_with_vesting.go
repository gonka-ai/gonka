package types

import (
	errorsmod "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
)

var _ sdk.Msg = &MsgBatchTransferWithVesting{}

func (m *MsgBatchTransferWithVesting) ValidateBasic() error {
	if _, err := sdk.AccAddressFromBech32(m.Sender); err != nil {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid sender address: %s", err)
	}

	if len(m.Outputs) == 0 {
		return errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "outputs cannot be empty")
	}

	if len(m.Outputs) > MaxBatchRecipients {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many recipients: %d, max allowed: %d", len(m.Outputs), MaxBatchRecipients)
	}

	if m.VestingEpochs > MaxVestingEpochs {
		return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "vesting epochs %d exceeds maximum allowed: %d", m.VestingEpochs, MaxVestingEpochs)
	}

	totalCoinEntries := 0
	aggregated := make(map[string]sdk.Coins, len(m.Outputs))

	for i, output := range m.Outputs {
		if _, err := sdk.AccAddressFromBech32(output.Recipient); err != nil {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidAddress, "invalid recipient address at index %d: %s", i, err)
		}

		if output.Amount.IsZero() {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "amount cannot be zero for recipient at index %d", i)
		}

		if !output.Amount.IsValid() {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidCoins, "invalid coins for recipient at index %d", i)
		}

		if len(output.Amount) > MaxCoinsInAmount {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many coin denominations for recipient %s: %d, max allowed: %d", output.Recipient, len(output.Amount), MaxCoinsInAmount)
		}

		totalCoinEntries += len(output.Amount)
		if totalCoinEntries > MaxBatchCoinEntries {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many total coin entries in batch: %d, max allowed: %d", totalCoinEntries, MaxBatchCoinEntries)
		}

		if err := validateMinTransferAmounts(output.Amount); err != nil {
			return errorsmod.Wrapf(err, "recipient at index %d (%s)", i, output.Recipient)
		}

		aggregated[output.Recipient] = aggregated[output.Recipient].Add(output.Amount...)
		if len(aggregated[output.Recipient]) > MaxCoinsInAmount {
			return errorsmod.Wrapf(sdkerrors.ErrInvalidRequest, "too many total coin denominations for recipient %s: %d, max allowed: %d", output.Recipient, len(aggregated[output.Recipient]), MaxCoinsInAmount)
		}
	}

	return nil
}
