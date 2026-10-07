package chain

import "sync/atomic"

// Phase tracks the current chain epoch. In standalone devshardd it is updated
// from runtime-config OnEpochChange (long-poll or chain-poll fallback), not from
// per-block Comet subscriptions.
// It is safe for concurrent access.
type Phase struct {
	epochID atomic.Uint64
}

// EpochID returns the current epoch index.
func (p *Phase) EpochID() uint64 { return p.epochID.Load() }

// SetEpoch stores the current epoch index.
func (p *Phase) SetEpoch(epochID uint64) { p.epochID.Store(epochID) }
