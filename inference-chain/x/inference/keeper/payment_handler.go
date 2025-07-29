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

func (k *Keeper) SendCoinsFromModuleToAccount(ctx context.Context, senderModule string, recipientAddr sdk.AccAddress, amt sdk.Coins, memo string) error {
	err := k.BankKeeper.SendCoinsFromModuleToAccount(ctx, senderModule, recipientAddr, amt)
	if err != nil {
		return err
	}
	k.LogTransaction(recipientAddr.String(), senderModule, amt.AmountOf(types.BaseCoin).Int64(), memo)
	return nil
}
func (k *Keeper) SendCoinsFromModuleToModule(ctx context.Context, senderModule, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.BankKeeper.SendCoinsFromModuleToModule(ctx, senderModule, recipientModule, amt)
	if err != nil {
		return err
	}
	k.LogTransaction(recipientModule, senderModule, amt.AmountOf(types.BaseCoin).Int64(), memo)
	return nil
}
func (k *Keeper) SendCoinsFromAccountToModule(ctx context.Context, senderAddr sdk.AccAddress, recipientModule string, amt sdk.Coins, memo string) error {
	err := k.BankKeeper.SendCoinsFromAccountToModule(ctx, senderAddr, recipientModule, amt)
	if err != nil {
		return err
	}
	k.LogTransaction(recipientModule, senderAddr.String(), amt.AmountOf(types.BaseCoin).Int64(), memo)
	return nil
}

func (k *Keeper) MintCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error {
	if amt.IsZero() {
		return nil
	}
	err := k.BankKeeper.MintCoins(ctx, moduleName, amt)
	if err != nil {
		return err
	}
	k.LogTransaction(moduleName, "supply", amt.AmountOf(types.BaseCoin).Int64(), memo)
	return nil
}

func (k *Keeper) BurnCoins(ctx context.Context, moduleName string, amt sdk.Coins, memo string) error {
	if amt.IsZero() {
		k.LogInfo("No coins to burn", types.Payments)
		return nil
	}
	k.LogInfo("Burning coins", types.Payments, "coins", amt)
	err := k.BankKeeper.BurnCoins(ctx, types.ModuleName, amt)
	if err == nil {
		burnCoins := amt.AmountOf(types.BaseCoin).Int64()
		k.LogTransaction("supply", types.ModuleName, burnCoins, memo)
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: uint64(burnCoins)})
	}
	return err
}

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference, cost int64) (int64, error) {
	payeeAddress, err := sdk.AccAddressFromBech32(inference.RequestedBy)
	if err != nil {
		return 0, err
	}
	k.LogDebug("Sending coins to escrow", types.Payments, "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	err = k.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, types.GetCoins(cost), "escrow for inferenceId:"+inference.InferenceId)
	if err != nil {
		k.LogError("Error sending coins to escrow", types.Payments, "error", err)
		return 0,
			sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
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
	return k.MintCoins(ctx, types.ModuleName, types.GetCoins(newCoins), memo)
}

func (k *Keeper) PayParticipantFromEscrow(ctx context.Context, address string, amount uint64, memo string, vestingPeriods *uint64) error {
	return k.PayParticipantFromModule(ctx, address, amount, types.ModuleName, memo, vestingPeriods)
}

func (k *Keeper) PayParticipantFromModule(ctx context.Context, address string, amount uint64, moduleName string, memo string, vestingPeriods *uint64) error {
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
		vestingAmount := types.GetCoins(int64(amount))
		// Vesting keeper should move funds and create vesting schedule
		err = k.GetStreamVestingKeeper().AddVestedRewards(ctx, address, types.ModuleName, vestingAmount, vestingEpochs, memo+"_vested")
		if err != nil {
			k.LogError("Error adding vested payment", types.Payments, "error", err, "amount", vestingAmount)
			return err
		}
	} else {
		// Direct payment (existing logic)
		err = k.SendCoinsFromModuleToAccount(ctx, moduleName, participantAddress, types.GetCoins(int64(amount)), memo)
	}
	return err
}

func (k *Keeper) LogSubAccountTransaction(recipient string, sender string, subAccount string, amt sdk.Coin, memo string) {
	if amt.Denom != types.BaseCoin {
		k.LogError("Invalid coin denomination for transaction logging", types.Payments, "recipient", recipient, "sender", sender, "amount", amt, "memo", memo)
		return
	}
	k.LogTransaction(recipient+"_"+subAccount, sender+"_"+subAccount, amt.Amount.Int64(), memo)
}

func (k *Keeper) BurnModuleCoins(ctx context.Context, burnCoins int64, memo string) error {
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
