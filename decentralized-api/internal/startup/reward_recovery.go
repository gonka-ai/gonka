package startup

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"sync/atomic"
	"time"

	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/types"
)

const waitTimeBlocksFromLaunch = 60
const waitBetweenAttempts = 1000

func NewRewardRecoveryChecker(
	phaseTracker *chainphase.ChainPhaseTracker,
	recorder *cosmosclient.InferenceCosmosClient,
	validator *validation.InferenceValidator,
	configManager *apiconfig.ConfigManager,
) *RewardRecoveryChecker {
	return &RewardRecoveryChecker{
		launchBlockHeight:       0,
		lastRecoveryBlockHeight: 0,
		phaseTracker:            phaseTracker,
		recorder:                recorder,
		validator:               validator,
		configManager:           configManager,
	}
}

type RewardRecoveryChecker struct {
	launchBlockHeight       int64
	lastRecoveryBlockHeight int64
	autoRecoveryRunning     atomic.Bool
	phaseTracker            *chainphase.ChainPhaseTracker
	recorder                *cosmosclient.InferenceCosmosClient
	validator               *validation.InferenceValidator
	configManager           *apiconfig.ConfigManager
}

func (c *RewardRecoveryChecker) RecoverIfNeeded(
	currentBlockHeight int64,
) {
	if c.launchBlockHeight == 0 {
		logging.Info("[AutoRewardRecovery] Launch block height not set, setting to current block height", types.Claims,
			"currentBlockHeight", currentBlockHeight)
		c.launchBlockHeight = currentBlockHeight
	}

	if currentBlockHeight < (c.launchBlockHeight + waitTimeBlocksFromLaunch) {
		logging.Debug("[AutoRewardRecovery] Waiting for launch", types.Claims,
			"currentBlockHeight", currentBlockHeight,
			"launchBlockHeight", c.launchBlockHeight)
		return
	}

	if currentBlockHeight < (c.lastRecoveryBlockHeight + waitBetweenAttempts) {
		logging.Debug("[AutoRewardRecovery] Waiting for last recovery", types.Claims,
			"currentBlockHeight", currentBlockHeight,
			"lastRecoveryBlockHeight", c.lastRecoveryBlockHeight)
		return
	}

	latestEpoch := c.phaseTracker.GetCurrentEpochState().LatestEpoch
	inferenceValidationCutoff := latestEpoch.InferenceValidationCutoff()
	if currentBlockHeight > inferenceValidationCutoff {
		logging.Debug("[AutoRewardRecovery] Inference validation cutoff reached", types.Claims,
			"currentBlockHeight", currentBlockHeight,
			"inferenceValidationCutoff", inferenceValidationCutoff)
		return
	}

	if currentBlockHeight < (latestEpoch.ClaimMoney() + waitBetweenAttempts) {
		logging.Debug("[AutoRewardRecovery] Waiting for claim money", types.Claims,
			"currentBlockHeight", currentBlockHeight,
			"claimMoney", latestEpoch.ClaimMoney())
		return
	}

	if latestEpoch.GetCurrentPhase(currentBlockHeight) != types.InferencePhase {
		logging.Debug("[AutoRewardRecovery] Not in inference phase", types.Claims,
			"currentBlockHeight", currentBlockHeight,
			"latestEpoch", latestEpoch)
		return
	}

	c.lastRecoveryBlockHeight = currentBlockHeight

	// Prevent overlapping auto-recoveries
	if !c.autoRecoveryRunning.CompareAndSwap(false, true) {
		logging.Debug("[AutoRewardRecovery] Skipping, previous recovery still running", types.Claims)
		return
	}

	go func() {
		c.AutoRewardRecovery()
	}()
}

// AutoRewardRecovery checks for unclaimed settle amounts and attempts to recover rewards on startup
func (c *RewardRecoveryChecker) AutoRewardRecovery() {
	defer c.autoRecoveryRunning.Store(false)

	logging.Info("[AutoRewardRecovery] Starting automatic reward recovery check", types.Claims)

	// Get participant address
	address := c.recorder.GetAddress()
	if address == "" {
		logging.Error("[AutoRewardRecovery] Cannot perform reward recovery: no participant address", types.Claims)
		return
	}

	// Query for settle amount
	queryClient := c.recorder.NewInferenceQueryClient()
	ctx, cancel := context.WithTimeout(c.recorder.GetContext(), 30*time.Second)
	defer cancel()

	settleAmountResp, err := queryClient.SettleAmount(ctx, &types.QueryGetSettleAmountRequest{
		Participant: address,
	})

	if err != nil {
		// This is expected if no settle amount exists
		logging.Debug("[AutoRewardRecovery] No settle amount found for participant", types.Claims, "address", address, "error", err)
		return
	}

	if settleAmountResp == nil {
		logging.Debug("[AutoRewardRecovery] No settle amount data available", types.Claims, "address", address)
		return
	}

	settleAmount := settleAmountResp.SettleAmount
	totalAmount := settleAmount.RewardCoins + settleAmount.WorkCoins
	logging.Info("[AutoRewardRecovery] Found settle amount for participant", types.Claims,
		"address", address,
		"rewardCoins", settleAmount.RewardCoins,
		"workCoins", settleAmount.WorkCoins,
		"totalAmount", totalAmount,
		"epochIndex", settleAmount.EpochIndex)

	// Check if we have unclaimed rewards (totalAmount > 0 indicates pending rewards)
	if totalAmount <= 0 {
		logging.Info("[AutoRewardRecovery] No unclaimed rewards found", types.Claims, "address", address, "totalAmount", totalAmount)
		return
	}

	epochIndex := settleAmount.EpochIndex
	previousSeed := c.configManager.GetPreviousSeed()

	var seedValue int64
	if previousSeed.EpochIndex != epochIndex || previousSeed.Seed == 0 {
		generatedSeed, err := poc.CreateSeedForEpoch(c.recorder, epochIndex)
		if err != nil {
			logging.Error("[AutoRewardRecovery] Failed to generate seed", types.Claims,
				"epochIndex", epochIndex, "error", err)
			return
		}
		seedValue = generatedSeed
		logging.Info("[AutoRewardRecovery] Generated seed for epoch", types.Claims,
			"epochIndex", epochIndex, "seed", seedValue,
			"reason", map[bool]string{true: "epoch mismatch", false: "seed was zero"}[previousSeed.EpochIndex != epochIndex])
	} else {
		seedValue = previousSeed.Seed
		logging.Info("[AutoRewardRecovery] Using stored seed", types.Claims,
			"epochIndex", epochIndex, "seed", seedValue)
	}

	logging.Info("[AutoRewardRecovery] Attempting automatic reward recovery", types.Claims,
		"epochIndex", epochIndex,
		"seed", seedValue,
		"totalAmount", totalAmount,
		"address", address)

	// Perform validation recovery using the same logic as the admin endpoint
	missedInferences, err := c.validator.DetectMissedValidations(epochIndex, seedValue)
	if err != nil {
		logging.Error("[AutoRewardRecovery] Failed to detect missed validations during startup", types.Claims,
			"epochIndex", epochIndex,
			"error", err)
		return
	}

	missedCount := len(missedInferences)
	logging.Info("[AutoRewardRecovery] Startup recovery detected missed validations", types.Claims,
		"epochIndex", epochIndex,
		"missedCount", missedCount,
		"address", address)

	// Execute recovery validations if any were missed
	if missedCount > 0 {
		recoveredCount, err := c.validator.ExecuteRecoveryValidations(missedInferences)
		if err != nil {
			logging.Error("[AutoRewardRecovery] Failed to execute recovery validations during startup", types.Claims,
				"epochIndex", epochIndex,
				"missedCount", missedCount,
				"error", err)
			return
		}

		logging.Info("[AutoRewardRecovery] Startup recovery validations completed", types.Claims,
			"epochIndex", epochIndex,
			"recoveredCount", recoveredCount,
			"missedCount", missedCount,
			"address", address)

		// Wait for validations to be recorded on-chain
		if recoveredCount > 0 {
			logging.Info("[AutoRewardRecovery] Waiting for startup recovery validations to be recorded on-chain", types.Claims,
				"epochIndex", epochIndex,
				"recoveredCount", recoveredCount)
			c.validator.WaitForValidationsToBeRecorded()
		}
	}

	// Attempt to claim rewards
	err = c.recorder.ClaimRewards(&inference.MsgClaimRewards{
		Seed:       seedValue,
		EpochIndex: epochIndex,
	})
	if err != nil {
		logging.Error("[AutoRewardRecovery] Failed to claim rewards during startup recovery", types.Claims,
			"epochIndex", epochIndex,
			"error", err)
		return
	}

	if epochIndex == previousSeed.EpochIndex {
		err = c.configManager.MarkPreviousSeedClaimed()
		if err != nil {
			logging.Error("[AutoRewardRecovery] Failed to mark seed as claimed after successful recovery", types.Claims,
				"epochIndex", epochIndex,
				"error", err)
		}
	}

	logging.Info("[AutoRewardRecovery] Automatic reward recovery completed successfully", types.Claims,
		"epochIndex", epochIndex,
		"address", address)
}
