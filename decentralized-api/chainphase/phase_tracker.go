package chainphase

import (
	"sync"

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

// CalculatePoCStartHeight calculates the PoC start height for the current epoch
func (t *ChainPhaseTracker) CalculatePoCStartHeight(currentHeight int64) int64 {
	params := t.GetEpochParams()

	return CalculatePoCStartHeight(currentHeight, params)
}

func CalculatePoCStartHeight(height int64, params *types.EpochParams) int64 {
	shiftedHeight := height + params.EpochShift

	epochNumber := shiftedHeight / params.EpochLength

	epochStartShifted := epochNumber * params.EpochLength
	pocStartShifted := epochStartShifted + params.GetStartOfPoCStage()

	return pocStartShifted - params.EpochShift
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

type EpochPhaseInfo struct {
	Epoch       uint64
	BlockHeight int64
	Phase       types.EpochPhase
	EpochParams types.EpochParams
}

func (t *ChainPhaseTracker) GetCurrentEpochPhaseInfo() EpochPhaseInfo {
	t.mu.RLock()
	defer t.mu.RUnlock()

	blockHeight := t.currentBlockHeight
	phase := t.currentEpochParams.GetCurrentPhase(blockHeight)
	epoch := getEpoch(blockHeight, t.currentEpochParams)

	return EpochPhaseInfo{
		Epoch:       epoch,
		BlockHeight: blockHeight,
		Phase:       phase,
		EpochParams: *t.currentEpochParams,
	}
}

// GetCurrentEpoch returns the current epoch number based on block height
func (t *ChainPhaseTracker) GetCurrentEpoch() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.currentEpochParams == nil || t.currentBlockHeight == 0 {
		return 0
	}

	return getEpoch(t.currentBlockHeight, t.currentEpochParams)
}

func getEpoch(blockHeight int64, params *types.EpochParams) uint64 {
	shiftedHeight := blockHeight + params.EpochShift
	epochNumber := uint64(shiftedHeight / params.EpochLength)

	return epochNumber
}
