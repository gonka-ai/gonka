package registry

import (
	"context"
	"time"

	"devshard/cmd/gateway/quorum"
	"devshard/types"
	"devshard/user"
)

const (
	noRaceGrace       time.Duration = 0
	sweepMinimumShare int           = 1
	sweepShareDivisor int           = 2
)

// SweepExecutionTimeouts retries the execution timeouts no race is left to post. The budget is the ceiling for the whole tick, not per escrow, so the added vote traffic has one upper bound whatever the escrow count is; the walk starts one escrow further along each tick, and an escrow takes at most half of it until every escrow has had its turn, so a long backlog on one cannot starve the rest. Each escrow is held for its own sweep, so a retirement waits instead of closing a session mid-vote; the hold makes the escrow busy but is not a request, so it never moves ActiveUsers.
func (r *Registry) SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) (due, applied, failed int) {
	states := r.Snapshot()
	if budget <= 0 || len(states) == 0 || r.chainMaxNonce.Load() == unknownChainMaxNonce {
		return 0, 0, 0
	}
	start := int(r.sweepCursor.Add(1)-1) % len(states)
	share := max(sweepMinimumShare, budget/sweepShareDivisor)
	remaining := budget
	var capped []EscrowState
	sweep := func(state EscrowState, limit int) bool {
		report := r.sweepEscrow(ctx, state, grace, limit)
		remaining -= report.Due
		due += report.Due
		applied += report.Applied
		failed += report.Failed
		return report.Due >= limit
	}
	for offset := range states {
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		state := states[(start+offset)%len(states)]
		if sweep(state, min(share, remaining)) {
			capped = append(capped, state)
		}
	}
	for _, state := range capped {
		if remaining <= 0 || ctx.Err() != nil {
			break
		}
		sweep(state, remaining)
	}
	return due, applied, failed
}

func (r *Registry) sweepEscrow(ctx context.Context, state EscrowState, grace time.Duration, budget int) SweepReport {
	session, release, held := r.holdForSweep(state.ID)
	if !held {
		return SweepReport{}
	}
	defer release()
	if session.Phase() != types.PhaseActive {
		return SweepReport{}
	}
	report := session.SweepExecutionTimeouts(ctx, grace, budget)
	if r.sweptVotes != nil && len(report.Votes) > 0 {
		participants := session.HostParticipantKeyList()
		if len(participants) == 0 {
			return report
		}
		for _, vote := range report.Votes {
			r.sweptVotes(state.ID, state.Model, participants[quorum.ExecutorSlot(vote.Nonce, len(participants))], vote)
		}
	}
	return report
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
