package accounting

import (
	"context"
	"errors"
	"log"
)

var (
	ErrPurgeEpochRequired = errors.New("accounting purge: epoch is required")
	ErrPurgeNoTracker     = errors.New("accounting purge: accounting is not configured")
	// ErrEpochPurged is returned by RegisterEscrow once an epoch has been purged.
	// A live session's later facts are discarded quietly instead.
	ErrEpochPurged = errors.New("epoch is purged")
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
		if escrow.Meta.CreationEpoch != epoch {
			continue
		}
		delete(t.escrows, id)
		delete(t.dirty, id)
		// Postgres deletes only the ids named here. SQLite rewrites the table
		// from whatever is still in memory, so a missing entry is invisible
		// there and the row survives a Postgres reopen.
		if t.pendingDeletes == nil {
			t.pendingDeletes = make(map[string]struct{})
		}
		t.pendingDeletes[id] = struct{}{}
		t.quietEscrowLocked(id)
		removed++
	}
	if removed > 0 {
		// The epoch tombstone is what a peer, still holding these escrows, reads
		// on its next flush. Naming the ids alone deletes the rows once; the
		// peer's following upsert would write them back.
		if t.pendingPurgeEpochs == nil {
			t.pendingPurgeEpochs = make(map[uint64]struct{})
		}
		t.pendingPurgeEpochs[epoch] = struct{}{}
		t.notePurgedEpochLocked(epoch)
	}
	// A failed flush leaves the deletes armed and the escrows already gone, so a
	// retry removes nothing. Flush anyway while work is still pending; an epoch
	// that was never here has nothing to write.
	pending := len(t.pendingDeletes) + len(t.pendingPurgeEpochs)
	t.mu.Unlock()
	if removed > 0 {
		log.Printf("accounting purge: dropped %d escrow(s) in epoch %d", removed, epoch)
	}
	if removed == 0 && pending == 0 {
		return 0, nil
	}
	// The snapshot tick is the only other writer, so without this the next restart reloads the epoch
	// the operator just discarded.
	return removed, t.Flush(ctx)
}
