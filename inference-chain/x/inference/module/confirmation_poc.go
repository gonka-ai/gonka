package inference

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

// handleConfirmationPoC manages confirmation PoC trigger decisions and phase transitions
func (am AppModule) handleConfirmationPoC(ctx context.Context, blockHeight int64) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Get current parameters
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}

	confirmationParams := params.ConfirmationPocParams
	if confirmationParams == nil {
		// Confirmation PoC not configured, skip
		return nil
	}

	// Check if expected confirmations is 0 (feature disabled)
	if confirmationParams.ExpectedConfirmationsPerEpoch == 0 {
		return nil
	}

	epochParams := params.EpochParams
	if epochParams == nil {
		return fmt.Errorf("epoch params not found")
	}

	// Get current epoch context
	currentEpoch, found := am.keeper.GetEffectiveEpoch(ctx)
	if !found || currentEpoch == nil {
		// No epoch yet, skip
		return nil
	}

	epochContext, err := types.NewEpochContextFromEffectiveEpoch(*currentEpoch, *epochParams, blockHeight)
	if err != nil {
		return fmt.Errorf("failed to create epoch context: %w", err)
	}

	// Handle phase transitions for active event
	err = am.handleConfirmationPoCPhaseTransitions(ctx, blockHeight, epochContext, epochParams)
	if err != nil {
		am.LogError("Error handling confirmation PoC phase transitions", types.PoC, "error", err)
		// Continue to check for new triggers
	}

	// Check if we should trigger a new confirmation PoC event
	err = am.checkConfirmationPoCTrigger(ctx, blockHeight, epochContext, epochParams, confirmationParams, sdkCtx)
	if err != nil {
		return fmt.Errorf("failed to check confirmation PoC trigger: %w", err)
	}

	return nil
}

// checkConfirmationPoCTrigger checks if a confirmation PoC event should be triggered
func (am AppModule) checkConfirmationPoCTrigger(
	ctx context.Context,
	blockHeight int64,
	epochContext *types.EpochContext,
	epochParams *types.EpochParams,
	confirmationParams *types.ConfirmationPoCParams,
	sdkCtx sdk.Context,
) error {
	// Don't trigger in early epochs (0, 1) - no confirmation PoC needed
	if epochContext.EpochIndex <= 1 {
		return nil
	}

	// Only trigger during inference phase
	currentPhase := epochContext.GetCurrentPhase(blockHeight)
	if currentPhase != types.InferencePhase {
		return nil
	}

	// Check if there's already an active event
	_, isActive, err := am.keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active confirmation PoC event: %w", err)
	}
	if isActive {
		// Already have an active event, don't trigger another
		return nil
	}

	// Check for upgrades within upgrade protection window
	upgradeProtectionWindow := confirmationParams.UpgradeProtectionWindow
	if upgradeProtectionWindow <= 0 {
		upgradeProtectionWindow = 500 // Default to 500 blocks if not set
	}
	hasUpgrade, reason, err := am.keeper.HasUpgradeInWindow(ctx, blockHeight, upgradeProtectionWindow)
	if err != nil {
		return fmt.Errorf("failed to check upgrade window: %w", err)
	}
	if hasUpgrade {
		am.LogDebug("Skipping confirmation PoC trigger due to upgrade protection", types.PoC,
			"blockHeight", blockHeight,
			"upgradeProtectionWindow", upgradeProtectionWindow,
			"reason", reason)
		return nil
	}

	// Calculate valid trigger window
	// [SetNewValidators(), NextPoCStart - InferenceValidationCutoff - ConfirmationWindowDuration]
	setNewValidatorsHeight := epochContext.SetNewValidators()
	nextEpochContext := epochContext.NextEpochContext()
	nextPoCStart := nextEpochContext.PocStartBlockHeight

	// Total duration includes all phases (same as regular PoC structure)
	confirmationWindowDuration := epochParams.PocStageDuration +
		epochParams.PocExchangeDuration +
		epochParams.PocValidationDelay +
		epochParams.PocValidationDuration
	triggerWindowEnd := nextPoCStart - epochParams.InferenceValidationCutoff - confirmationWindowDuration

	if blockHeight < setNewValidatorsHeight || blockHeight > triggerWindowEnd {
		// Outside valid trigger window
		return nil
	}

	triggerWindowLength := triggerWindowEnd - setNewValidatorsHeight + 1
	if triggerWindowLength <= 0 {
		// Invalid window
		return nil
	}

	// Calculate trigger probability using deterministicFloat pattern
	expectedConfirmations := decimal.NewFromInt(int64(confirmationParams.ExpectedConfirmationsPerEpoch))
	windowBlocks := decimal.NewFromInt(triggerWindowLength)
	triggerProbability := expectedConfirmations.Div(windowBlocks)

	// Use block hash at H-1 as randomness source
	prevBlockHash := sdkCtx.HeaderInfo().Hash
	if len(prevBlockHash) < 8 {
		return fmt.Errorf("block hash too short: %d bytes", len(prevBlockHash))
	}

	blockHashSeed := int64(binary.BigEndian.Uint64(prevBlockHash[:8]))
	randFloat := calculations.DeterministicFloat(blockHashSeed, fmt.Sprintf("confirmation_poc_trigger_%d", blockHeight))

	shouldTrigger := randFloat.LessThan(triggerProbability)

	if !shouldTrigger {
		return nil
	}

	// Trigger a new confirmation PoC event
	am.LogInfo("Triggering confirmation PoC event", types.PoC,
		"blockHeight", blockHeight,
		"epochIndex", epochContext.EpochIndex,
		"triggerProbability", triggerProbability.String(),
		"randomValue", randFloat.String())

	// Get next event sequence number for this epoch
	existingEvents, err := am.keeper.GetAllConfirmationPoCEventsForEpoch(ctx, epochContext.EpochIndex)
	if err != nil {
		return fmt.Errorf("failed to get existing events: %w", err)
	}
	eventSequence := uint64(len(existingEvents))

	// Calculate event heights with minimum grace period of 1 block
	gracePeriod := epochParams.InferenceValidationCutoff
	if gracePeriod < 1 {
		gracePeriod = 1
	}
	generationStartHeight := blockHeight + gracePeriod

	// Create event - only store anchor, calculate rest dynamically via helper methods
	event := types.ConfirmationPoCEvent{
		EpochIndex:            epochContext.EpochIndex,
		EventSequence:         eventSequence,
		TriggerHeight:         blockHeight,
		GenerationStartHeight: generationStartHeight,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GRACE_PERIOD,
		PocSeedBlockHash:      "", // Will be set when transitioning to GENERATION phase
	}

	// Store the event
	err = am.keeper.SetConfirmationPoCEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to store confirmation PoC event: %w", err)
	}

	// Set as active event
	err = am.keeper.SetActiveConfirmationPoCEvent(ctx, event)
	if err != nil {
		return fmt.Errorf("failed to set active confirmation PoC event: %w", err)
	}

	am.LogInfo("Created confirmation PoC event", types.PoC,
		"epochIndex", event.EpochIndex,
		"eventSequence", event.EventSequence,
		"triggerHeight", event.TriggerHeight,
		"generationStartHeight", event.GenerationStartHeight,
		"validationEndHeight", event.GetValidationEnd(epochParams))

	return nil
}

// handleConfirmationPoCPhaseTransitions manages phase transitions for active confirmation PoC events
func (am AppModule) handleConfirmationPoCPhaseTransitions(
	ctx context.Context,
	blockHeight int64,
	epochContext *types.EpochContext,
	epochParams *types.EpochParams,
) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	if epochContext.EpochIndex <= 1 {
		return nil
	}

	activeEvent, isActive, err := am.keeper.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active confirmation PoC event: %w", err)
	}
	if !isActive || activeEvent == nil {
		// No active event
		return nil
	}

	event := *activeEvent
	updated := false
	transitionCount := 0
	var transitions []string

	// GRACE_PERIOD -> GENERATION transition
	if event.ShouldTransitionToGeneration(blockHeight) {
		// Capture block hash from (generation_start_height - 1)
		// At generation_start_height, HeaderInfo().Hash gives us the hash of the previous block
		prevBlockHash := sdkCtx.HeaderInfo().Hash
		event.PocSeedBlockHash = hex.EncodeToString(prevBlockHash)
		event.Phase = types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION
		updated = true
		transitionCount++
		transitions = append(transitions, "GRACE_PERIOD->GENERATION")

		am.LogInfo("Confirmation PoC: GRACE_PERIOD -> GENERATION", types.PoC,
			"epochIndex", event.EpochIndex,
			"eventSequence", event.EventSequence,
			"blockHeight", blockHeight,
			"generationStartHeight", event.GenerationStartHeight,
			"pocSeedBlockHash", event.PocSeedBlockHash[:16]+"...")
	}

	// GENERATION -> VALIDATION transition
	if event.ShouldTransitionToValidation(blockHeight, epochParams) {
		event.Phase = types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION
		updated = true
		transitionCount++
		transitions = append(transitions, "GENERATION->VALIDATION")

		am.LogInfo("Confirmation PoC: GENERATION -> VALIDATION", types.PoC,
			"epochIndex", event.EpochIndex,
			"eventSequence", event.EventSequence,
			"blockHeight", blockHeight,
			"validationStartHeight", event.GetValidationStart(epochParams))
	}

	// VALIDATION -> COMPLETED transition
	if event.ShouldTransitionToCompleted(blockHeight, epochParams) {
		event.Phase = types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED
		updated = true
		transitionCount++
		transitions = append(transitions, "VALIDATION->COMPLETED")

		err := am.updateConfirmationWeights(ctx, &event)
		if err != nil {
			am.LogError("Confirmation PoC: Failed to update confirmation weights", types.PoC,
				"epochIndex", event.EpochIndex,
				"eventSequence", event.EventSequence,
				"error", err)
		}

		am.LogInfo("Confirmation PoC: VALIDATION -> COMPLETED", types.PoC,
			"epochIndex", event.EpochIndex,
			"eventSequence", event.EventSequence,
			"blockHeight", blockHeight,
			"validationEndHeight", event.GetValidationEnd(epochParams))
	}

	// Clear active event after transition delay
	if event.Phase == types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED {
		completionHeight := event.GetValidationEnd(epochParams) + 1
		if blockHeight >= completionHeight+epochParams.SetNewValidatorsDelay {
			err := am.keeper.ClearActiveConfirmationPoCEvent(ctx)
			if err != nil {
				return fmt.Errorf("failed to clear active confirmation PoC event: %w", err)
			}
			updated = false
			am.LogInfo("Confirmation PoC: Cleared active event", types.PoC,
				"epochIndex", event.EpochIndex,
				"eventSequence", event.EventSequence,
				"blockHeight", blockHeight)
		}
	}

	// Warn if multiple transitions occurred (catch-up scenario)
	if transitionCount > 1 {
		am.LogWarn("Confirmation PoC: Multiple phase transitions in single block (catch-up)", types.PoC,
			"epochIndex", event.EpochIndex,
			"eventSequence", event.EventSequence,
			"blockHeight", blockHeight,
			"transitionCount", transitionCount,
			"transitions", transitions)
	}

	// Update the event if phase changed
	if updated {
		// Update stored event
		err = am.keeper.SetConfirmationPoCEvent(ctx, event)
		if err != nil {
			return fmt.Errorf("failed to update confirmation PoC event: %w", err)
		}

		// Update active event (keep during COMPLETED transition period)
		err = am.keeper.SetActiveConfirmationPoCEvent(ctx, event)
		if err != nil {
			return fmt.Errorf("failed to update active confirmation PoC event: %w", err)
		}
	}

	return nil
}

// updateConfirmationWeights calculates confirmation weights from PoC batches/validations
// and updates EpochGroupData.ValidationWeights with minimum values
func (am AppModule) updateConfirmationWeights(ctx context.Context, event *types.ConfirmationPoCEvent) error {
	am.LogInfo("updateConfirmationWeights: Updating confirmation weights", types.PoC,
		"epochIndex", event.EpochIndex,
		"eventSequence", event.EventSequence,
		"triggerHeight", event.TriggerHeight)

	// Get current epoch's EpochGroupData
	epochGroupData, found := am.keeper.GetEpochGroupData(ctx, event.EpochIndex, "")
	if !found {
		return fmt.Errorf("epoch group data not found for epoch %d", event.EpochIndex)
	}

	// Get current validator weights for WeightCalculator
	currentValidatorWeights, err := am.getCurrentValidatorWeights(ctx)
	if err != nil {
		return fmt.Errorf("failed to get current validator weights: %w", err)
	}

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}
	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	migrationState := GetMigrationStateFromParams(params.PocParams)

	useV2, dryRun := false, false
	switch migrationState {
	case ModeFullV2:
		useV2 = true
		// Grace period: dry-run for the epoch when V2 was enabled
		if graceEpoch, ok := am.keeper.GetPocV2EnabledEpoch(ctx); ok && event.EpochIndex == graceEpoch {
			dryRun = true
		}
	case ModeMigration:
		if event.EventSequence == 0 {
			useV2, dryRun = true, true
		}
	}
	am.evaluateConfirmation(ctx, event, &epochGroupData, currentValidatorWeights, weightScaleFactor, useV2, dryRun)

	return nil
}

func (am AppModule) evaluateConfirmation(
	ctx context.Context,
	event *types.ConfirmationPoCEvent,
	epochGroupData *types.EpochGroupData,
	currentValidatorWeights map[string]int64,
	weightScaleFactor mathsdk.LegacyDec,
	useV2 bool,
	dryRun bool,
) {
	var confirmationParticipants []*types.ActiveParticipant
	if useV2 {
		confirmationParticipants = am.updateConfirmationWeightsV2(ctx, event, currentValidatorWeights, weightScaleFactor)
	} else {
		confirmationParticipants = am.UpdateConfirmationWeightsV1(ctx, event, currentValidatorWeights, weightScaleFactor)
	}

	confirmationWeights := make(map[string]int64)
	for _, cp := range confirmationParticipants {
		confirmationWeights[cp.Index] = cp.Weight
	}

	am.LogInfo("evaluateConfirmation: Confirmation weights", types.PoC,
		"useV2", useV2, "dryRun", dryRun, "confirmationWeights", confirmationWeights)

	if dryRun {
		return
	}

	notPreservedWeights, err := am.GetNotPreservedTotalWeightByParticipant(ctx, event.EpochIndex)
	if err != nil {
		am.LogError("evaluateConfirmation: Failed to get not preserved weights", types.PoC, "error", err)
	}

	updated := false
	for i, vw := range epochGroupData.ValidationWeights {
		if calculatedWeight, found := confirmationWeights[vw.MemberAddress]; found {
			if calculatedWeight < vw.ConfirmationWeight {
				previousWeight := vw.ConfirmationWeight
				epochGroupData.ValidationWeights[i].ConfirmationWeight = calculatedWeight
				updated = true
				am.LogInfo("evaluateConfirmation: Updated confirmation weight", types.PoC,
					"participant", vw.MemberAddress,
					"previousWeight", previousWeight,
					"newWeight", calculatedWeight)
			}
		} else {
			pocWeight := notPreservedWeights[vw.MemberAddress]
			if pocWeight > 0 && vw.ConfirmationWeight > 0 {
				previousWeight := vw.ConfirmationWeight
				epochGroupData.ValidationWeights[i].ConfirmationWeight = 0
				updated = true
				am.LogInfo("evaluateConfirmation: No batches submitted, setting weight to 0", types.PoC,
					"participant", vw.MemberAddress,
					"previousWeight", previousWeight)
			}
		}
	}

	if updated {
		am.keeper.SetEpochGroupData(ctx, *epochGroupData)
		am.LogInfo("evaluateConfirmation: Saved updated EpochGroupData", types.PoC,
			"epochIndex", event.EpochIndex)
	}

	am.checkConfirmationSlashing(ctx, epochGroupData)
}

// updateConfirmationWeightsV2 calculates confirmation weights using off-chain store commits
func (am AppModule) updateConfirmationWeightsV2(
	ctx context.Context,
	event *types.ConfirmationPoCEvent,
	currentValidatorWeights map[string]int64,
	weightScaleFactor mathsdk.LegacyDec,
) []*types.ActiveParticipant {
	// Get off-chain store commits using trigger_height as key
	storeCommits, err := am.keeper.GetAllPoCV2StoreCommitsForStage(ctx, event.TriggerHeight)
	if err != nil {
		am.LogError("updateConfirmationWeightsV2: failed to get store commits for confirmation", types.PoC, "error", err)
		return nil
	}

	// Get weight distributions for per-node weights
	weightDistributions, err := am.keeper.GetAllMLNodeWeightDistributionsForStage(ctx, event.TriggerHeight)
	if err != nil {
		am.LogError("updateConfirmationWeightsV2: failed to get weight distributions for confirmation", types.PoC, "error", err)
		// Continue without distributions
	}

	validationsV2, err := am.keeper.GetPoCValidationsV2ByStage(ctx, event.TriggerHeight)
	if err != nil {
		am.LogError("updateConfirmationWeightsV2: failed to get PoC v2 validations for confirmation", types.PoC, "error", err)
		return nil
	}

	// Collect participants and seeds
	participants := make(map[string]types.Participant)
	seeds := make(map[string]types.RandomSeed)

	for participantAddress := range storeCommits {
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogWarn("updateConfirmationWeightsV2: Participant not found", types.PoC,
				"address", participantAddress)
			continue
		}
		participants[participantAddress] = participant

		seed, found := am.keeper.GetRandomSeed(ctx, event.EpochIndex, participantAddress)
		if found {
			seeds[participantAddress] = seed
		}
	}

	guardianEnabled := am.keeper.GetGenesisGuardianEnabled(ctx)
	guardianAddrs := am.keeper.GetGenesisGuardianAddresses(ctx)
	guardianSet := make(map[string]bool, len(guardianAddrs))
	for _, addr := range guardianAddrs {
		guardianSet[addr] = true
	}

	// Create WeightCalculator with store commits and distributions
	calculator := NewWeightCalculator(
		currentValidatorWeights,
		storeCommits,
		weightDistributions,
		validationsV2,
		participants,
		seeds,
		event.TriggerHeight,
		am,
		weightScaleFactor,
		guardianEnabled,
		guardianSet,
	)

	// Calculate confirmation weights
	return calculator.Calculate()
}

// checkConfirmationSlashing checks if participants should be slashed based on confirmation PoC results
// Stub implementation - slashing logic not yet implemented
func (am AppModule) checkConfirmationSlashing(
	ctx context.Context,
	epochGroupData *types.EpochGroupData,
) error {
	notPreservedTotalWeight, err := am.GetNotPreservedTotalWeightByParticipant(ctx, epochGroupData.EpochIndex)
	if err != nil {
		return fmt.Errorf("failed to get not preserved total weight by participant: %w", err)
	}
	for _, vw := range epochGroupData.ValidationWeights {
		address := vw.MemberAddress
		notPreservedTotalWeightValue, found := notPreservedTotalWeight[address]
		if !found {
			am.LogWarn("checkConfirmationSlashing: Not preserved total weight not found for participant", types.PoC,
				"address", address)
			continue
		}
		confirmationWeight := vw.ConfirmationWeight
		participant, found := am.keeper.GetParticipant(ctx, address)
		if !found {
			am.LogWarn("checkConfirmationSlashing: Participant not found", types.PoC,
				"address", address)
			continue
		}
		if notPreservedTotalWeightValue == 0 {
			participant.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromDecimal(decimal.NewFromInt(1))
		} else {
			participant.CurrentEpochStats.ConfirmationPoCRatio = types.DecimalFromDecimal(decimal.NewFromInt(confirmationWeight).Div(decimal.NewFromInt(notPreservedTotalWeightValue)))
		}
		am.keeper.SetParticipant(ctx, participant)
	}
	return nil
}

func (am AppModule) GetNotPreservedTotalWeightByParticipant(ctx context.Context, epochId uint64) (map[string]int64, error) {
	participants, found := am.keeper.GetActiveParticipants(ctx, epochId)
	if !found {
		am.LogError("GetPreviousEpochMLNodesWithInferenceAllocation: Active participants not found", types.PoC, "epochId", epochId)
		return nil, errors.New("GetPreviousEpochMLNodesWithInferenceAllocation: active participant not found. epochId: " + strconv.FormatUint(epochId, 10))
	}

	result := make(map[string]int64)

	for _, p := range participants.Participants {
		am.LogInfo("GetPreviousEpochMLNodesWithInferenceAllocation. GetPreservedNodesByParticipant: Processing participant", types.PoC,
			"participantAddress", p.Index, "len(p.MlNodes)", len(p.MlNodes))

		totalWeight := int64(0)
		for _, nodeArray := range p.MlNodes {
			for _, mlNode := range nodeArray.MlNodes {
				if len(mlNode.TimeslotAllocation) > 1 && !mlNode.TimeslotAllocation[1] {
					totalWeight += mlNode.PocWeight
				}
			}
		}
		result[p.Index] = totalWeight
	}

	return result, nil
}
