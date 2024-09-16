package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const inferenceDenom = "icoin"

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference) (uint64, error) {
	cost := CalculateCost(*inference)
	payeeAddress, err := sdk.AccAddressFromBech32(inference.ReceivedBy)
	if err != nil {
		return 0, err
	}
	err = k.bank.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, getCoins(cost))
	senderAddr := k.AccountKeeper.GetModuleAddress(types.ModuleName)
	k.LogInfo("Module Address", "address", senderAddr)
	if err != nil {
		k.LogError("Error sending coins to escrow", "error", err)
		return 0,
			sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
	}
	k.LogInfo("Sent coins to escrow", "inference", inference.InferenceId, "coins", cost)
	spendable := k.bankView.SpendableCoin(ctx, senderAddr, inferenceDenom)
	k.LogInfo("New spendable coins", "coins", spendable, "address", senderAddr)
	return cost, nil
}

func (k *Keeper) SettleParticipant(ctx context.Context, participant *types.Participant) error {
	k.LogInfo("Settling participant", "participant", participant)
	participantAddress, err := sdk.AccAddressFromBech32(participant.Address)
	senderAddr := k.AccountKeeper.GetModuleAddress(types.ModuleName)
	k.LogInfo("Module Address", "address", senderAddr)
	if err != nil {
		k.LogError("Error converting participant address", "error", err)
		return err
	}
	if participant.CoinBalance > 0 {
		k.LogInfo("Sending coins to participant", "coins", participant.CoinBalance, "participant", participant.Address)
		spendable := k.bankView.SpendableCoin(ctx, senderAddr, inferenceDenom)
		k.LogInfo("Spendable coins", "coins", spendable, "address", senderAddr)
		err = k.bank.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddress, getCoins(participant.CoinBalance))
		if err != nil {
			k.LogError("Error sending coins to participant", "error", err)
			return err
		}
		participant.CoinBalance = 0
		k.LogDebug("Sent coins to participant", "participant", participant)
	}
	if participant.RefundBalance > 0 {
		err = k.bank.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddress, getCoins(participant.RefundBalance))
		k.LogInfo("Sending refund to participant", "refund", participant.RefundBalance, "participant", participant.Address)
		if err != nil {
			k.LogError("Error sending refund to participant", "error", err)
			return err
		}
		participant.RefundBalance = 0
		k.LogDebug("Sent refund to participant", "participant", participant)
	}
	k.LogDebug("Settled participant", "participant", participant)
	return nil
}

func getCoins(coins uint64) sdk.Coins {
	return sdk.NewCoins(sdk.NewInt64Coin(inferenceDenom, int64(coins)))
}
