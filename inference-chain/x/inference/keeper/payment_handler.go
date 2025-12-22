package keeper

import (
	"context"
	"math"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference, cost int64) (int64, error) {
	payeeAddress, err := sdk.AccAddressFromBech32(inference.RequestedBy)
	if err != nil {
		return 0, err
	}
	k.LogDebug("Sending coins to escrow", types.Payments, "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	coins, err := types.GetCoins(cost)
	if err != nil {
		return 0, err
	}
	err = k.BankKeeper.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, coins, "escrow for inferenceId:"+inference.InferenceId)
	if err != nil {
		k.LogError("Error sending coins to escrow", types.Payments, "error", err)
		return 0,
			sdkerrors.Wrap(err, types.ErrRequesterCannotPay.Error())
	}
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
	coins, err := types.GetCoins(newCoins)
	if err != nil {
		return err
	}
	return k.BankKeeper.MintCoins(ctx, types.ModuleName, coins, memo)
}

func (k *Keeper) PayParticipantFromEscrow(ctx context.Context, address string, amount int64, memo string, vestingPeriods *uint64) error {
	return k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, memo, vestingPeriods)
}

func (k *Keeper) PayParticipantFromModule(ctx context.Context, address string, amount int64, moduleName string, memo string, vestingPeriods *uint64) error {
	participantAddress, err := sdk.AccAddressFromBech32(address)
	if err != nil {
		return err
	}
	if amount == 0 {
		k.LogInfo("No amount to pay", types.Payments, "participant", participantAddress, "amount", amount, "address", address, "module", moduleName, "vestingPeriods", vestingPeriods)
		return nil
	}

	vestingEpochs := vestingPeriods
	k.LogInfo("Paying participant", types.Payments, "participant", participantAddress, "amount", amount, "address", address, "module", moduleName, "vestingPeriods", vestingPeriods)

	if vestingPeriods != nil && *vestingPeriods > 0 {
		// Route through streamvesting system
		vestingAmount, err := types.GetCoins(amount)
		if err != nil {
			return err
		}
		// Vesting keeper should move funds and create vesting schedule
		err = k.GetStreamVestingKeeper().AddVestedRewards(ctx, address, types.ModuleName, vestingAmount, vestingEpochs, memo+"_vested")
		if err != nil {
			k.LogError("Error adding vested payment", types.Payments, "error", err, "amount", vestingAmount)
			return err
		}
	} else {
		// Direct payment (existing logic)
		coins, err := types.GetCoins(amount)
		if err != nil {
			return err
		}
		err = k.BankKeeper.SendCoinsFromModuleToAccount(ctx, moduleName, participantAddress, coins, memo)
	}
	return err
}

func (k *Keeper) BurnModuleCoins(ctx context.Context, burnCoins int64, memo string) error {
	if burnCoins <= 0 {
		k.LogInfo("No coins to burn", types.Payments, "coins", burnCoins)
		return nil
	}
	k.LogInfo("Burning coins", types.Payments, "coins", burnCoins)
	coins, err := types.GetCoins(burnCoins)
	if err != nil {
		return err
	}
	err = k.BankKeeper.BurnCoins(ctx, types.ModuleName, coins, memo)
	if err == nil {
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: uint64(burnCoins)})
	}
	return err
}

func (k *Keeper) IssueRefund(ctx context.Context, refundAmount int64, address string, memo string) error {
	k.LogInfo("Issuing refund", types.Payments, "address", address, "amount", refundAmount)
	err := k.PayParticipantFromEscrow(ctx, address, refundAmount, memo, nil) // Refunds should be direct payment
	if err != nil {
		k.LogError("Error issuing refund", types.Payments, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalRefunded: uint64(refundAmount)})
	return nil
}

func (k *Keeper) SafeLogSubAccountTransaction(ctx context.Context, recipient, sender, subaccount string, amount int64, memo string) {
	coin, err := types.GetCoin(amount)
	if err != nil {
		k.LogError("Negative coins", types.Payments, "recipient", recipient, "sender", sender, "amount", amount, "memo", memo)
	} else {
		k.BankKeeper.LogSubAccountTransaction(ctx, recipient, sender, subaccount, coin, memo)
	}
}

func (k *Keeper) SafeLogSubAccountTransactionUint(ctx context.Context, recipient, sender, subaccount string, amount uint64, memo string) {
	if amount > uint64(math.MaxInt64) {
		k.LogError("Amount exceeds int64 max", types.Payments, "recipient", recipient, "sender", sender, "amount", amount, "memo", memo)
		return
	}
	k.SafeLogSubAccountTransaction(ctx, recipient, sender, subaccount, int64(amount), memo)
}
