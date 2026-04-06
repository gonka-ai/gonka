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

// SetEpoch updates the tracked epoch.
func (p *Phase) SetEpoch(id uint64) { p.epochID.Store(id) }

// SetBlockHeight updates the tracked block height.
func (p *Phase) SetBlockHeight(h int64) { p.blockHeight.Store(h) }
