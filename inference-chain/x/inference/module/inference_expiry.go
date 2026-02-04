package inference

import (
	"context"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// PoCTimeRange represents the block range of a PoC or CPoC event
type PoCTimeRange struct {
	StartBlock int64
	EndBlock   int64
	IsActive   bool
	IsCPoC     bool // true if this is a Confirmation PoC, false if regular PoC
}

// GetLatestPoCOrCPoCRangeWithData returns the block range of the most recent PoC or CPoC event
// Reuses already-loaded data to avoid redundant reads
// Logic:
// 1. Check if current epoch's PoC has started - if yes, we're in that PoC
// 2. If not in current PoC, check for latest CPoC event
// 3. If no relevant CPoC, return current epoch's PoC range
func (am AppModule) GetLatestPoCOrCPoCRangeWithData(
	ctx context.Context,
	blockHeight int64,
	currentEpoch *types.Epoch,
	params *types.Params,
) (*PoCTimeRange, error) {
	if currentEpoch == nil {
		return &PoCTimeRange{IsActive: false}, nil
	}

	epochParams := params.EpochParams
	epochContext := types.NewEpochContext(*currentEpoch, *epochParams)

	// 1. Check if next PoC started
	if epochContext.IsNextPoCStart(blockHeight) {
		// We're at the start of the next epoch's PoC
		// Return range from start of next PoC to current block
		nextPoCStart := epochContext.NextPoCStart()
		return &PoCTimeRange{
			StartBlock: nextPoCStart,
			EndBlock:   blockHeight, // Can't predict future, use current block
			IsActive:   true,
			IsCPoC:     false,
		}, nil
	}

	// 2. Check for Confirmation PoC events
	cpocEvents, err := am.keeper.GetAllConfirmationPoCEventsForEpoch(ctx, currentEpoch.Index)
	if err != nil {
		return nil, err
	}

	// Find the latest CPoC event (highest event sequence number)
	var latestCPoC *types.ConfirmationPoCEvent
	for i := range cpocEvents {
		event := &cpocEvents[i]
		if latestCPoC == nil || event.EventSequence > latestCPoC.EventSequence {
			latestCPoC = event
		}
	}

	// If we have a latest CPoC, check if it's relevant (active or recently completed)
	if latestCPoC != nil {
		cpocEndBlock := latestCPoC.GetValidationEnd(epochParams)

		isActive := blockHeight >= latestCPoC.GenerationStartHeight && blockHeight <= cpocEndBlock
		return &PoCTimeRange{
			StartBlock: latestCPoC.GenerationStartHeight,
			EndBlock:   cpocEndBlock,
			IsActive:   isActive,
			IsCPoC:     true,
		}, nil
	}

	// 3. No active PoC or CPoC - return current epoch's PoC range as default
	return &PoCTimeRange{
		StartBlock: epochContext.StartOfPoC(),
		EndBlock:   epochContext.EndOfPoCValidation(),
		IsActive:   false,
		IsCPoC:     false,
	}, nil
}

// InferenceExpiryContext holds context information for efficient inference expiry processing
type InferenceExpiryContext struct {
	CurrentBlockHeight         int64
	TimeoutDuration            int64
	PoCRange                   *PoCTimeRange
	CurrentEpoch               *types.Epoch
	PreviousEpoch              *types.Epoch
	EpochParams                *types.EpochParams
	CurrentActiveParticipants  *types.ActiveParticipants // Cached for current epoch
	PreviousActiveParticipants *types.ActiveParticipants // Cached for previous epoch (lazy loaded)
}

// NewInferenceExpiryContextWithEpoch creates a context for processing inference expiries efficiently
// Reuses already-loaded params and epoch data to avoid redundant reads and unmarshalling
func (am AppModule) NewInferenceExpiryContextWithEpoch(
	ctx context.Context,
	blockHeight int64,
	currentEpoch *types.Epoch,
	params *types.Params,
) (*InferenceExpiryContext, error) {
	pocRange, err := am.GetLatestPoCOrCPoCRangeWithData(ctx, blockHeight, currentEpoch, params)
	if err != nil {
		return nil, err
	}

	expirationBlocks := params.ValidationParams.ExpirationBlocks

	return &InferenceExpiryContext{
		CurrentBlockHeight:         blockHeight,
		TimeoutDuration:            expirationBlocks,
		PoCRange:                   pocRange,
		CurrentEpoch:               currentEpoch,
		PreviousEpoch:              nil, // Lazy loaded in GetEpochForInference if needed
		EpochParams:                params.EpochParams,
		CurrentActiveParticipants:  nil, // Lazy loaded in GetEpochForInference if needed
		PreviousActiveParticipants: nil, // Lazy loaded in GetEpochForInference if needed
	}, nil
}

// IsBlockInPoCRange checks if a block height is within the PoC/CPoC range
func (ec *InferenceExpiryContext) IsBlockInPoCRange(blockHeight int64) bool {
	if ec.PoCRange == nil || !ec.PoCRange.IsActive && ec.PoCRange.StartBlock == 0 {
		return false
	}
	return blockHeight >= ec.PoCRange.StartBlock && blockHeight <= ec.PoCRange.EndBlock
}

// GetEpochForInference determines which epoch to use for checking node availability
// Returns the epoch when the inference was assigned (started), since that's when
// the executor was selected from active participants
// Lazily loads previous epoch data only when needed for precision
func (ec *InferenceExpiryContext) GetEpochForInference(ctx context.Context, keeper keeper.Keeper, inference types.Inference) *types.Epoch {
	if ec.CurrentEpoch == nil {
		return nil
	}

	// Check if inference started before the current epoch became effective
	// The epoch becomes effective at SetNewValidators block
	epochContext := types.NewEpochContext(*ec.CurrentEpoch, *ec.EpochParams)

	// If inference started after current epoch became effective, use current epoch
	if inference.StartBlockHeight >= epochContext.SetNewValidators() {
		// Lazy load current epoch's active participants if not already loaded
		if ec.CurrentActiveParticipants == nil && ec.CurrentEpoch != nil {
			activeParticipants, found := keeper.GetActiveParticipants(ctx, ec.CurrentEpoch.Index)
			if found {
				ec.CurrentActiveParticipants = &activeParticipants
			}
		}
		return ec.CurrentEpoch
	}

	// Inference started before current epoch - need previous epoch
	// Lazy load previous epoch and its active participants if not already loaded
	if ec.PreviousEpoch == nil {
		previousEpoch, found := keeper.GetPreviousEpoch(ctx)
		if found && previousEpoch != nil {
			ec.PreviousEpoch = previousEpoch

			// Also load previous epoch's active participants
			activeParticipants, found := keeper.GetActiveParticipants(ctx, previousEpoch.Index)
			if found {
				ec.PreviousActiveParticipants = &activeParticipants
			}
		}
	}

	// Return previous epoch if we successfully loaded it, otherwise fall back to current
	if ec.PreviousEpoch != nil {
		return ec.PreviousEpoch
	}
	return ec.CurrentEpoch
}

// ShouldCheckPreserveNode determines if we should check for preserve nodes instead of regular mlnodes
// This is true when the inference started or timed out inside a PoC/CPoC
func (ec *InferenceExpiryContext) ShouldCheckPreserveNode(inference types.Inference) bool {
	startBlock := inference.StartBlockHeight
	timeoutBlock := ec.CurrentBlockHeight

	// Check if start or timeout is in PoC range
	return ec.IsBlockInPoCRange(startBlock) || ec.IsBlockInPoCRange(timeoutBlock)
}

// HasNodeForModel checks if a participant has the required node for the model
// Checks preserve nodes if in PoC, otherwise checks regular mlnodes
// Uses cached active participants from the expiry context to avoid repeated reads
func (am AppModule) HasNodeForModel(
	participantAddr string,
	modelId string,
	checkPreserveNode bool,
	activeParticipants *types.ActiveParticipants,
) bool {
	if activeParticipants == nil {
		return false
	}

	// Find the participant
	var participant *types.ActiveParticipant
	for _, p := range activeParticipants.Participants {
		if p.Index == participantAddr {
			participant = p
			break
		}
	}

	if participant == nil {
		return false
	}

	// Find the model index in the Models array
	modelIndex := -1
	for i, model := range participant.Models {
		if model == modelId {
			modelIndex = i
			break
		}
	}

	// Model not found in participant's models
	if modelIndex == -1 || modelIndex >= len(participant.MlNodes) {
		return false
	}

	modelMLNodes := participant.MlNodes[modelIndex]
	if modelMLNodes == nil || len(modelMLNodes.MlNodes) == 0 {
		return false
	}

	// Check if participant has the model
	if checkPreserveNode {
		// Check preserve nodes (POC_SLOT = true, which is index 1 in TimeslotAllocation)
		for _, mlNode := range modelMLNodes.MlNodes {
			// Check if this is a preserve node (POC_SLOT = true)
			if len(mlNode.TimeslotAllocation) > 1 && mlNode.TimeslotAllocation[1] {
				return true
			}
		}
		return false
	} else {
		// Any mlnode for this model is sufficient
		return true
	}
}
