package inference

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"math"
)

// Start with a power of 2 for even distribution?
const EpochNewCoin = 1_048_576
const CoinHalvingHeight = 100

func (am AppModule) SettleAccounts(ctx context.Context) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	participants, err := am.keeper.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	if err != nil {
		am.LogError("Error getting participants", "error", err)
		return err
	}

	amounts, rewardCoins, err := GetSettleAmounts(participants.Participant, blockHeight)
	if err != nil {
		am.LogError("Error getting settle amounts", "error", err)
		return err
	}
	err = am.keeper.MintRewardCoins(ctx, rewardCoins)
	if err != nil {
		am.LogError("Unable to mint new coins!", "error", err)
		return err
	}
	for _, amount := range amounts {
		if amount.Error != nil {
			am.LogError("Error calculating settle amounts", "error", amount.Error)
			continue
		}
		totalPayment := amount.WorkCoins + amount.RewardCoins + amount.RefundCoins
		if totalPayment == 0 {
			am.LogDebug("No payment needed for participant", "address", amount.Participant.Index)
			continue
		}
		am.LogInfo("Settling participant", "rewardCoins", amount.RewardCoins, "refundCoins", amount.RefundCoins, "workCoins", amount.WorkCoins, "address", amount.Participant.Index)
		err = am.keeper.PayParticipantFromEscrow(ctx, amount.Participant.Address, totalPayment)
		if err != nil {
			am.LogError("Error paying participant", "error", err)
			return err
		}
		amount.Participant.CoinBalance = 0
		amount.Participant.RefundBalance = 0
		am.keeper.SetParticipant(ctx, *amount.Participant)
	}
	return nil
}

func GetSettleAmounts(participants []types.Participant, blockHeight int64) ([]SettleAmounts, int64, error) {
	halvings := blockHeight / CoinHalvingHeight
	// Halve it that many times
	totalRewardCoin := EpochNewCoin / math.Pow(2.0, float64(halvings))
	totalWork := int64(0)
	for _, p := range participants {
		totalWork += p.CoinBalance
	}
	rewardInfo := RewardCoinInfo{
		totalWork:       totalWork,
		totalRewardCoin: totalRewardCoin,
	}
	amounts := make([]SettleAmounts, len(participants))
	for i, p := range participants {
		amounts[i] = getSettleAmount(&p, rewardInfo)
	}
	return amounts, int64(totalRewardCoin), nil
}

func getSettleAmount(participant *types.Participant, rewardInfo RewardCoinInfo) SettleAmounts {
	if participant.CoinBalance < 0 {
		return SettleAmounts{
			Participant: participant,
			Error:       types.ErrNegativeCoinBalance,
		}
	}
	if participant.RefundBalance < 0 {
		return SettleAmounts{
			Participant: participant,
			Error:       types.ErrNegativeRefundBalance,
		}
	}
	if participant.CoinBalance == 0 && participant.RefundBalance == 0 {
		return SettleAmounts{Participant: participant}
	}
	workCoins := participant.CoinBalance
	refundCoins := participant.RefundBalance
	rewardCoins := rewardInfo.calculateBonusCoins(workCoins)
	return SettleAmounts{
		RewardCoins: uint64(rewardCoins),
		RefundCoins: uint64(refundCoins),
		WorkCoins:   uint64(workCoins),
		Participant: participant,
	}
}

type RewardCoinInfo struct {
	totalWork       int64
	totalRewardCoin float64
}

func (rc *RewardCoinInfo) calculateBonusCoins(participantWorkDone int64) int64 {
	bonusCoins := float64(participantWorkDone) / float64(rc.totalWork) * rc.totalRewardCoin
	return int64(bonusCoins)
}

type SettleAmounts struct {
	RewardCoins uint64
	RefundCoins uint64
	WorkCoins   uint64
	Participant *types.Participant
	Error       error
}
