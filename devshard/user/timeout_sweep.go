package user

import (
	"context"
	"time"

	"devshard/logging"
)

// SweepReport counts what one sweep did, for the operator asking why a nonce settled at full reserve.
type SweepReport struct {
	Due     int
	Applied int
	Failed  int
}

// adoptRecoveredNonces marks every nonce so far as a previous process's: none of them has a race left to wake in this one.
func (s *Session) adoptRecoveredNonces() {
	s.racesFromNonce = s.nonce + 1
}

// SweepExecutionTimeouts votes the execution timeouts no race is left to retry. The race that owned a
// nonce votes once; a round that found no verifiers, or a restart that outlived the round, leaves the
// nonce settleable and unclaimed until the escrow settles it at full reserve. Work is bounded by budget
// and votes run one at a time, so the load this adds does not follow the request rate.
func (s *Session) SweepExecutionTimeouts(ctx context.Context, grace time.Duration, budget int) SweepReport {
	due := s.sm.StartedInferencesPastDeadline(time.Now(), TimeoutBuffer, grace, s.racesFromNonce, budget)
	report := SweepReport{Due: len(due)}
	for _, nonce := range due {
		if ctx.Err() != nil {
			break
		}
		// A zero send time keeps the refusal deadline in the past, so a record that changed under the
		// scan is judged at once instead of waiting out a deadline this sweep never planned for.
		result, err := s.HandleTimeout(ctx, nonce, time.Time{}, nil)
		if result.Applied {
			report.Applied++
			continue
		}
		report.Failed++
		logging.Warn("swept execution timeout was not applied",
			"escrow", s.escrowID, "nonce", nonce, "outcome", result.Outcome,
			"detail", result.DetailReason, "error", err)
	}
	return report
}
