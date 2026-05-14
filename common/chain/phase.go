package chain

import "sync/atomic"

// Phase tracks the current epoch and block height, updated by the event listener.
// It is safe for concurrent access.
type Phase struct {
	epochID     atomic.Uint64
	blockHeight atomic.Int64
}

// EpochID returns the current epoch index.
func (p *Phase) EpochID() uint64 { return p.epochID.Load() }

// BlockHeight returns the latest observed block height.
func (p *Phase) BlockHeight() int64 { return p.blockHeight.Load() }

// Update sets epoch and block height. Called on every new block.
func (p *Phase) Update(epochID uint64, blockHeight int64) {
	p.epochID.Store(epochID)
	p.blockHeight.Store(blockHeight)
}

// SetBlockHeight updates only the block height, used when the epoch query fails.
func (p *Phase) SetBlockHeight(h int64) { p.blockHeight.Store(h) }
