package chainphase

import (
	"sync"

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
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance
func NewChainPhaseTracker() *ChainPhaseTracker {
	return &ChainPhaseTracker{
		currentPhase:        PhaseUnknown,
		currentBlockHeight:  0,
		currentEpochParams:  nil,
		pocStartBlockHash:   "",
		pocStartBlockHeight: 0,
		isSynced:            false,
	}
}

// UpdateBlockHeight updates the tracker with a new block height and epoch params
func (t *ChainPhaseTracker) UpdateBlockHeight(height int64, epochParams *types.EpochParams, currentBlockHash string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlockHeight = height
	t.currentEpochParams = epochParams

	if epochParams == nil {
		t.currentPhase = PhaseUnknown
		return
	}

	// Determine the current phase based on block height
	previousPhase := t.currentPhase
	newPhase := t.calculatePhase(height, epochParams)
	t.currentPhase = newPhase

	// Handle PoC start tracking
	if epochParams.IsStartOfPoCStage(height) {
		t.pocStartBlockHash = currentBlockHash
		t.pocStartBlockHeight = height
	}

	// Clear PoC parameters when leaving PoC phase
	if previousPhase == PhasePoC && newPhase != PhasePoC {
		t.pocStartBlockHash = ""
		t.pocStartBlockHeight = 0
	}
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
