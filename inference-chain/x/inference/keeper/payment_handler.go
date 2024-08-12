package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const inferenceDenom = "icoin"

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference) error {
	cost := CalculateCost(*inference)
	payeeAddress, err := sdk.AccAddressFromBech32(inference.ReceivedBy)
	if err != nil {
		return err
	}
	err = k.bank.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, sdk.NewCoins(sdk.NewInt64Coin(inferenceDenom, int64(cost))))
	if err != nil {
		return sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
	}
	return nil
}
