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
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"github.com/shopspring/decimal"
)

var pocDeviationCoeff = decimal.New(909, -3)

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
	// Check if current epoch is a grace epoch with extended protection window
	if graceParams, ok := am.keeper.GetPunishmentGraceEpoch(ctx, epochContext.EpochIndex); ok && graceParams.UpgradeProtectionWindow > 0 {
		upgradeProtectionWindow = graceParams.UpgradeProtectionWindow
		am.LogDebug("using grace UpgradeProtectionWindow", types.PoC, "epoch", epochContext.EpochIndex, "window", upgradeProtectionWindow)
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
		epochParams.PocValidationDuration +
		epochParams.SetNewValidatorsDelay +
		epochParams.ConfirmationPocSafetyWindow
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

		modelAssigner := NewModelAssigner(am.keeper, am.keeper)
		preservedSnapshot, err := modelAssigner.SamplePreservedForEpisode(ctx, types.Epoch{Index: event.EpochIndex}, event.TriggerHeight)
		if err != nil {
			am.LogError("Confirmation PoC: failed to sample preserved nodes", types.PoC,
				"triggerHeight", event.TriggerHeight, "error", err)
		} else if err := am.captureGenerationStartTimestamp(ctx, sdkCtx.BlockTime().Unix(), event.TriggerHeight, preservedSnapshot); err != nil {
			am.LogError("Confirmation PoC: failed to store generation start snapshots", types.PoC,
				"triggerHeight", event.TriggerHeight, "error", err)
		}

		am.LogInfo("Confirmation PoC: GRACE_PERIOD -> GENERATION", types.PoC,
			"epochIndex", event.EpochIndex,
			"eventSequence", event.EventSequence,
			"blockHeight", blockHeight,
			"generationStartHeight", event.GenerationStartHeight,
			"pocSeedBlockHash", event.PocSeedBlockHash[:16]+"...")
	}

	// GENERATION -> VALIDATION transition
	if event.ShouldTransitionToValidation(blockHeight, epochParams) {
		am.captureConfirmationValidationSnapshot(ctx, blockHeight, event.TriggerHeight)

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
			// Clean up validation snapshot
			am.keeper.DeletePoCValidationSnapshot(ctx, event.TriggerHeight)

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

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return fmt.Errorf("failed to get params: %w", err)
	}

	return am.evaluateConfirmation(ctx, event, &epochGroupData, params.PocParams)
}

// evaluateConfirmation lowers each participant's ConfirmationWeight to
// min(current, preserved(event) + measured(event)) and records the event-local
// slashing ratio.
func (am AppModule) evaluateConfirmation(
	ctx context.Context,
	event *types.ConfirmationPoCEvent,
	epochGroupData *types.EpochGroupData,
	pocParams *types.PocParams,
) error {
	coefficients := ModelCoefficients(pocParams)

	confirmationParticipants := am.updateConfirmationWeightsV2(ctx, event)
	measured := make(map[string]int64, len(confirmationParticipants))
	for _, p := range confirmationParticipants {
		p.Weight = AggregateConsensusWeight(ExtractModelWeights(p), coefficients)
		measured[p.Index] = p.Weight
	}

	// Missing snapshot collapses the preserved side to zero; min-take and slashing
	// then behave as if no node was preserved for this event.
	preservedSnapshot, _, err := am.keeper.GetPreservedNodesSnapshot(ctx)
	if err != nil {
		am.LogWarn("evaluateConfirmation: failed to read preserved snapshot, using empty set",
			types.PoC, "triggerHeight", event.TriggerHeight, "error", err)
	}

	preserved, notPreserved, err := am.partitionWeightByPreservation(ctx, event.EpochIndex, coefficients, &preservedSnapshot)
	if err != nil {
		return fmt.Errorf("evaluateConfirmation: failed to partition weights: %w", err)
	}

	updated, ratios := foldEventReadings(epochGroupData, measured, preserved, notPreserved)
	if updated {
		am.LogInfo("evaluateConfirmation: confirmation weights lowered", types.PoC,
			"epochIndex", event.EpochIndex,
			"triggerHeight", event.TriggerHeight)
	}

	for _, vw := range epochGroupData.ValidationWeights {
		addr := vw.MemberAddress
		ratio, ok := ratios[addr]
		if !ok {
			continue
		}
		participant, found := am.keeper.GetParticipant(ctx, addr)
		if !found {
			am.LogWarn("evaluateConfirmation: participant not found for slashing record", types.PoC,
				"address", addr)
			continue
		}
		participant.CurrentEpochStats.ConfirmationPoCRatio = ratio
		am.keeper.SetParticipant(ctx, participant)
	}

	if updated {
		am.keeper.SetEpochGroupData(ctx, *epochGroupData)
		am.LogInfo("evaluateConfirmation: saved updated EpochGroupData", types.PoC,
			"epochIndex", event.EpochIndex)
	}

	return nil
}

// foldEventReadings applies this event's reading (preserved + measured) to every
// ValidationWeight via min-take and returns the per-participant slashing ratio.
// Pure: no keeper reads, no logging. Caller persists the result.
func foldEventReadings(
	epochGroupData *types.EpochGroupData,
	measured, preserved, notPreserved map[string]int64,
) (updated bool, ratios map[string]*types.Decimal) {
	ratios = make(map[string]*types.Decimal, len(epochGroupData.ValidationWeights))
	for i, vw := range epochGroupData.ValidationWeights {
		addr := vw.MemberAddress
		reading := preserved[addr] + measured[addr]
		if reading < vw.ConfirmationWeight {
			epochGroupData.ValidationWeights[i].ConfirmationWeight = reading
			updated = true
		}

		totalExpected := preserved[addr] + notPreserved[addr]
		if totalExpected == 0 {
			ratios[addr] = types.DecimalFromDecimal(decimal.NewFromInt(1))
			continue
		}
		ratio := decimal.NewFromInt(reading).Div(decimal.NewFromInt(totalExpected))
		ratio = decimal.Min(ratio.Div(pocDeviationCoeff), decimal.NewFromInt(1))
		ratios[addr] = types.DecimalFromDecimal(ratio)
	}
	return updated, ratios
}

// updateConfirmationWeightsV2 calculates confirmation weights using off-chain store commits
func (am AppModule) updateConfirmationWeightsV2(
	ctx context.Context,
	event *types.ConfirmationPoCEvent,
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

	for key := range storeCommits {
		participantAddress := key.ParticipantAddress
		participant, ok := am.keeper.GetParticipant(ctx, participantAddress)
		if !ok {
			am.LogWarn("updateConfirmationWeightsV2: Participant not found", types.PoC,
				"address", participantAddress,
				"modelId", key.ModelID)
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
		accAddr, err := utils.OperatorAddressToAccAddress(addr)
		if err != nil {
			am.LogWarn("calculateConfirmationPoCWeight: Failed to convert guardian address", types.PoC,
				"operatorAddress", addr, "error", err)
			continue
		}
		guardianSet[accAddr] = true
	}

	guardianAccAddrs := make([]string, 0, len(guardianSet))
	for addr := range guardianSet {
		guardianAccAddrs = append(guardianAccAddrs, addr)
	}
	am.LogInfo("calculateConfirmationPoCWeight: Resolved guardian addresses", types.PoC,
		"guardianEnabled", guardianEnabled,
		"guardianAccAddrs", guardianAccAddrs)

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		am.LogError("updateConfirmationWeightsV2: failed to get params", types.PoC, "error", err)
		return nil
	}

	var appHash string
	var validationSlots int
	timeNormalizationFactor := mathsdk.LegacyOneDec()

	snapshot, snapshotFound, _ := am.keeper.GetPoCValidationSnapshot(ctx, event.TriggerHeight)
	if snapshotFound {
		if params.PocParams.ValidationSlots > 0 {
			appHash = snapshot.AppHash
			validationSlots = int(params.PocParams.ValidationSlots)
		}
		if params.PocParams.PocNormalizationEnabled {
			timeNormalizationFactor = CalculateTimeNormalizationFactor(
				snapshot.GenerationStartTimestamp,
				snapshot.ExchangeEndTimestamp,
				params.EpochParams.PocStageDuration,
				params.EpochParams.PocExchangeDuration,
			)
		}
		am.LogInfo("updateConfirmationWeightsV2: Using validation snapshot", types.PoC,
			"appHash", appHash,
			"validationSlots", validationSlots,
			"generationStartTimestamp", snapshot.GenerationStartTimestamp,
			"exchangeEndTimestamp", snapshot.ExchangeEndTimestamp,
			"timeNormalizationFactor", timeNormalizationFactor.String(),
			"pocNormalizationEnabled", params.PocParams.PocNormalizationEnabled,
		)
	} else {
		am.LogWarn("updateConfirmationWeightsV2: Validation snapshot not found", types.PoC,
			"triggerHeight", event.TriggerHeight,
		)
	}

	// Load per-model voting powers from snapshot
	modelVotingPowers := make(map[string]map[string]int64)
	totalNetworkWeight := int64(0)
	if snapshotFound {
		for _, mvw := range snapshot.ModelVotingPowers {
			modelVotingPowers[mvw.ModelId] = types.VotingPowerSliceToMap(mvw.VotingPowers)
		}
		totalNetworkWeight = snapshot.TotalNetworkWeight
	}

	calculator := NewPoCWeightCalculator(
		modelVotingPowers,
		totalNetworkWeight,
		storeCommits,
		weightDistributions,
		validationsV2,
		params.PocParams,
		participants,
		seeds,
		event.TriggerHeight,
		am,
		timeNormalizationFactor,
		guardianEnabled,
		guardianSet,
		appHash,
		validationSlots,
	)

	// Calculate confirmation weights
	return calculator.Calculate()
}

// partitionWeightByPreservation splits each participant's coefficient-adjusted
// MLNode weight into preserved / not-preserved portions based on the given
// snapshot. The sum of the two maps equals the participant's total expected
// weight for the episode.
func (am AppModule) partitionWeightByPreservation(
	ctx context.Context,
	epochId uint64,
	coefficients map[string]mathsdk.LegacyDec,
	preservedSnapshot *types.PreservedNodesSnapshot,
) (preserved, notPreserved map[string]int64, err error) {
	participants, found := am.keeper.GetActiveParticipants(ctx, epochId)
	if !found {
		am.LogError("partitionWeightByPreservation: Active participants not found", types.PoC, "epochId", epochId)
		return nil, nil, errors.New("partitionWeightByPreservation: active participants not found. epochId: " + strconv.FormatUint(epochId, 10))
	}

	preserved = make(map[string]int64, len(participants.Participants))
	notPreserved = make(map[string]int64, len(participants.Participants))

	for _, p := range participants.Participants {
		var preservedTotal, notPreservedTotal int64
		for i, nodeArray := range p.MlNodes {
			if nodeArray == nil {
				continue
			}
			if i >= len(p.Models) || p.Models[i] == "" {
				continue
			}
			modelId := p.Models[i]
			preservedNodeSet := keeper.PreservedNodeSetByModel(preservedSnapshot, modelId)
			coeff, ok := coefficients[modelId]
			if !ok {
				coeff = mathsdk.LegacyOneDec()
			}
			var rawPreserved, rawNotPreserved int64
			for _, mlNode := range nodeArray.MlNodes {
				if mlNode == nil {
					continue
				}
				if keeper.IsPreservedNode(preservedNodeSet, p.Index, mlNode.NodeId) {
					rawPreserved += mlNode.PocWeight
				} else {
					rawNotPreserved += mlNode.PocWeight
				}
			}
			preservedTotal += coeff.MulInt64(rawPreserved).TruncateInt64()
			notPreservedTotal += coeff.MulInt64(rawNotPreserved).TruncateInt64()
		}
		preserved[p.Index] = preservedTotal
		notPreserved[p.Index] = notPreservedTotal
	}

	return preserved, notPreserved, nil
}
