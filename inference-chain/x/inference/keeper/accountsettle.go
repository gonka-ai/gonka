package keeper

import (
	"context"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type SettleParameters struct {
	CurrentSubsidyPercentage float32 `json:"current_subsidy_percentage"`
	TotalSubsidyPaid         int64   `json:"total_subsidy_paid"`
	StageCutoff              float64 `json:"stage_cutoff"`
	StageDecrease            float32 `json:"stage_decrease"`
	TotalSubsidySupply       int64   `json:"total_subsidy_supply"`
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
		// TODO: Settle Parameters should just use (our) Decimal
		CurrentSubsidyPercentage: params.TokenomicsParams.CurrentSubsidyPercentage.ToFloat32(),
		TotalSubsidyPaid:         int64(tokenomicsData.TotalSubsidies),
		StageCutoff:              params.TokenomicsParams.SubsidyReductionInterval.ToFloat(),
		StageDecrease:            params.TokenomicsParams.SubsidyReductionAmount.ToFloat32(),
		TotalSubsidySupply:       normalizedTotalSuply.Amount.Int64(),
	}
}

func (k *Keeper) SettleAccounts(ctx context.Context, currentEpochIndex uint64, previousEpochIndex uint64) error {
	if currentEpochIndex == 0 {
		k.LogInfo("SettleAccounts Skipped For Epoch 0", types.Settle, "currentEpochIndex", currentEpochIndex, "skipping")
		return nil
	}

	k.LogInfo("SettleAccounts", types.Settle, "currentEpochIndex", currentEpochIndex)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	participants, err := k.ParticipantAll(ctx, &types.QueryAllParticipantRequest{})
	if err != nil {
		k.LogError("Error getting participants", types.Settle, "error", err)
		return err
	}

	k.LogInfo("Block height", types.Settle, "height", blockHeight)
	k.LogInfo("Got participants", types.Settle, "participants", len(participants.Participant))

	data, found := k.GetEpochGroupData(ctx, currentEpochIndex, "")
	k.LogInfo("Settling for block", types.Settle, "height", currentEpochIndex)
	if !found {
		k.LogError("Epoch group data not found", types.Settle, "height", currentEpochIndex)
		return types.ErrCurrentEpochGroupNotFound
	}
	seedSigMap := make(map[string]string)
	for _, seedSig := range data.MemberSeedSignatures {
		seedSigMap[seedSig.MemberAddress] = seedSig.Signature
	}

	// Check governance flag to determine which reward system to use
	params := k.GetParams(ctx)
	var amounts []*SettleResult
	var rewardAmount int64
	settleParameters := k.GetSettleParameters(ctx)
	k.LogInfo("Settle parameters", types.Settle, "parameters", settleParameters)

	if params.BitcoinRewardParams.UseBitcoinRewards {
		// Use Bitcoin-style fixed reward system with its own parameters
		k.LogInfo("Using Bitcoin-style reward system", types.Settle)
		var bitcoinResult BitcoinResult
		amounts, bitcoinResult, err = GetBitcoinSettleAmounts(participants.Participant, &data, params.BitcoinRewardParams, settleParameters)
		if err != nil {
			k.LogError("Error getting Bitcoin settle amounts", types.Settle, "error", err)
		}
		rewardAmount = bitcoinResult.Amount
	} else {
		// Use current WorkCoins-based variable reward system with its own parameters
		k.LogInfo("Using current WorkCoins-based reward system", types.Settle)
		var subsidyResult SubsidyResult
		amounts, subsidyResult, err = GetSettleAmounts(participants.Participant, settleParameters)
		if err != nil {
			k.LogError("Error getting settle amounts", types.Settle, "error", err)
		}
		rewardAmount = subsidyResult.Amount
		// Handle cutoff logic internally for current system
		if subsidyResult.CrossedCutoff {
			k.LogInfo("Crossed subsidy cutoff", types.Settle, "amount", subsidyResult.Amount)
			k.ReduceSubsidyPercentage(ctx)
		}
	}

	err = k.MintRewardCoins(ctx, rewardAmount, "reward_distribution")
	if err != nil {
		k.LogError("Error minting reward coins", types.Settle, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalSubsidies: uint64(rewardAmount)})

	k.LogInfo("Checking downtime for participants", types.Settle, "participants", len(participants.Participant))
	for _, participant := range participants.Participant {
		k.LogDebug("Checking downtime for participant", types.Settle, "participant", participant.Address, "missed_requests", participant.CurrentEpochStats.MissedRequests, "inference_count", participant.CurrentEpochStats.InferenceCount)
		// TODO: Check if it is better to move this function outside the settleAccounts function.
		// Check for downtime and slash if necessary.
		k.CheckAndSlashForDowntime(ctx, &participant)
	}

	for i, participant := range participants.Participant {
		// amount should have the same order as participants
		amount := amounts[i]

		if participant.Status != types.ParticipantStatus_INVALID {
			participant.EpochsCompleted += 1
		}
		// TODO: Check if we need to reset status
		k.BankKeeper.LogSubAccountTransaction(ctx, types.ModuleName, participant.Address, "balance", sdk.NewInt64Coin(types.BaseCoin, participant.CoinBalance), "settling")
		participant.CoinBalance = 0
		participant.CurrentEpochStats.EarnedCoins = 0
		k.LogInfo("Participant CoinBalance reset", types.Balances, "address", participant.Address)
		epochPerformance := types.EpochPerformanceSummary{
			EpochIndex:            currentEpochIndex,
			ParticipantId:         participant.Address,
			InferenceCount:        participant.CurrentEpochStats.InferenceCount,
			MissedRequests:        participant.CurrentEpochStats.MissedRequests,
			EarnedCoins:           amount.Settle.WorkCoins,
			RewardedCoins:         amount.Settle.RewardCoins,
			ValidatedInferences:   participant.CurrentEpochStats.ValidatedInferences,
			InvalidatedInferences: participant.CurrentEpochStats.InvalidatedInferences,
			Claimed:               false,
		}
		k.SetEpochPerformanceSummary(ctx, epochPerformance)
		participant.CurrentEpochStats = &types.CurrentEpochStats{}
		k.SetParticipant(ctx, participant)
	}

	for _, amount := range amounts {
		// TODO: Check if we have to store 0 or error settle amount as well, as it store seed signature, which we may use somewhere
		if amount.Error != nil {
			k.LogError("Error calculating settle amounts", types.Settle, "error", amount.Error, "participant", amount.Settle.Participant)
			continue
		}
		totalPayment := amount.Settle.WorkCoins + amount.Settle.RewardCoins
		if totalPayment == 0 {
			k.LogDebug("No payment needed for participant", types.Settle, "address", amount.Settle.Participant)
			continue
		}

		seedSignature, found := seedSigMap[amount.Settle.Participant]
		if found {
			amount.Settle.SeedSignature = seedSignature
		}

		amount.Settle.EpochIndex = currentEpochIndex
		k.LogInfo("Settle for participant", types.Settle, "rewardCoins", amount.Settle.RewardCoins, "workCoins", amount.Settle.WorkCoins, "address", amount.Settle.Participant)
		k.SetSettleAmountWithBurn(ctx, *amount.Settle)
	}

	if previousEpochIndex == 0 {
		return nil
	}

	k.LogInfo("Burning old settle amounts", types.Settle, "previousEpochIndex", previousEpochIndex)
	err = k.BurnOldSettleAmounts(ctx, previousEpochIndex)
	if err != nil {
		k.LogError("Error burning old settle amounts", types.Settle, "error", err)
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
		// We have to create amount record for each participant in the same order as participants
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
