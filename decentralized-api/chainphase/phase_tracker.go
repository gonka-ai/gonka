package chainphase

import (
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// ChainPhaseTracker acts as a thread-safe cache for the current Epoch's state.
// It is updated by the OnNewBlockDispatcher and used by other components like the Broker
// to get consistent and reliable information about the current Epoch and phase.
type ChainPhaseTracker struct {
	mu sync.RWMutex

	currentBlock        BlockInfo
	effectiveEpochGroup *types.EpochGroupData
	currentEpochParams  *types.EpochParams
	currentIsSynced     bool
}

type BlockInfo struct {
	Height int64
	Hash   string
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance.
func NewChainPhaseTracker() *ChainPhaseTracker {
	return &ChainPhaseTracker{}
}

// Update caches the latest Epoch information from the network.
// This method should be called by the OnNewBlockDispatcher on every new block.
func (t *ChainPhaseTracker) Update(block BlockInfo, group *types.EpochGroupData, params *types.EpochParams, isSynced bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlock = block
	t.effectiveEpochGroup = group
	t.currentEpochParams = params
	t.currentIsSynced = isSynced
}

type EpochState struct {
	CurrentEpoch types.EpochContext
	CurrentBlock BlockInfo
	CurrentPhase types.EpochPhase
	IsSynced     bool
}

func (t *ChainPhaseTracker) GetCurrentEpochState() *EpochState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.effectiveEpochGroup == nil || t.currentEpochParams == nil {
		return nil
	}

	// Create a new context for this specific query to ensure consistency
	ctx := types.NewEpochContext(t.effectiveEpochGroup, *t.currentEpochParams, t.currentBlock.Height)
	phase := ctx.GetCurrentPhase(t.currentBlock.Height)

	return &EpochState{
		CurrentEpoch: *ctx,
		CurrentBlock: t.currentBlock,
		CurrentPhase: phase,
		IsSynced:     t.currentIsSynced,
	}
}

// To de deleted once you refactor validation
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
