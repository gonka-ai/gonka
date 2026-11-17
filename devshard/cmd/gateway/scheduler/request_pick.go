package scheduler

import (
	"context"
	"errors"
	"fmt"
	"maps"

	"devshard/types"
)

// Pick serves one request or one escalation attempt; an escalation reuses the pinned escrow, and a nonce committed before ctx ends is still returned. See routing.md, "One re-pick when an escrow gives up" and "Past every escrow that cannot pay".
func (s *Scheduler) Pick(ctx context.Context, profile RequestProfile) (Assignment, error) {
	if profile.Escrow != "" {
		assignment, _, err := s.pickOnce(ctx, profile, nil)
		return assignment, err
	}

	assignment, routedTo, err := s.pickPastEmptyEscrows(ctx, profile, nil)
	reason, gaveUp := escrowGaveUp(err)
	if !gaveUp {
		return assignment, err
	}

	if ctx.Err() != nil {
		return Assignment{}, err
	}

	retried, _, retryErr := s.pickPastEmptyEscrows(ctx, profile, avoidedEscrows{routedTo: reason})
	if retryErr == nil || outranksBusy(retryErr) || reason.secondAnswerStands(retryErr) {
		return retried, retryErr
	}
	return Assignment{}, err
}

func escrowGaveUp(err error) (avoidReason, bool) {
	switch {
	case errors.Is(err, ErrHostsBusy):
		return avoidedHostsBusy, true
	case errors.Is(err, ErrEscrowGone):
		return avoidedRetired, true
	case errors.Is(err, ErrEscrowAdvanceFailed):
		return avoidedAdvanceFailed, true
	}
	return avoidedNone, false
}

// pickPastEmptyEscrows steps over every escrow that cannot pay for this request, so a caller hears "no balance" only once no escrow has any. See routing.md, "Past every escrow that cannot pay".
func (s *Scheduler) pickPastEmptyEscrows(ctx context.Context, profile RequestProfile, seed avoidedEscrows) (Assignment, string, error) {
	assignment, routedTo, err := s.pickOnce(ctx, profile, seed)
	if !outOfFunds(err) || routedTo == "" {
		return assignment, routedTo, err
	}

	emptied := err
	refused := 0
	avoided := make(avoidedEscrows, len(seed)+1)
	maps.Copy(avoided, seed)
	for range len(s.escrows.Candidates(profile.Model)) {
		avoided[routedTo] = avoidedOutOfFunds
		refused++
		if ctx.Err() != nil {
			return Assignment{}, "", outOfFundsAfter(refused, emptied)
		}
		assignment, routedTo, err = s.pickOnce(ctx, profile, avoided)
		switch {
		case err == nil:
			return assignment, routedTo, nil
		case routedTo == "":
			return Assignment{}, "", outOfFundsAfter(refused, emptied)
		case !outOfFunds(err):
			return assignment, routedTo, err
		}
	}
	return Assignment{}, "", outOfFundsAfter(refused, emptied)
}

// outOfFundsAfter names how many escrows were asked and answers as the shard having no room, which a drain's own error does not. See routing.md, "Past every escrow that cannot pay".
func outOfFundsAfter(refused int, err error) error {
	if refused <= 0 {
		return err
	}
	return &EscrowsOutOfFundsError{Refused: refused, wrapped: fmt.Errorf("%w: %w", ErrNoEscrowCapacity, err)}
}

// outOfFunds holds for the one refusal another escrow's balance can answer. See routing.md, "Past every escrow that cannot pay".
func outOfFunds(err error) bool {
	return errors.Is(err, types.ErrInsufficientBalance)
}

// outranksBusy holds for the answers a second round may return in place of a busy shard's. See routing.md, "One re-pick when an escrow gives up".
func outranksBusy(err error) bool {
	return errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, types.ErrInsufficientBalance)
}

// pickOnce names the escrow it routed to, so a caller that re-picks can leave that one out of the second round.
func (s *Scheduler) pickOnce(ctx context.Context, profile RequestProfile, avoided avoidedEscrows) (Assignment, string, error) {
	queued := newWaiter(profile, s.now())
	escrow, err := s.pickEscrow(profile, s.snapshots.Snapshot(), queued, avoided)
	if err != nil {
		return Assignment{}, "", err
	}

	claimed, err := s.claimAndSubmit(escrow, queued)
	if errors.Is(err, ErrEscrowBusy) && profile.Escrow == "" {
		escrow, claimed, err = s.claimPastFullQueues(ctx, profile, queued, escrow, avoided)
	}
	if err != nil {
		return Assignment{}, escrow.ID, err
	}
	defer claimed.pendingSubmits.Add(-1)

	var result pickResult
	select {
	case result = <-queued.replyCh:
	case <-ctx.Done():
		// A committed nonce is handed over even to a caller that left: its host is owed the request. See routing.md, "The per-escrow dispatcher".
		delivered, wasDelivered := queued.abandon()
		if !wasDelivered || delivered.err != nil {
			return Assignment{}, escrow.ID, ctx.Err()
		}
		result = delivered
	}
	if result.err != nil {
		return Assignment{}, escrow.ID, result.err
	}
	// Only a reserve that served a nonce is taken: a failed pick leaves it a reserve.
	if escrow.IsReserve {
		s.reportReserveTaken(escrow.ID)
	}
	return result.assignment, escrow.ID, nil
}

// claimPastFullQueues offers the request to the next escrow in the ranking while each chosen one's submit queue is full, and answers busy once none is left. See routing.md, "Past a full queue".
func (s *Scheduler) claimPastFullQueues(ctx context.Context, profile RequestProfile, queued *waiter, refusedFirst Escrow, avoided avoidedEscrows) (Escrow, *dispatcher, error) {
	avoidedFull := make(avoidedEscrows, len(avoided)+1)
	maps.Copy(avoidedFull, avoided)
	refused := refusedFirst
	for range len(s.escrows.Candidates(profile.Model)) {
		avoidedFull[refused.ID] = avoidedQueueFull
		if ctx.Err() != nil {
			break
		}
		next, err := s.pickEscrow(profile, s.snapshots.Snapshot(), queued, avoidedFull)
		if err != nil {
			break
		}
		claimed, err := s.claimAndSubmit(next, queued)
		if !errors.Is(err, ErrEscrowBusy) {
			return next, claimed, err
		}
		refused = next
	}
	return refusedFirst, nil, ErrEscrowBusy
}

// queuedAhead estimates how far each candidate's cursor will have moved before this request draws a nonce. See routing.md, "Pricing an escrow by the burns it will cost".
func (s *Scheduler) queuedAhead(candidates []Escrow) []uint64 {
	ahead := make([]uint64, len(candidates))
	s.registryMu.Lock()
	defer s.registryMu.Unlock()
	for index, candidate := range candidates {
		if running := s.dispatchers[candidate.ID]; running != nil && running.sessionID == candidate.SessionID {
			ahead[index] = uint64(max(running.pendingSubmits.Load(), 0))
		}
	}
	return ahead
}

// claimAndSubmit returns the dispatcher that accepted the waiter, still claimed; a stopped one is replaced by the next get-or-create, so this retries at most once more.
func (s *Scheduler) claimAndSubmit(escrow Escrow, queued *waiter) (*dispatcher, error) {
	for {
		target, err := s.dispatcherFor(escrow)
		if err != nil {
			return nil, err
		}
		outcome := target.submitWaiter(queued)
		if outcome == submitAccepted {
			return target, nil
		}
		target.pendingSubmits.Add(-1)
		if outcome == submitFull {
			return nil, ErrEscrowBusy
		}
	}
}
