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
	pocV2Enabled               bool // cached from PocParams.PocV2Enabled, default false (set from chain)
	confirmationPocV2Enabled   bool // cached from PocParams.ConfirmationPocV2Enabled, default true
}

type BlockInfo struct {
	Height int64
	Hash   string
}

// NewChainPhaseTracker creates a new ChainPhaseTracker instance.
func NewChainPhaseTracker() *ChainPhaseTracker {
	return &ChainPhaseTracker{
		pocV2Enabled:             false, // Start false, will be set from chain params
		confirmationPocV2Enabled: true,  // V2 is default going forward
	}
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
	PocV2Enabled               bool // true = V2 (off-chain), false = V1 (on-chain batches)
	ConfirmationPocV2Enabled   bool // true = Confirmation PoC uses V2, enables migration mode
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
		PocV2Enabled:               t.pocV2Enabled,
		ConfirmationPocV2Enabled:   t.confirmationPocV2Enabled,
	}
}

// To be deleted once you refactor validation
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

// UpdatePocV2Enabled updates the cached poc_v2_enabled flag from governance params.
func (t *ChainPhaseTracker) UpdatePocV2Enabled(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.pocV2Enabled = enabled
}

// IsPoCv2Enabled returns the cached poc_v2_enabled flag.
// Returns true by default if not explicitly set.
func (t *ChainPhaseTracker) IsPoCv2Enabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.pocV2Enabled
}

// UpdateConfirmationPocV2Enabled updates the cached confirmation_poc_v2_enabled flag.
func (t *ChainPhaseTracker) UpdateConfirmationPocV2Enabled(enabled bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.confirmationPocV2Enabled = enabled
}

// IsConfirmationPoCv2Enabled returns the cached confirmation_poc_v2_enabled flag.
// Returns true by default if not explicitly set.
func (t *ChainPhaseTracker) IsConfirmationPoCv2Enabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.confirmationPocV2Enabled
}
