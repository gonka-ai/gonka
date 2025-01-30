package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// Start with a power of 2 for even distribution?
const EpochNewCoin = 1_048_576
const CoinHalvingHeight = 100

func (k *Keeper) SettleAccounts(ctx context.Context, pocBlockHeight uint64) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	participants, err := k.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	if err != nil {
		k.LogError("Error getting participants", "error", err)
		return err
	}

	k.LogInfo("Block height", "height", blockHeight)
	k.LogInfo("Got participants", "participants", len(participants.Participant))

	data, found := k.GetEpochGroupData(ctx, pocBlockHeight)
	k.LogInfo("Settling for block", "height", pocBlockHeight)
	if !found {
		k.LogError("Epoch group data not found", "height", pocBlockHeight)
		return types.ErrCurrentEpochGroupNotFound
	}
	seedSigMap := make(map[string]string)
	for _, seedSig := range data.MemberSeedSignatures {
		seedSigMap[seedSig.MemberAddress] = seedSig.Signature
	}
	tokenomicsParams := k.GetParams(ctx).TokenomicsParams
	amounts, rewardCoins, err := GetSettleAmounts(participants.Participant, tokenomicsParams)
	if err != nil {
		k.LogError("Error getting settle amounts", "error", err)
		return err
	}
	err = k.MintRewardCoins(ctx, rewardCoins)
	if err != nil {
		k.LogError("Unable to mint new coins!", "error", err)
		return err
	}
	for _, amount := range amounts {
		if amount.Error != nil {
			k.LogError("Error calculating settle amounts", "error", amount.Error, "participant", amount.Settle.Participant)
			continue
		}
		seedSignature, found := seedSigMap[amount.Settle.Participant]
		if found {
			amount.Settle.SeedSignature = seedSignature
		}
		totalPayment := amount.Settle.WorkCoins + amount.Settle.RewardCoins + amount.Settle.RefundCoins
		if totalPayment == 0 {
			k.LogDebug("No payment needed for participant", "address", amount.Settle.Participant)
			continue
		}
		k.LogInfo("Settle for participant", "rewardCoins", amount.Settle.RewardCoins, "refundCoins", amount.Settle.RefundCoins, "workCoins", amount.Settle.WorkCoins, "address", amount.Settle.Participant)
		participant, found := k.GetParticipant(ctx, amount.Settle.Participant)
		if !found {
			k.LogError("Participant not found", "address", amount.Settle.Participant)
			continue
		}
		// Issue refunds right away, participants may not be validating
		if amount.Settle.RefundCoins > 0 {
			k.LogInfo("Paying refund", "address", participant.Address, "amount", amount.Settle.RefundCoins)
			err = k.PayParticipantFromEscrow(ctx, amount.Settle.Participant, amount.Settle.RefundCoins)
			if err != nil {
				k.LogError("Error paying refund", "error", err)
				continue
			}
			amount.Settle.RefundCoins = 0
		}
		if amount.Settle.RewardCoins > 0 && participant.Reputation < 1.0 {
			participant.Reputation += 0.01
		}
		participant.CoinBalance = 0
		participant.RefundBalance = 0
		k.SetParticipant(ctx, participant)
		amount.Settle.PocStartHeight = pocBlockHeight
		previousSettle, found := k.GetSettleAmount(ctx, amount.Settle.Participant)
		if found {
			// No claim, burn it!
			err = k.BurnCoins(ctx, int64(previousSettle.GetTotalCoins()))
			if err != nil {
				k.LogError("Error burning coins", "error", err)
			}
		}
		k.SetSettleAmount(ctx, *amount.Settle)
	}
	return nil
}

func GetSettleAmounts(participants []types.Participant, tokenParams *types.TokenomicsParams) ([]*SettleResult, int64, error) {
	totalWork, invalidatedBalance := getWorkTotals(participants)
	totalRewardCoin := float64(totalWork) * float64(tokenParams.CurrentSubsidyPercentage)
	punishmentDistribution := DistributedCoinInfo{
		totalWork:       totalWork,
		totalRewardCoin: float64(invalidatedBalance),
	}
	rewardDistribution := DistributedCoinInfo{
		totalWork:       totalWork,
		totalRewardCoin: totalRewardCoin,
	}
	amounts := make([]*SettleResult, 0)
	distributions := make([]DistributedCoinInfo, 0)
	distributions = append(distributions, punishmentDistribution)
	distributions = append(distributions, rewardDistribution)
	for _, p := range participants {
		settle, err := getSettleAmount(&p, distributions)
		amounts = append(amounts, &SettleResult{
			Settle: settle,
			Error:  err,
		})
	}
	if totalWork == 0 {
		return amounts, 0, nil
	}
	return amounts, int64(totalRewardCoin), nil
}

func getWorkTotals(participants []types.Participant) (int64, int64) {
	totalWork := int64(0)
	invalidatedBalance := int64(0)
	for _, p := range participants {
		// Do not count invalid participants work as "work", since it should not be part of the distributions
		if p.CoinBalance > 0 && p.RefundBalance >= 0 && p.Status != types.ParticipantStatus_INVALID {
			totalWork += p.CoinBalance
		}
		if p.CoinBalance > 0 && p.Status == types.ParticipantStatus_INVALID {
			invalidatedBalance += p.CoinBalance
		}
	}
	return totalWork, invalidatedBalance
}

func getSettleAmount(participant *types.Participant, rewardInfo []DistributedCoinInfo) (*types.SettleAmount, error) {
	settle := &types.SettleAmount{
		Participant: participant.Address,
	}
	if participant.CoinBalance < 0 {
		return settle, types.ErrNegativeCoinBalance
	}
	if participant.RefundBalance < 0 {
		return settle, types.ErrNegativeRefundBalance
	}
	if participant.CoinBalance == 0 && participant.RefundBalance == 0 {
		return settle, nil
	}
	if participant.Status == types.ParticipantStatus_INVALID {
		return settle, nil
	}
	workCoins := participant.CoinBalance
	refundCoins := participant.RefundBalance
	rewardCoins := int64(0)
	for _, distribution := range rewardInfo {
		if participant.Status == types.ParticipantStatus_INVALID {
			continue
		}
		rewardCoins += distribution.calculateDistribution(workCoins)
	}
	return &types.SettleAmount{
		RewardCoins: uint64(rewardCoins),
		RefundCoins: uint64(refundCoins),
		WorkCoins:   uint64(workCoins),
		Participant: participant.Address,
	}, nil
}

type DistributedCoinInfo struct {
	totalWork       int64
	totalRewardCoin float64
}

func (rc *DistributedCoinInfo) calculateDistribution(participantWorkDone int64) int64 {
	bonusCoins := float64(participantWorkDone) / float64(rc.totalWork) * rc.totalRewardCoin
	return int64(bonusCoins)
}

type SettleResult struct {
	Settle *types.SettleAmount
	Error  error
}
