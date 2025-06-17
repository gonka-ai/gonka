package chainphase

import (
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// ChainPhaseTracker acts as a thread-safe cache for the current epoch's state.
// It is updated by the OnNewBlockDispatcher and used by other components like the Broker
// to get consistent and reliable information about the current epoch and phase.
type ChainPhaseTracker struct {
	mu sync.RWMutex

	currentBlockHeight int64
	currentEpochGroup  *types.EpochGroupData
	currentEpochParams *types.EpochParams
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance.
func NewChainPhaseTracker() *ChainPhaseTracker {
	return &ChainPhaseTracker{}
}

// Update caches the latest epoch information from the network.
// This method should be called by the OnNewBlockDispatcher on every new block.
func (t *ChainPhaseTracker) Update(height int64, group *types.EpochGroupData, params *types.EpochParams) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlockHeight = height
	t.currentEpochGroup = group
	t.currentEpochParams = params
}

// GetCurrentEpochPhaseInfo returns a snapshot of the current epoch state.
// It creates a new EpochContext on the fly to ensure calculations are based on the latest cached data.
func (t *ChainPhaseTracker) GetCurrentEpochPhaseInfo() (*EpochContext, int64, types.EpochPhase, uint64) {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.currentEpochGroup == nil || t.currentEpochParams == nil {
		return nil, t.currentBlockHeight, types.InferencePhase, 0
	}

	// Create a new context for this specific query to ensure consistency
	ctx := NewEpochContext(t.currentEpochGroup, *t.currentEpochParams)
	phase := ctx.GetCurrentPhase(t.currentBlockHeight)
	epoch := getEpoch(int64(t.currentEpochGroup.PocStartBlockHeight), t.currentEpochParams)

	return ctx, t.currentBlockHeight, phase, epoch
}

// GetCurrentEpoch returns the current epoch number based on the cached epoch start height.
func (t *ChainPhaseTracker) GetCurrentEpoch() uint64 {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.currentEpochGroup == nil || t.currentEpochParams == nil || t.currentEpochParams.EpochLength == 0 {
		return 0
	}

	return getEpoch(int64(t.currentEpochGroup.PocStartBlockHeight), t.currentEpochParams)
}

func getEpoch(pocStartBlockHeight int64, params *types.EpochParams) uint64 {
	if params == nil || params.EpochLength == 0 {
		return 0
	}
	shiftedHeight := pocStartBlockHeight + params.EpochShift
	epochNumber := uint64(shiftedHeight / params.EpochLength)

	return epochNumber
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
