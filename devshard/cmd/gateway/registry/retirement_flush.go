package registry

import (
	"context"
	"time"
)

// retirementFlushTimeout gives the host about as long to take the last diff as it gets to acknowledge a
// request: the close can run on the goroutine of the request that released the escrow.
const retirementFlushTimeout = 5 * time.Second

// flushPendingAtRetirement carries the transactions an escrow gossiped but no request composed into one
// last diff, for the escrows holding one. See routing.md, "The last diff of a retiring escrow".
func (r *Registry) flushPendingAtRetirement(entry *escrowEntry) {
	if entry.session == nil {
		return
	}
	pending := len(entry.session.PendingTxs())
	if pending == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), retirementFlushTimeout)
	defer cancel()
	err := entry.session.SendPendingDiff(ctx)
	if r.narrator != nil {
		r.narrator.RetirementPendingFlushed(entry.id, pending, err)
	}
}
