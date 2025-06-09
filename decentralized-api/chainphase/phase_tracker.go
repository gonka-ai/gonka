package chainphase

import (
	"sync"

	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type ChainPhaseTracker struct {
	mu sync.RWMutex

	currentBlockHeight int64
	currentEpochParams *types.EpochParams
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance
func NewChainPhaseTracker() *ChainPhaseTracker {
	return &ChainPhaseTracker{
		currentBlockHeight: 0,
		currentEpochParams: nil,
	}
}

func (t *ChainPhaseTracker) GetEpochParams() *types.EpochParams {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.currentEpochParams
}

func (t *ChainPhaseTracker) UpdateEpochParams(params types.EpochParams) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentEpochParams = &params
}

// UpdateBlockHeight updates the tracker with a new block height
func (t *ChainPhaseTracker) UpdateBlockHeight(height int64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlockHeight = height
}

func (t *ChainPhaseTracker) GetBlockHeight() int64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.currentBlockHeight
}

// calculatePoCStartHeight calculates the PoC start height for the current epoch
func (t *ChainPhaseTracker) calculatePoCStartHeight(currentHeight int64) int64 {
	params := t.GetEpochParams()

	shiftedHeight := currentHeight + params.EpochShift

	epochNumber := shiftedHeight / params.EpochLength

	epochStartShifted := epochNumber * params.EpochLength
	pocStartShifted := epochStartShifted + params.GetStartOfPoCStage()

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

func (t *ChainPhaseTracker) GetPhase(height int64) types.EpochPhase {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.currentEpochParams.GetCurrentPhase(height)
}

func (t *ChainPhaseTracker) GetCurrentPhase() (types.EpochPhase, int64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	blockHeight := t.currentBlockHeight
	phase := t.currentEpochParams.GetCurrentPhase(blockHeight)

	return phase, blockHeight
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

type PoCParameters struct {
	PoCStartHeight     int64
	PoCStartHash       string
	CurrentBlockHeight int64
	IsInPoC            bool
}

func (t *ChainPhaseTracker) GetPoCParameters() PoCParameters {
	currentPhase, blockHeight := t.GetCurrentPhase()
	pocStartHeight := t.calculatePoCStartHeight(blockHeight)
	hash := t.queryBlockHashAtHeight(pocStartHeight)
	isInPoc := currentPhase == types.PoCGeneratePhase
	return PoCParameters{
		PoCStartHeight:     pocStartHeight,
		PoCStartHash:       hash,
		CurrentBlockHeight: blockHeight,
		IsInPoC:            isInPoc,
	}
}
