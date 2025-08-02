package keeper

import (
	"context"
	"fmt"

	"cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

// AdjustWeightsByCollateral adjusts participant weights based on their collateral deposit,
// implementing the core logic of the Tokenomics V2 proposal. After an initial grace
// period, a participant's final weight is a combination of a collateral-free
// base weight and additional weight activated by depositing collateral.
// This function modifies the participants' weights in-memory.
func (k Keeper) AdjustWeightsByCollateral(ctx context.Context, participants []*types.ActiveParticipant) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	inferenceParams := k.GetParams(sdkCtx)

	latestEpoch, found := k.GetLatestEpoch(sdkCtx)
	if !found {
		// This should not happen in a normal chain lifecycle
		return fmt.Errorf("latest epoch not found, cannot adjust weights by collateral")
	}

	collateralParams := inferenceParams.CollateralParams
	// During the grace period, collateral is not required. The BaseWeightRatio is
	// effectively 100%, so the PotentialWeight calculated by ComputeNewWeights
	// becomes the final EffectiveWeight. We can exit early.
	if latestEpoch.Index <= collateralParams.GracePeriodEndEpoch {
		k.LogInfo("Collateral grace period is active, skipping weight adjustment.", types.Tokenomics, "current_epoch", latestEpoch.Index, "grace_period_end", inferenceParams.CollateralParams.GracePeriodEndEpoch)
		return nil
	}

	k.LogInfo("Collateral grace period has ended. Adjusting weights by collateral.", types.Tokenomics, "current_epoch", latestEpoch.Index)

	baseWeightRatio, err := collateralParams.BaseWeightRatio.ToLegacyDec()
	if err != nil {
		panic(fmt.Sprintf("invalid base_weight_ratio: %v", err))
	}
	collateralPerWeightUnit, err := collateralParams.CollateralPerWeightUnit.ToLegacyDec()
	if err != nil {
		panic(fmt.Sprintf("invalid collateral_per_weight_unit: %v", err))
	}

	if collateralPerWeightUnit.IsZero() {
		k.LogWarn("CollateralPerWeightUnit is zero. Any non-zero collateral deposit will activate all eligible weight.", types.Tokenomics)
	}

	for _, participant := range participants {
		participantAddress, err := sdk.AccAddressFromBech32(participant.Index)
		if err != nil {
			k.LogError("Could not parse participant address, skipping weight adjustment for this participant", types.Tokenomics, "address", participant.Index, "error", err)
			continue
		}

		potentialWeight := math.LegacyNewDecFromInt(math.NewIntFromUint64(uint64(participant.Weight)))

		// 1. Calculate Base Weight: The portion of weight granted without collateral.
		baseWeight := potentialWeight.Mul(baseWeightRatio)

		// 2. Calculate Collateral-Eligible Weight: The remaining weight that can be activated by collateral.
		collateralEligibleWeight := potentialWeight.Sub(baseWeight)
		var activatedWeight math.LegacyDec

		// 3. Calculate Activated Weight: Determine how much of the eligible weight is activated by the participant's collateral.
		collateral, found := k.collateralKeeper.GetCollateral(sdkCtx, participantAddress)
		if !found || collateral.IsZero() {
			activatedWeight = math.LegacyZeroDec()
		} else {
			collateralAmount := math.LegacyNewDecFromInt(collateral.Amount)
			if !collateralPerWeightUnit.IsZero() {
				// Weight activated is limited by the collateral deposited.
				weightFromCollateral := collateralAmount.Quo(collateralPerWeightUnit)
				activatedWeight = math.LegacyMinDec(collateralEligibleWeight, weightFromCollateral)
			} else {
				// If collateral requirement is zero, any deposit activates all eligible weight.
				activatedWeight = collateralEligibleWeight
			}
		}

		// 4. Calculate Final Effective Weight and update the participant's weight in-memory.
		effectiveWeight := baseWeight.Add(activatedWeight)
		participant.Weight = effectiveWeight.TruncateInt64()

		k.LogDebug("Adjusted participant weight by collateral", types.Tokenomics,
			"participant", participant.Index,
			"potential_weight", potentialWeight.String(),
			"base_weight", baseWeight.String(),
			"eligible_weight", collateralEligibleWeight.String(),
			"activated_weight", activatedWeight.String(),
			"effective_weight", participant.Weight,
		)
	}

	return nil
}

// CheckAndSlashForInvalidStatus checks if a participant's status has transitioned to INVALID
// and, if so, triggers a collateral slash.
func (k Keeper) CheckAndSlashForInvalidStatus(ctx context.Context, originalStatus types.ParticipantStatus, participant *types.Participant) {
	// If the status changed to INVALID, trigger a slash.
	if originalStatus != types.ParticipantStatus_INVALID && participant.Status == types.ParticipantStatus_INVALID {
		sdkCtx := sdk.UnwrapSDKContext(ctx)
		params := k.GetParams(sdkCtx)
		slashFraction, err := params.CollateralParams.SlashFractionInvalid.ToLegacyDec()
		if err != nil {
			panic(fmt.Sprintf("invalid slash_fraction_invalid: %v", err))
		}

		participantAddress, err := sdk.AccAddressFromBech32(participant.Address)
		if err != nil {
			// This should not happen if the address is valid in the keeper.
			k.LogError("Could not parse participant address for slashing", types.Validation, "address", participant.Address, "error", err)
		} else {
			k.LogInfo("Slashing participant for being marked INVALID", types.Tokenomics,
				"participant", participant.Address,
				"slash_fraction", slashFraction.String(),
			)
			_, err := k.collateralKeeper.Slash(sdkCtx, participantAddress, slashFraction)
			if err != nil {
				k.LogError("Failed to slash participant", types.Tokenomics, "participant", participant.Address, "error", err)
				// Non-fatal error, we log and continue. The participant is already marked INVALID.
			}
		}
	}
}

// CheckAndSlashForDowntime checks a participant's performance for the completed epoch and
// slashes their collateral if their missed request percentage exceeds the threshold.
func (k Keeper) CheckAndSlashForDowntime(ctx context.Context, participant *types.Participant) {
	// Don't slash participants who had no work assigned.
	totalRequests := participant.CurrentEpochStats.InferenceCount + participant.CurrentEpochStats.MissedRequests
	if totalRequests == 0 {
		return
	}

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	params := k.GetParams(sdkCtx)
	downtimeThreshold, err := params.CollateralParams.DowntimeMissedPercentageThreshold.ToLegacyDec()
	if err != nil {
		panic(fmt.Sprintf("invalid downtime_missed_percentage_threshold: %v", err))
	}

	missedPercentage := math.LegacyNewDec(int64(participant.CurrentEpochStats.MissedRequests)).Quo(
		math.LegacyNewDec(int64(totalRequests)),
	)

	if missedPercentage.GT(downtimeThreshold) {
		slashFraction, err := params.CollateralParams.SlashFractionDowntime.ToLegacyDec()
		if err != nil {
			panic(fmt.Sprintf("invalid slash_fraction_downtime: %v", err))
		}
		participantAddress, err := sdk.AccAddressFromBech32(participant.Address)
		if err != nil {
			k.LogError("Could not parse participant address for downtime slashing", types.Tokenomics, "address", participant.Address, "error", err)
			return
		}

		k.LogInfo("Slashing participant for downtime", types.Tokenomics,
			"participant", participant.Address,
			"missed_percentage", missedPercentage.String(),
			"downtime_threshold", downtimeThreshold.String(),
			"slash_fraction", slashFraction.String(),
		)

		_, err = k.collateralKeeper.Slash(sdkCtx, participantAddress, slashFraction)
		if err != nil {
			k.LogError("Failed to slash participant for downtime", types.Tokenomics, "participant", participant.Address, "error", err)
		}
	} else {
		k.LogInfo("Participant did not exceed downtime threshold", types.Tokenomics, "participant", participant.Address, "missed_percentage", missedPercentage.String(), "downtime_threshold", downtimeThreshold.String())
	}
}
