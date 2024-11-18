package keeper

import (
	"context"
	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

const inferenceDenom = "icoin"

func (k *Keeper) PutPaymentInEscrow(ctx context.Context, inference *types.Inference) (int64, error) {
	cost := CalculateCost(*inference)
	payeeAddress, err := sdk.AccAddressFromBech32(inference.RequestedBy)
	if err != nil {
		return 0, err
	}
	k.LogDebug("Sending coins to escrow", "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	err = k.bank.SendCoinsFromAccountToModule(ctx, payeeAddress, types.ModuleName, getCoins(cost))
	if err != nil {
		k.LogError("Error sending coins to escrow", "error", err)
		return 0,
			sdkerrors.Wrapf(err, types.ErrRequesterCannotPay.Error())
	}
	k.LogInfo("Sent coins to escrow", "inference", inference.InferenceId, "coins", cost, "payee", payeeAddress)
	return cost, nil
}

func (k *Keeper) MintRewardCoins(ctx context.Context, newCoins int64) error {
	return k.bank.MintCoins(ctx, types.ModuleName, getCoins(newCoins))
}

func (k *Keeper) SettleParticipant(ctx context.Context, participant *types.Participant, totalWork uint64, newCoin uint64) error {
	k.LogInfo("Settling participant", "participant", participant)
	participantAddress, err := sdk.AccAddressFromBech32(participant.Address)
	if err != nil {
		k.LogError("Error converting participant address", "error", err)
		return err
	}
	if participant.CoinBalance < 0 {
		k.LogWarn("Participant has negative coin balance", "participant", participant)
	}
	if participant.RefundBalance < 0 {
		k.LogWarn("Participant has negative refund balance", "participant", participant)
	}
	if participant.CoinBalance > 0 {
		bonusCoinsInt := k.calculateBonusCoins(participant, totalWork, newCoin)
		k.LogDebug("Bonus coins", "coins", bonusCoinsInt)
		k.LogDebug("Participant coin balance", "coins", participant.CoinBalance)
		k.LogInfo("Sending coins to participant", "coins", participant.CoinBalance+bonusCoinsInt, "participant", participant.Address)
		err = k.bank.SendCoinsFromModuleToAccount(ctx, types.ModuleName, participantAddress, getCoins(participant.CoinBalance+bonusCoinsInt))
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

func (k *Keeper) calculateBonusCoins(participant *types.Participant, totalWork uint64, newCoin uint64) int64 {
	bonusCoins := float64(participant.CoinBalance) / float64(totalWork) * float64(newCoin)
	bonusCoinsInt := int64(bonusCoins)
	return bonusCoinsInt
}

func getCoins(coins int64) sdk.Coins {
	return sdk.NewCoins(sdk.NewInt64Coin(inferenceDenom, coins))
}
