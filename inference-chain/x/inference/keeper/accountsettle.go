package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/log"
	sdk "github.com/cosmos/cosmos-sdk/types"
	govtypes "github.com/cosmos/cosmos-sdk/x/gov/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"

	"github.com/shopspring/decimal"
)

type SettleParameters struct {
	TotalSubsidyPaid   int64 `json:"total_subsidy_paid"`
	TotalSubsidySupply int64 `json:"total_subsidy_supply"`
}

func (k *Keeper) GetSettleParameters(ctx context.Context) (*SettleParameters, error) {
	tokenomicsData, found := k.GetTokenomicsData(ctx)
	if !found {
		return nil, fmt.Errorf("tokenomics data not found")
	}
	genesisOnlyParams, found := k.GetGenesisOnlyParams(ctx)
	if !found {
		return nil, fmt.Errorf("genesis only params not found")
	}
	normalizedTotalSuply := sdk.NormalizeCoin(sdk.NewInt64Coin(genesisOnlyParams.SupplyDenom, genesisOnlyParams.StandardRewardAmount))
	return &SettleParameters{
		TotalSubsidyPaid:   int64(tokenomicsData.TotalSubsidies),
		TotalSubsidySupply: normalizedTotalSuply.Amount.Int64(),
	}, nil
}

func CheckAndPunishForDowntimeForParticipants(participants []types.Participant, rewards map[string]uint64, p0 *types.Decimal, logger log.Logger) {
	for _, participant := range participants {
		rewards[participant.Address] = CheckAndPunishForDowntimeForParticipant(participant, rewards[participant.Address], p0, logger)
	}
}

func CheckAndPunishForDowntimeForParticipant(participant types.Participant, reward uint64, p0 *types.Decimal, logger log.Logger) uint64 {
	totalRequests := participant.CurrentEpochStats.InferenceCount + participant.CurrentEpochStats.MissedRequests
	missedRequests := participant.CurrentEpochStats.MissedRequests
	logger.Info("Checking downtime for participant", "participant", participant.Address, "totalRequests", totalRequests, "missedRequests", missedRequests, "reward", reward)
	finalReward := CheckAndPunishForDowntime(totalRequests, missedRequests, reward, p0)
	logger.Info("Final reward after downtime check", "participant", participant.Address, "finalReward", finalReward)
	return finalReward
}

func CheckAndPunishForDowntime(total, missed, reward uint64, p0 *types.Decimal) uint64 {
	if total == 0 {
		return reward
	}
	passed, err := calculations.MissedStatTest(int(missed), int(total), p0.ToDecimal())
	if err != nil {
		return reward
	}
	if !passed {
		return 0
	}
	return reward
}

// AggregateMLNodesFromModelSubgroups builds a map of participant addresses to their aggregated MLNodes
// by collecting MLNode data from all model-specific EpochGroup subgroups for the given epoch.
func (k *Keeper) AggregateMLNodesFromModelSubgroups(ctx context.Context, epochIndex uint64, validationWeights []*types.ValidationWeight) map[string][]*types.MLNodeInfo {
	participantMLNodes := make(map[string][]*types.MLNodeInfo)
	allEpochGroups := k.GetAllEpochGroupData(ctx)

	for _, vw := range validationWeights {
		aggregated := make([]*types.MLNodeInfo, 0)
		for _, subgroup := range allEpochGroups {
			if subgroup.EpochIndex != epochIndex || subgroup.ModelId == "" {
				continue // Skip wrong epoch or parent group
			}
			for _, subVw := range subgroup.ValidationWeights {
				if subVw.MemberAddress == vw.MemberAddress {
					aggregated = append(aggregated, subVw.MlNodes...)
					break
				}
			}
		}
		participantMLNodes[vw.MemberAddress] = aggregated
		k.LogInfo("Settlement: Aggregated MLNodes for participant", types.Settle,
			"participant", vw.MemberAddress,
			"numMLNodes", len(aggregated))
	}

	return participantMLNodes
}

func (k *Keeper) SettleAccounts(ctx context.Context, currentEpochIndex uint64, previousEpochIndex uint64) error {
	if currentEpochIndex == 0 {
		k.LogInfo("SettleAccounts Skipped For Epoch 0", types.Settle, "currentEpochIndex", currentEpochIndex, "skipping")
		return nil
	}

	k.LogInfo("SettleAccounts", types.Settle, "currentEpochIndex", currentEpochIndex)
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	blockHeight := sdkCtx.BlockHeight()
	activeParticipants, found := k.GetActiveParticipants(ctx, currentEpochIndex)
	if !found {
		k.LogError("Active participants not found", types.Settle, "currentEpochIndex", currentEpochIndex)
		return nil
	}
	activeParticipantAddresses := make([]string, len(activeParticipants.Participants))
	for i, participant := range activeParticipants.Participants {
		activeParticipantAddresses[i] = participant.Index
	}
	allParticipants := k.GetParticipants(ctx, activeParticipantAddresses)

	k.LogInfo("Block height", types.Settle, "height", blockHeight)
	k.LogInfo("Got all participants", types.Settle, "participants", len(allParticipants))

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
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Error getting params", types.Settle, "error", err)
		return err
	}
	var amounts []*SettleResult
	var rewardAmount int64
	var governanceRewardAmount int64
	settleParameters, err := k.GetSettleParameters(ctx)
	if err != nil {
		k.LogError("Error getting settle parameters", types.Settle, "error", err)
		return err
	}
	k.LogInfo("Settle parameters", types.Settle, "parameters", settleParameters)

	// Use Bitcoin-style fixed reward system with its own parameters
	k.LogInfo("Using Bitcoin-style reward system", types.Settle)

	// Aggregate MLNodes from model-specific subgroups for preservedWeight calculation
	participantMLNodes := k.AggregateMLNodesFromModelSubgroups(ctx, currentEpochIndex, data.ValidationWeights)

	var bitcoinResult BitcoinResult
	amounts, bitcoinResult, err = GetBitcoinSettleAmounts(allParticipants, &data, params.BitcoinRewardParams, params.ValidationParams, settleParameters, participantMLNodes, k.Logger())
	if err != nil {
		k.LogError("Error getting Bitcoin settle amounts", types.Settle, "error", err)
	}
	if bitcoinResult.Amount < 0 {
		k.LogError("Bitcoin reward amount is negative", types.Settle, "amount", bitcoinResult.Amount)
		return types.ErrNegativeRewardAmount
	}
	k.LogInfo("Bitcoin reward amount", types.Settle, "amount", bitcoinResult.Amount)
	rewardAmount = bitcoinResult.Amount
	governanceRewardAmount = bitcoinResult.GovernanceAmount

	err = k.MintRewardCoins(ctx, rewardAmount, "reward_distribution")
	if err != nil {
		k.LogError("Error minting reward coins", types.Settle, "error", err)
		return err
	}
	k.AddTokenomicsData(ctx, &types.TokenomicsData{TotalSubsidies: uint64(rewardAmount)})

	// In Bitcoin reward system, any undistributed rewards (e.g. downtime punishments or rounding)
	// are transferred to governance instead of being redistributed to other participants.
	if params.BitcoinRewardParams.UseBitcoinRewards && governanceRewardAmount > 0 {
		coins, err := types.GetCoins(governanceRewardAmount)
		if err != nil {
			return err
		}
		memo := fmt.Sprintf("bitcoin_reward_to_governance:epoch=%d", currentEpochIndex)
		if err := k.BankKeeper.SendCoinsFromModuleToModule(ctx, types.ModuleName, govtypes.ModuleName, coins, memo); err != nil {
			k.LogError("Error transferring undistributed bitcoin rewards to governance", types.Settle, "error", err, "amount", governanceRewardAmount)
			return err
		}
		k.LogInfo("Transferred undistributed bitcoin rewards to governance", types.Settle, "amount", governanceRewardAmount)
	}

	k.LogInfo("Checking downtime for participants", types.Settle, "participants", len(allParticipants))

	for i, participant := range allParticipants {
		// amount should have the same order as participants
		amount := amounts[i]

		if participant.Status == types.ParticipantStatus_ACTIVE {
			participant.EpochsCompleted += 1
		}
		k.SafeLogSubAccountTransaction(ctx, types.ModuleName, participant.Address, "balance", participant.CoinBalance, "settling")
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
		err = k.SetEpochPerformanceSummary(ctx, epochPerformance)
		if err != nil {
			return err
		}
		participant.CurrentEpochStats = types.NewCurrentEpochStats()
		err := k.SetParticipant(ctx, participant)
		if err != nil {
			return err
		}
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
		k.SetSettleAmountWithGovernanceTransfer(ctx, *amount.Settle)
	}

	if previousEpochIndex == 0 {
		return nil
	}

	k.LogInfo("Transferring old settle amounts", types.Settle, "previousEpochIndex", previousEpochIndex)
	err = k.TransferOldSettleAmountsToGovernance(ctx, previousEpochIndex)
	if err != nil {
		k.LogError("Error burning old settle amounts", types.Settle, "error", err)
	}
	return nil
}

type DistributedCoinInfo struct {
	totalWork       int64
	totalRewardCoin int64
}

func (rc *DistributedCoinInfo) calculateDistribution(participantWorkDone int64) int64 {
	if participantWorkDone == 0 {
		return 0
	}
	if rc.totalWork == 0 {
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
