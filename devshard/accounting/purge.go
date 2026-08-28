package accounting

import (
	"context"
	"errors"
)

var (
	ErrPurgeEpochRequired = errors.New("accounting purge: epoch is required")
	ErrPurgeNoTracker     = errors.New("accounting purge: accounting is not configured")
)

// PurgeEpoch drops every escrow created in one epoch and reports how many went. Accounting is the
// gateway's own record rather than chain state, so an epoch recorded through a bug can be discarded
// without touching anything that settled. Epoch 0 is refused so there is no shape of this call that
// clears the whole ledger.
func (t *Tracker) PurgeEpoch(ctx context.Context, epoch uint64) (int, error) {
	if t == nil {
		return 0, ErrPurgeNoTracker
	}
	if epoch == 0 {
		return 0, ErrPurgeEpochRequired
	}
	t.mu.Lock()
	removed := 0
	for id, escrow := range t.escrows {
		if escrow.Meta.CreationEpoch == epoch {
			delete(t.escrows, id)
			removed++
		}
	}
	t.mu.Unlock()
	if removed == 0 {
		return 0, nil
	}
	// The snapshot tick is the only other writer, so without this the next restart reloads the epoch
	// the operator just discarded.
	return removed, t.Flush(ctx)
}
