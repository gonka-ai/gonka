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
		k.LogError("Error getting participants", types.Settle, "error", err)
		return err
	}

	k.LogInfo("Block height", types.Settle, "height", blockHeight)
	k.LogInfo("Got participants", types.Settle, "participants", len(participants.Participant))

	data, found := k.GetEpochGroupData(ctx, pocBlockHeight)
	k.LogInfo("Settling for block", types.Settle, "height", pocBlockHeight)
	if !found {
		k.LogError("Epoch group data not found", types.Settle, "height", pocBlockHeight)
		return types.ErrCurrentEpochGroupNotFound
	}
	seedSigMap := make(map[string]string)
	for _, seedSig := range data.MemberSeedSignatures {
		seedSigMap[seedSig.MemberAddress] = seedSig.Signature
	}
	amounts, subsidyResult, err := GetSettleAmounts(participants.Participant, k.GetSettleParameters(ctx))
	if err != nil {
		k.LogError("Error getting settle amounts", types.Settle, "error", err)
		return err
	}
	err = k.MintRewardCoins(ctx, subsidyResult.Amount)
	if err != nil {
		k.LogError("Error minting reward coins", types.Settle, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalSubsidies: uint64(subsidyResult.Amount)})
	if subsidyResult.CrossedCutoff {
		k.LogInfo("Crossed subsidy cutoff", types.Settle, "amount", subsidyResult.Amount)
		k.ReduceSubsidyPercentage(ctx)
	}

	for _, amount := range amounts {
		if amount.Error != nil {
			k.LogError("Error calculating settle amounts", types.Settle, "error", amount.Error, "participant", amount.Settle.Participant)
			continue
		}

		seedSignature, found := seedSigMap[amount.Settle.Participant]
		if found {
			amount.Settle.SeedSignature = seedSignature
		}
		totalPayment := amount.Settle.WorkCoins + amount.Settle.RewardCoins
		if totalPayment == 0 {
			k.LogDebug("No payment needed for participant", types.Settle, "address", amount.Settle.Participant)
			continue
		}
		k.LogInfo("Settle for participant", types.Settle, "rewardCoins", amount.Settle.RewardCoins, "workCoins", amount.Settle.WorkCoins, "address", amount.Settle.Participant)
		participant, found := k.GetParticipant(ctx, amount.Settle.Participant)
		if !found {
			k.LogError("Participant not found", types.Settle, "address", amount.Settle.Participant)
			continue
		}
		participant.EpochsCompleted += 1
		participant.CoinBalance = 0
		k.SetEpochPerformanceSummary(ctx,
			types.EpochPerformanceSummary{
				EpochStartHeight:      pocBlockHeight,
				ParticipantId:         participant.Address,
				InferenceCount:        participant.CurrentEpochStats.InferenceCount,
				MissedRequests:        participant.CurrentEpochStats.MissedRequests,
				EarnedCoins:           amount.Settle.WorkCoins,
				RewardedCoins:         amount.Settle.RewardCoins,
				ValidatedInferences:   participant.CurrentEpochStats.ValidatedInferences,
				InvalidatedInferences: participant.CurrentEpochStats.InvalidatedInferences,
				Claimed:               false,
			})
		participant.CurrentEpochStats = &types.CurrentEpochStats{}
		k.SetParticipant(ctx, participant)
		amount.Settle.PocStartHeight = pocBlockHeight
		previousSettle, found := k.GetSettleAmount(ctx, amount.Settle.Participant)
		if found {
			// No claim, burn it!
			err = k.BurnCoins(ctx, int64(previousSettle.GetTotalCoins()))
			if err != nil {
				k.LogError("Error burning coins", types.Settle, "error", err)
			}
		}
		k.SetSettleAmount(ctx, *amount.Settle)
	}
	return nil
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
		if p.CoinBalance > 0 && p.Status != types.ParticipantStatus_INVALID {
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
	if participant.Status == types.ParticipantStatus_INVALID {
		return settle, nil
	}
	workCoins := participant.CoinBalance
	rewardCoins := int64(0)
	for _, distribution := range rewardInfo {
		if participant.Status == types.ParticipantStatus_INVALID {
			continue
		}
		rewardCoins += distribution.calculateDistribution(workCoins)
	}
	return &types.SettleAmount{
		RewardCoins: uint64(rewardCoins),
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
	if participantWorkDone == 0 {
		return 0
	}
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
