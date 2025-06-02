package chainphase

import (
	"context"
	"sync"

	"decentralized-api/cosmosclient"
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

// Phase represents the current phase of the blockchain
type Phase int

const (
	PhaseUnknown Phase = iota
	PhaseInference
	PhasePoC
	PhasePoCValidation
	PhaseValidatorSelection
	PhaseMoneyClaim
)

// String returns the string representation of a Phase
func (p Phase) String() string {
	switch p {
	case PhaseUnknown:
		return "Unknown"
	case PhaseInference:
		return "Inference"
	case PhasePoC:
		return "PoC"
	case PhasePoCValidation:
		return "PoCValidation"
	case PhaseValidatorSelection:
		return "ValidatorSelection"
	case PhaseMoneyClaim:
		return "MoneyClaim"
	default:
		return "Invalid"
	}
}

// ChainPhaseTracker tracks the current phase of the blockchain based on block height
type ChainPhaseTracker struct {
	mu sync.RWMutex

	currentPhase        Phase
	currentBlockHeight  int64
	currentEpochParams  *types.EpochParams
	pocStartBlockHash   string
	pocStartBlockHeight int64
	isSynced            bool

	// For self-sufficient epoch params querying
	cosmosClient cosmosclient.InferenceCosmosClient
	ctx          context.Context
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance
func NewChainPhaseTracker(ctx context.Context, cosmosClient cosmosclient.InferenceCosmosClient) *ChainPhaseTracker {
	return &ChainPhaseTracker{
		currentPhase:        PhaseUnknown,
		currentBlockHeight:  0,
		currentEpochParams:  nil,
		pocStartBlockHash:   "",
		pocStartBlockHeight: 0,
		isSynced:            false,
		cosmosClient:        cosmosClient,
		ctx:                 ctx,
	}
}

// UpdateBlockHeight updates the tracker with a new block height
func (t *ChainPhaseTracker) UpdateBlockHeight(height int64, currentBlockHash string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlockHeight = height

	// Query epoch params if we don't have them or periodically refresh
	if t.currentEpochParams == nil || height%100 == 0 { // Refresh every 100 blocks
		t.refreshEpochParams()
	}

	if t.currentEpochParams == nil {
		t.currentPhase = PhaseUnknown
		return
	}

	// Determine the current phase based on block height
	previousPhase := t.currentPhase
	newPhase := t.calculatePhase(height, t.currentEpochParams)
	t.currentPhase = newPhase

	// Handle PoC start tracking with resilience
	if t.isInPoCStage(height, t.currentEpochParams) {
		// Calculate what the PoC start height should be for this epoch
		expectedPoCStartHeight := t.calculatePoCStartHeight(height, t.currentEpochParams)

		// If we don't have PoC parameters or they're from a different PoC cycle, update them
		if t.pocStartBlockHeight != expectedPoCStartHeight {
			t.pocStartBlockHeight = expectedPoCStartHeight

			// If this is the exact start block, we can use the current hash
			if height == expectedPoCStartHeight {
				t.pocStartBlockHash = currentBlockHash
			} else {
				// Otherwise, we need to query the chain for the hash at the start height
				t.pocStartBlockHash = t.queryBlockHashAtHeight(expectedPoCStartHeight)
			}

			logging.Info("Updated PoC start parameters", types.Stages,
				"pocStartHeight", t.pocStartBlockHeight,
				"pocStartHash", t.pocStartBlockHash,
				"currentHeight", height)
		}
	}

	// Clear PoC parameters when leaving PoC phase
	if previousPhase == PhasePoC && newPhase != PhasePoC {
		t.pocStartBlockHash = ""
		t.pocStartBlockHeight = 0
	}
}

// refreshEpochParams queries the chain for the latest epoch parameters
func (t *ChainPhaseTracker) refreshEpochParams() {
	queryClient := t.cosmosClient.NewInferenceQueryClient()
	params, err := queryClient.Params(t.ctx, &types.QueryParamsRequest{})
	if err != nil {
		logging.Error("Failed to query epoch params in ChainPhaseTracker", types.System, "error", err)
		return
	}
	t.currentEpochParams = params.Params.EpochParams
	logging.Debug("Refreshed epoch params in ChainPhaseTracker", types.System)
}

// calculatePoCStartHeight calculates the PoC start height for the current epoch
func (t *ChainPhaseTracker) calculatePoCStartHeight(currentHeight int64, params *types.EpochParams) int64 {
	// Shift the height to account for epoch shift
	shiftedHeight := currentHeight + params.EpochShift

	// Calculate which epoch we're in
	epochNumber := shiftedHeight / params.EpochLength

	// Calculate the start of this epoch and then the start of PoC
	epochStartShifted := epochNumber * params.EpochLength
	pocStartShifted := epochStartShifted + params.GetStartOfPoCStage()

	// Unshift to get the actual block height
	return pocStartShifted - params.EpochShift
}

// queryBlockHashAtHeight queries the chain for the block hash at a specific height
func (t *ChainPhaseTracker) queryBlockHashAtHeight(height int64) string {
	// This would require a tendermint client to query historical blocks
	// For now, we'll log a warning and return empty string
	// In a full implementation, you'd use the tendermint RPC client
	logging.Warn("Need to query block hash at historical height - not implemented yet", types.System,
		"height", height)
	return ""
}

// calculatePhase determines the current phase based on block height and epoch params
func (t *ChainPhaseTracker) calculatePhase(height int64, params *types.EpochParams) Phase {
	// We need to check the phases in order, as some conditions might overlap

	// Check if we're in PoC stage
	if t.isInPoCStage(height, params) {
		return PhasePoC
	}

	// Check if we're in PoC validation stage
	if t.isInPoCValidationStage(height, params) {
		return PhasePoCValidation
	}

	// Check if we're in validator selection stage
	if params.IsSetNewValidatorsStage(height) {
		return PhaseValidatorSelection
	}

	// Check if we're in money claim stage
	if params.IsClaimMoneyStage(height) {
		return PhaseMoneyClaim
	}

	// Default to inference phase
	return PhaseInference
}

// isInPoCStage checks if the current block height is within the PoC stage
func (t *ChainPhaseTracker) isInPoCStage(height int64, params *types.EpochParams) bool {
	// Shift the height to account for epoch shift
	shiftedHeight := height + params.EpochShift

	// Calculate position within the epoch
	positionInEpoch := shiftedHeight % params.EpochLength

	// PoC starts at position 0 and ends at GetEndOfPoCStage()
	pocStart := params.GetStartOfPoCStage()
	pocEnd := params.GetEndOfPoCStage()

	return positionInEpoch >= pocStart && positionInEpoch < pocEnd
}

// isInPoCValidationStage checks if the current block height is within the PoC validation stage
func (t *ChainPhaseTracker) isInPoCValidationStage(height int64, params *types.EpochParams) bool {
	// Shift the height to account for epoch shift
	shiftedHeight := height + params.EpochShift

	// Calculate position within the epoch
	positionInEpoch := shiftedHeight % params.EpochLength

	// PoC validation starts and ends at specific positions
	valStart := params.GetStartOfPoCValidationStage()
	valEnd := params.GetEndOfPoCValidationStage()

	return positionInEpoch >= valStart && positionInEpoch < valEnd
}

// GetCurrentPhase returns the current phase and associated block height
func (t *ChainPhaseTracker) GetCurrentPhase() (phase Phase, blockHeight int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.currentPhase, t.currentBlockHeight
}

// GetCurrentEpoch returns the current epoch number based on block height
func (t *ChainPhaseTracker) GetCurrentEpoch() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.currentEpochParams == nil || t.currentBlockHeight == 0 {
		return 0
	}

	// Calculate epoch number from block height
	shiftedHeight := t.currentBlockHeight + t.currentEpochParams.EpochShift
	epochNumber := uint64(shiftedHeight / t.currentEpochParams.EpochLength)

	return epochNumber
}

// GetPoCParameters returns PoC start parameters if currently in PoC phase
func (t *ChainPhaseTracker) GetPoCParameters() (startBlockHeight int64, startBlockHash string, isInPoC bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.currentPhase == PhasePoC && t.pocStartBlockHeight > 0 {
		return t.pocStartBlockHeight, t.pocStartBlockHash, true
	}

	return 0, "", false
}

// SetSyncStatus updates the sync status of the chain
func (t *ChainPhaseTracker) SetSyncStatus(synced bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.isSynced = synced
}

// IsSynced returns whether the chain is currently synced
func (t *ChainPhaseTracker) IsSynced() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.isSynced
}

// IsInPoCPhase returns true if currently in PoC phase
func (t *ChainPhaseTracker) IsInPoCPhase() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.currentPhase == PhasePoC
}

// ShouldBeInInferenceMode returns true if nodes should be in inference mode
func (t *ChainPhaseTracker) ShouldBeInInferenceMode() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Nodes should be in inference mode during inference phase
	// or when we don't know the phase (unknown)
	return t.currentPhase == PhaseInference || t.currentPhase == PhaseUnknown
}

// GetIntendedNodeStatus returns the intended hardware node status based on current phase
func (t *ChainPhaseTracker) GetIntendedNodeStatus() types.HardwareNodeStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()

	switch t.currentPhase {
	case PhasePoC:
		return types.HardwareNodeStatus_POC
	case PhaseInference:
		return types.HardwareNodeStatus_INFERENCE
	default:
		// Default to inference for other phases
		return types.HardwareNodeStatus_INFERENCE
	}
}
