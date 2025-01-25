package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type PaymentHandler interface {
	PutPaymentInEscrow(ctx context.Context, inference *types.Inference) (int64, error)
	MintRewardCoins(ctx context.Context, newCoins int64) error
	PayParticipantFromEscrow(ctx context.Context, address string, amount uint64) error
}

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference) (int64, error) {
	cost := CalculateCost(*inference)
	payeeAddress, err := sdk.AccAddressFromBech32(inference.RequestedBy)
	if err != nil {
		return 0, err
	}
	k.LogDebug("Sending coins to escrow", "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	err = k.bank.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, GetCoins(cost))
	if err != nil {
		k.LogError("Error sending coins to escrow", "error", err)
		return 0,
			sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
	}
	k.LogInfo("Sent coins to escrow", "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	return cost, nil
}

func (k *Keeper) MintRewardCoins(ctx context.Context, newCoins int64) error {
	return k.bank.MintCoins(ctx, types.ModuleName, GetCoins(newCoins))
}

func (k *Keeper) PayParticipantFromEscrow(ctx context.Context, address string, amount uint64) error {
	participantAddress, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return err
	}

	k.LogInfo("Paying participant", "participant", participantAddress, "amount", amount, "address", address)
	err = k.bank.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddress, GetCoins(int64(amount)))
	return err
}

func (k *Keeper) BurnCoins(ctx context.Context, burnCoins int64) error {
	if burnCoins <= 0 {
		k.LogInfo("No coins to burn", "coins", burnCoins)
		return nil
	}
	k.LogInfo("Burning coins", "coins", burnCoins)
	return k.bank.BurnCoins(ctx, types.ModuleName, GetCoins(burnCoins))
}
