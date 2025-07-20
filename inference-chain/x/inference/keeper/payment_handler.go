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
	PayParticipantFromEscrow(ctx context.Context, address string, amount uint64, memo string, vestingPeriods *uint64) error
}

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference, cost int64) (int64, error) {
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
	k.LogTransaction(types.ModuleName, payeeAddress.String(), cost, "inferenceId:"+inference.InferenceId)
	k.LogInfo("Sent coins to escrow", types.Payments, "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	return cost, nil
}

func (k *Keeper) MintRewardCoins(ctx context.Context, newCoins int64, memo string) error {
	if newCoins == 0 {
		return nil
	}
	if newCoins < 0 {
		k.LogError("Cannot mint negative coins", types.Payments, "coins", newCoins)
		return sdkerrors.Wrapf(types.ErrCannotMintNegativeCoins, "coins: %d", newCoins)
	}
	k.LogInfo("Minting coins", types.Payments, "coins", newCoins, "moduleAccount", types.ModuleName)
	err := k.BankKeeper.MintCoins(ctx, types.ModuleName, types.GetCoins(newCoins))
	if err == nil {
		k.LogTransaction(types.ModuleName, "supply", newCoins, memo)
	}
	return err
}

func (k *Keeper) PayParticipantFromEscrow(ctx context.Context, address string, amount uint64, memo string, vestingPeriods *uint64) error {
	return k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, memo, vestingPeriods)
}

func (k *Keeper) PayParticipantFromModule(ctx context.Context, address string, amount uint64, moduleName string, memo string, vestingPeriods *uint64) error {
	participantAddress, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return err
	}

	vestingEpochs := vestingPeriods
	k.LogInfo("Paying participant", types.Payments, "participant", participantAddress, "amount", amount, "address", address, "module", moduleName, "vestingPeriods", vestingPeriods)

	if vestingPeriods != nil && *vestingPeriods > 0 {
		// Route through streamvesting system
		vestingAmount := types.GetCoins(int64(amount))

		// First, transfer coins from source module to streamvesting module
		err = k.BankKeeper.SendCoinsFromModuleToModule(ctx, moduleName, "streamvesting", vestingAmount)
		if err != nil {
			k.LogError("Error transferring coins to streamvesting module", types.Payments, "error", err, "amount", vestingAmount)
			return err
		}

		// Then, add to vesting schedule with specified vesting period
		err = k.GetStreamVestingKeeper().AddVestedRewards(ctx, address, vestingAmount, vestingEpochs)
		if err != nil {
			k.LogError("Error adding vested payment", types.Payments, "error", err, "amount", vestingAmount)
			return err
		}
		k.LogTransaction(address, moduleName, int64(amount), memo+"_vested")
	} else {
		// Direct payment (existing logic)
		err = k.BankKeeper.SendCoinsFromModuleToAccount(ctx, moduleName, participantAddress, types.GetCoins(int64(amount)))
		if err == nil {
			k.LogTransaction(address, moduleName, int64(amount), memo)
		}
	}

	return err
}

func (k *Keeper) BurnCoins(ctx context.Context, burnCoins int64, memo string) error {
	if burnCoins <= 0 {
		k.LogInfo("No coins to burn", types.Payments, "coins", burnCoins)
		return nil
	}
	k.LogInfo("Burning coins", types.Payments, "coins", burnCoins)
	err := k.BankKeeper.BurnCoins(ctx, types.ModuleName, types.GetCoins(burnCoins))
	if err == nil {
		k.LogTransaction("supply", types.ModuleName, burnCoins, memo)
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: uint64(burnCoins)})
	}
	return err
}

func (k *Keeper) IssueRefund(ctx context.Context, refundAmount uint64, address string, memo string) error {
	k.LogInfo("Issuing refund", types.Payments, "address", address, "amount", refundAmount)
	err := k.PayParticipantFromEscrow(ctx, address, refundAmount, memo, nil) // Refunds should be direct payment
	if err != nil {
		k.LogError("Error issuing refund", types.Payments, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalRefunded: refundAmount})
	return nil
}
