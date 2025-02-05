package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"math"
)

type SettleParameters struct {
	CurrentSubsidyPercentage float32
	TotalSubsidyPaid         int64
	StageCutoff              float64
	StageDecrease            float32
	TotalSubsidySupply       int64
}

type SubsidyResult struct {
	Amount        int64
	CrossedCutoff bool
}

func (sp *SettleParameters) GetTotalSubsidy(workCoins int64) SubsidyResult {
	if sp.TotalSubsidyPaid >= sp.TotalSubsidySupply {
		return SubsidyResult{Amount: 0, CrossedCutoff: false}
	}

	nextCutoff := sp.getNextCutoff()
	subsidyAtCurrentRate := getSubsidy(workCoins, sp.CurrentSubsidyPercentage)
	if sp.TotalSubsidyPaid+subsidyAtCurrentRate > nextCutoff {
		// Calculate the amount of subsidy that can be paid at the current rate
		// before the next cutoff
		subsidyUntilCutoff := nextCutoff - sp.TotalSubsidyPaid
		if nextCutoff >= sp.TotalSubsidySupply {
			return SubsidyResult{Amount: subsidyUntilCutoff, CrossedCutoff: true}
		}
		workUntilNextCutoff := getWork(subsidyUntilCutoff, sp.CurrentSubsidyPercentage)
		nextRate := sp.CurrentSubsidyPercentage * (1.0 - sp.StageDecrease)
		subsidyAtNextRate := getSubsidy(workCoins-workUntilNextCutoff, nextRate)
		return SubsidyResult{Amount: subsidyUntilCutoff + subsidyAtNextRate, CrossedCutoff: true}
	}
	return SubsidyResult{Amount: subsidyAtCurrentRate, CrossedCutoff: false}
}

// Clarify our approach to calculating the subsidy
func getSubsidy(work int64, rate float32) int64 {
	w := decimal.NewFromInt(work)
	r := decimal.NewFromInt(1).Sub(decimal.NewFromFloat32(rate))
	return w.Div(r).IntPart()
}

func getWork(subsidy int64, rate float32) int64 {
	s := decimal.NewFromInt(subsidy)
	r := decimal.NewFromInt(1).Sub(decimal.NewFromFloat32(rate))
	return s.Mul(r).IntPart()
}

func (sp *SettleParameters) getNextCutoff() int64 {
	cutoffUnit := int64(math.Round(sp.StageCutoff * float64(sp.TotalSubsidySupply)))
	currentCutoff := (sp.TotalSubsidyPaid / cutoffUnit) * cutoffUnit
	nextCutoff := currentCutoff + cutoffUnit
	return nextCutoff
}

func (k *Keeper) GetSettleParameters(ctx context.Context) *SettleParameters {
	params := k.GetParams(ctx)
	tokenomicsData, found := k.GetTokenomicsData(ctx)
	if !found {
		// Almost literally impossible
		panic("Tokenomics data not found")
	}
	genesisOnlyParams, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		// Almost literally impossible
		panic("Genesis only params not found")
	}
	normalizedTotalSuply := sdk.NormalizeCoin(sdk.NewInt64Coin(genesisOnlyParams.SupplyDenom, genesisOnlyParams.StandardRewardAmount))
	return &SettleParameters{
		CurrentSubsidyPercentage: params.TokenomicsParams.CurrentSubsidyPercentage,
		TotalSubsidyPaid:         int64(tokenomicsData.TotalSubsidies),
		StageCutoff:              params.TokenomicsParams.SubsidyReductionInterval,
		StageDecrease:            params.TokenomicsParams.SubsidyReductionAmount,
		TotalSubsidySupply:       normalizedTotalSuply.Amount.Int64(),
	}
}

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
	amounts, subsidyResult, err := GetSettleAmounts(participants.Participant, k.GetSettleParameters(ctx))
	if err != nil {
		k.LogError("Error getting settle amounts", "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalSubsidies: uint64(subsidyResult.Amount)})
	if subsidyResult.CrossedCutoff {
		k.LogInfo("Crossed subsidy cutoff", "amount", subsidyResult.Amount)
		k.ReduceSubsidyPercentage(ctx)
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
			k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalRefunded: amount.Settle.RefundCoins})
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
			k.burnUnclaimedSettle(ctx, amount, previousSettle)
		}
		k.SetSettleAmount(ctx, *amount.Settle)
	}
	return nil
}

func (k *Keeper) burnUnclaimedSettle(ctx context.Context, amount *SettleResult, previousSettle types.SettleAmount) {
	// No claim, burn it! This should not happen often
	k.LogWarn("Previous settle amount found, burning coins", "address", amount.Settle.Participant, "amount", previousSettle.GetTotalCoins())

	if previousSettle.RewardCoins > 0 {
		err := k.BankKeeper.BurnCoins(ctx, types.StandardRewardPoolAccName, types.GetCoins(int64(previousSettle.RewardCoins)))
		if err != nil {
			k.LogError("Error burning reward coins", "error", err)
		}
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: previousSettle.RewardCoins})
	}
	if previousSettle.WorkCoins > 0 || previousSettle.RefundCoins > 0 {
		err := k.BurnCoins(ctx, int64(previousSettle.WorkCoins+previousSettle.RefundCoins))
		if err != nil {
			k.LogError("Error burning work coins", "error", err)
		}
		k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalBurned: previousSettle.WorkCoins})
	}
}

func GetSettleAmounts(participants []types.Participant, tokenParams *SettleParameters) ([]*SettleResult, SubsidyResult, error) {
	totalWork, _ := getWorkTotals(participants)
	subsidyResult := tokenParams.GetTotalSubsidy(totalWork)
	rewardDistribution := DistributedCoinInfo{
		totalWork:       totalWork,
		totalRewardCoin: subsidyResult.Amount,
	}
	amounts := make([]*SettleResult, 0)
	distributions := make([]DistributedCoinInfo, 0)
	distributions = append(distributions, rewardDistribution)
	for _, p := range participants {
		settle, err := getSettleAmount(&p, distributions)
		amounts = append(amounts, &SettleResult{
			Settle: settle,
			Error:  err,
		})
	}
	if totalWork == 0 {
		return amounts, SubsidyResult{Amount: 0, CrossedCutoff: false}, nil
	}
	return amounts, subsidyResult, nil
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

func (k Keeper) ReduceSubsidyPercentage(ctx context.Context) {
	params := k.GetParams(ctx)
	params.TokenomicsParams = params.TokenomicsParams.ReduceSubsidyPercentage()
	err := k.SetParams(ctx, params)
	if err != nil {
		panic("Unable to set new subsidy percentage")
	}
}

type DistributedCoinInfo struct {
	totalWork       int64
	totalRewardCoin int64
}

func (rc *DistributedCoinInfo) calculateDistribution(participantWorkDone int64) int64 {
	wd := decimal.NewFromInt(participantWorkDone)
	tw := decimal.NewFromInt(rc.totalWork)
	tr := decimal.NewFromInt(rc.totalRewardCoin)
	bonusCoins := wd.Div(tw).Mul(tr)
	return bonusCoins.IntPart()
}

type SettleResult struct {
	Settle *types.SettleAmount
	Error  error
}
