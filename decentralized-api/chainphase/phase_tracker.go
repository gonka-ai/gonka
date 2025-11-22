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

	currentBlock BlockInfo
	// latestEpoch is not the effective epoch, but the latest epoch that has been set
	// so if PoC has just started it will be effectiveEpoch + 1
	latestEpoch                *types.Epoch
	currentEpochParams         *types.EpochParams
	currentIsSynced            bool
	activeConfirmationPoCEvent *types.ConfirmationPoCEvent
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
func (t *ChainPhaseTracker) Update(block BlockInfo, epoch *types.Epoch, params *types.EpochParams, isSynced bool, confirmationPoCEvent *types.ConfirmationPoCEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.currentBlock = block
	t.latestEpoch = epoch
	t.currentEpochParams = params
	t.currentIsSynced = isSynced
	t.activeConfirmationPoCEvent = confirmationPoCEvent
}

type EpochState struct {
	LatestEpoch                types.EpochContext
	CurrentBlock               BlockInfo
	CurrentPhase               types.EpochPhase
	IsSynced                   bool
	ActiveConfirmationPoCEvent *types.ConfirmationPoCEvent
}

func (es *EpochState) IsNilOrNotSynced() bool {
	return es == nil || !es.IsSynced
}

func (t *ChainPhaseTracker) GetCurrentEpochState() *EpochState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if t.latestEpoch == nil || t.currentEpochParams == nil {
		return nil
	}

	// Create a new context for this specific query to ensure consistency
	ec := types.NewEpochContext(*t.latestEpoch, *t.currentEpochParams)
	phase := ec.GetCurrentPhase(t.currentBlock.Height)

	return &EpochState{
		LatestEpoch:                ec,
		CurrentBlock:               t.currentBlock,
		CurrentPhase:               phase,
		IsSynced:                   t.currentIsSynced,
		ActiveConfirmationPoCEvent: t.activeConfirmationPoCEvent,
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
