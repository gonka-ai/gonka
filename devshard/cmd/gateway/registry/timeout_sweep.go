package registry

import (
	"context"
	"time"

	"devshard/types"
	"devshard/user"
)

const noRaceGrace time.Duration = 0

// SweepExecutionTimeouts retries the execution timeouts no race is left to post. The budget is the
// ceiling for the whole tick, not per escrow, so the added vote traffic has one upper bound whatever
// the escrow count is; the walk starts one escrow further along each tick, so a long backlog on the
// first escrow cannot starve the rest. Each escrow is held for its own sweep, so a retirement waits
// instead of closing a session mid-vote; the hold makes the escrow busy but is not a request, so it never moves ActiveUsers.
func (r *Registry) SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) (due, applied, failed int) {
	states := r.Snapshot()
	if budget <= 0 || len(states) == 0 || r.chainMaxNonce.Load() == unknownChainMaxNonce {
		return 0, 0, 0
	}
	start := int(r.sweepCursor.Add(1)-1) % len(states)
	remaining := budget
	for offset := range states {
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		state := states[(start+offset)%len(states)]
		session, release, held := r.holdForSweep(state.ID)
		if !held {
			continue // closed between the snapshot and the hold; its settlement sweeps it before finalizing
		}
		report := func() sweepCounts {
			defer release()
			if session.Phase() != types.PhaseActive {
				return sweepCounts{}
			}
			swept := session.SweepExecutionTimeouts(ctx, grace, remaining)
			return sweepCounts{due: swept.Due, applied: swept.Applied, failed: swept.Failed}
		}()
		remaining -= report.due
		due += report.due
		applied += report.applied
		failed += report.failed
	}
	return due, applied, failed
}

// SweepBeforeFinalize votes the execution timeouts left due on an escrow about to finalize, with no race grace, after any sweep holding it ends; a non-resident escrow is rehydrated with a serving session.
func (r *Registry) SweepBeforeFinalize(ctx context.Context, escrowID string, budget int) (due, applied, failed int, err error) {
	var swept user.SweepReport
	err = r.withServingSession(ctx, escrowID, func(session EscrowSession) (sweepErr error) {
		swept, sweepErr = session.SweepBeforeFinalize(ctx, budget)
		return sweepErr
	})
	return swept.Due, swept.Applied, swept.Failed, err
}

type sweepCounts struct {
	due     int
	applied int
	failed  int
}
