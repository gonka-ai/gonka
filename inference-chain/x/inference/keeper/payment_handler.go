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
	k.LogDebug("Sending coins to escrow", types.Payments, "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	err = k.BankKeeper.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, types.GetCoins(cost))
	if err != nil {
		k.LogError("Error sending coins to escrow", types.Payments, "error", err)
		return 0,
			sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
	}
	k.LogInfo("Sent coins to escrow", types.Payments, "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	return cost, nil
}

func (k *Keeper) MintRewardCoins(ctx context.Context, newCoins int64) error {
	if newCoins == 0 {
		return nil
	}
	if newCoins < 0 {
		k.LogError("Cannot mint negative coins", types.Payments, "coins", newCoins)
		return sdkerrors.Wrapf(types.ErrCannotMintNegativeCoins, "coins: %d", newCoins)
	}
	k.LogInfo("Minting coins", types.Payments, "coins", newCoins, "moduleAccount", types.ModuleName)
	return k.BankKeeper.MintCoins(ctx, types.ModuleName, types.GetCoins(newCoins))
}

func (k *Keeper) PayParticipantFromEscrow(ctx context.Context, address string, amount uint64) error {
	return k.PayParticipantFromModule(ctx, address, amount, types.ModuleName)
}

func (k *Keeper) PayParticipantFromModule(ctx context.Context, address string, amount uint64, moduleName string) error {
	participantAddress, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return err
	}

	k.LogInfo("Paying participant", types.Payments, "participant", participantAddress, "amount", amount, "address", address, "module", moduleName)
	err = k.BankKeeper.SendCoinsFromModuleToAccount(ctx, moduleName, participantAddress, types.GetCoins(int64(amount)))
	return err
}

func (k *Keeper) BurnCoins(ctx context.Context, burnCoins int64) error {
	if burnCoins <= 0 {
		k.LogInfo("No coins to burn", types.Payments, "coins", burnCoins)
		return nil
	}
	k.LogInfo("Burning coins", types.Payments, "coins", burnCoins)
	err := k.BankKeeper.BurnCoins(ctx, types.ModuleName, types.GetCoins(burnCoins))
	if err == nil {
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: uint64(burnCoins)})
	}
	return err
}

func (k *Keeper) IssueRefund(ctx context.Context, refundAmount uint64, address string) error {
	k.LogInfo("Issuing refund", types.Payments, "address", address, "amount", refundAmount)
	err := k.PayParticipantFromEscrow(ctx, address, refundAmount)
	if err != nil {
		k.LogError("Error issuing refund", types.Payments, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalRefunded: refundAmount})
	return nil
}
