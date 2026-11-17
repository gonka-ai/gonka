package scheduler

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

type armedTimer struct {
	fired  <-chan time.Time
	cancel func()
}

func (a *armedTimer) disarm() {
	if a.cancel != nil {
		a.cancel()
	}
	a.fired, a.cancel = nil, nil
}

// offer is what one Advance decided, kept across the loop so the decide closure is built once per drain.
type offer struct {
	decision      Decision
	taken         reservation
	escrowRetired bool
	forced        bool
	refusedWaiter *waiter
}

// drain assigns nonces until the queue empties, a nonce is held, or the burn budget trips. See README, "The drain".
func (d *dispatcher) drain() (time.Time, bool) {
	participants := d.session.ParticipantKeys()
	snapshot := d.snapshots.Snapshot()
	avail := freeze(d.predicates(snapshot), len(participants))
	acquire := admit(&avail, d.acquireSlot)
	groupSize := d.session.GroupSize()
	burnBudget := groupSize * (len(d.waiting) + 1)
	// Read before Advance: decide runs under the session's lock.
	escalationBound := escalationNonceBound(snapshot.MaxNonce, groupSize)
	burnLimit := d.maxConsecutiveBurns()

	var offered offer
	decide := func(binding HostBinding) NonceIntent {
		offered.taken.participant = binding.Participant
		forcingDue := burnLimit > 0 && d.burnsInARow >= burnLimit
		gates := avail
		if forcingDue {
			gates = avail.servingOverFullWindows()
		}
		offered.decision = match(binding, d.waiting, participants, gates, d.now(), d.matchWait)
		switch decided := offered.decision.(type) {
		case serve:
			cost := slotCost(decided.waiter.profile)
			waived := gates.throttlingWaived(binding.Participant)
			release, refused := acquire(binding.Participant, cost, waived)
			if refused != blockNone && forcingDue && !waived {
				release, refused = acquire(binding.Participant, cost, true)
				offered.forced = refused == blockNone
			}
			if refused != blockNone {
				offered.refusedWaiter = decided.waiter
				offered.decision = burn{kind: ghostFor[refused]}
			}
			offered.taken.hostSlot = release
		case burn:
		default:
			return intentFor(offered.decision)
		}
		var held bool
		if offered.taken.escrowHold, held = d.holdEscrow(); !held {
			offered.taken.escrowHold, held = d.holdForEscalations(binding.Nonce, escalationBound)
		}
		if !held {
			offered.taken.releaseSlot()
			offered.escrowRetired = true
			return NonceIntent{}
		}
		if served, serving := offered.decision.(serve); serving && !served.waiter.bind() {
			d.giveBack(offered.taken)
			offered.decision = decline{}
		}
		return intentFor(offered.decision)
	}

	for {
		d.sweepExhausted(participants, avail)
		if len(d.waiting) == 0 {
			return time.Time{}, false
		}

		offered = offer{}
		prepared, err := d.session.Advance(decide)
		var spent *SpentNonceError
		switch {
		case offered.escrowRetired:
			if d.failNewRequests(ErrEscrowGone) {
				continue
			}
			d.failWaiting(ErrEscrowGone)
			return time.Time{}, false
		case errors.As(err, &spent):
			d.burnSpentNonce(offered, spent)
			if d.spendBurnBudget(&burnBudget) {
				return time.Time{}, false
			}
			continue
		case err != nil:
			d.failAdvance(offered.decision, offered.taken, err)
			return time.Time{}, false
		}

		switch outcome := offered.decision.(type) {
		case serve:
			if outcome.despiteExclusion {
				d.recordExcludedServe(offered.taken.participant)
			}
			if d.handOff(outcome.waiter, offered.taken, prepared) {
				if offered.forced {
					d.recordForcedSend(offered.taken.participant, d.burnsInARow)
				}
				d.burnsInARow = 0
			}
		case burn:
			burned := Burn{
				Participant: offered.taken.participant, Reason: outcome.kind.reason(),
				RequestID: d.burnedDuring(offered.refusedWaiter),
			}
			if prepared != nil {
				burned.Nonce = prepared.Nonce()
			}
			d.recordGhost(burned)
			offered.taken.releaseHold()
			d.burnsInARow++
			if d.spendBurnBudget(&burnBudget) {
				return time.Time{}, false
			}
		case hold:
			d.recordHold()
			return outcome.until, true
		case decline:
			d.dropAbandoned()
			continue
		default:
			d.failWaiting(fmt.Errorf("escrow %s: session offered no binding", d.escrowID))
			return time.Time{}, false
		}
	}
}

// holdForEscalations holds an escrow its live hold refused for a queue of escalations alone, while the nonce stays below the escalation bound. See routing.md, "Picking an escrow".
func (d *dispatcher) holdForEscalations(nonce, bound uint64) (func(), bool) {
	if d.escalationHold == nil || nonce >= bound || slices.ContainsFunc(d.waiting, isNewRequest) {
		return nil, false
	}
	return d.escalationHold()
}

// failNewRequests answers every waiter but the escalations, and reports whether it answered any and escalations are left.
func (d *dispatcher) failNewRequests(err error) bool {
	kept := d.waiting[:0]
	for _, queued := range d.waiting {
		if isNewRequest(queued) {
			queued.deliver(pickResult{err: err})
			continue
		}
		kept = append(kept, queued)
	}
	answered := len(kept) < len(d.waiting)
	clear(d.waiting[len(kept):])
	d.waiting = kept
	return answered && len(kept) > 0
}

func isNewRequest(queued *waiter) bool { return !queued.profile.Escalation }

func (d *dispatcher) spendBurnBudget(budget *int) (tripped bool) {
	*budget--
	if *budget > 0 {
		return false
	}
	d.recordBudgetTrip()
	d.failWaiting(ErrNoAvailableHost)
	return true
}

func (d *dispatcher) burnSpentNonce(offered offer, spent *SpentNonceError) {
	d.giveBack(offered.taken)
	requestID := d.burnedDuring(offered.refusedWaiter)
	if served, serving := offered.decision.(serve); serving {
		requestID = served.waiter.profile.RequestID
		d.dequeue(served.waiter)
		served.waiter.deliver(pickResult{err: fmt.Errorf("escrow %s: %w", d.escrowID, spent)})
	}
	d.recordGhost(Burn{Nonce: spent.Nonce, Participant: offered.taken.participant, Reason: ghostPrepareFailed.reason(), RequestID: requestID})
}

// sweepExhausted drops waiters no available participant can serve, instantly and without touching the nonce.
func (d *dispatcher) sweepExhausted(participants []string, avail availability) {
	d.keepWaiting(func(queued *waiter) bool {
		if queued.abandoned.Load() {
			return false
		}
		canServe, busy, chainBlocked, excludedOnly := servable(queued, participants, avail)
		// An excluded host is not a dead end: past the stale window match spends the nonce on it.
		if canServe || excludedOnly {
			return true
		}
		switch {
		case busy, chainBlocked:
			queued.deliver(pickResult{err: ErrHostsBusy})
		default:
			queued.deliver(pickResult{err: ErrNoAvailableHost})
		}
		return false
	})
}

// dropAbandoned answers nobody: the goroutine that left already delivered the waiter's result.
func (d *dispatcher) dropAbandoned() {
	d.keepWaiting(func(queued *waiter) bool { return !queued.abandoned.Load() })
}

// burnedDuring names the waiter a refused slot was meant for, else the oldest waiter still waiting. See README, "The boundary types".
func (d *dispatcher) burnedDuring(refusedWaiter *waiter) string {
	if refusedWaiter != nil {
		return refusedWaiter.profile.RequestID
	}
	for _, queued := range d.waiting {
		if !queued.abandoned.Load() {
			return queued.profile.RequestID
		}
	}
	return ""
}

// keepWaiting compacts the queue, clearing the tail so a departed waiter is not held by the array.
func (d *dispatcher) keepWaiting(accept func(*waiter) bool) {
	kept := d.waiting[:0]
	for _, queued := range d.waiting {
		if accept(queued) {
			kept = append(kept, queued)
		}
	}
	for index := len(kept); index < len(d.waiting); index++ {
		d.waiting[index] = nil
	}
	d.waiting = kept
}

// servable separates a busy host, which passes on its own, from one the chain has stopped. See README, "The drain".
func servable(queued *waiter, participants []string, avail availability) (canServe, busy, chainBlocked, excludedOnly bool) {
	anyBusy, anyChainBlocked, anyExcluded := false, false, false
	for _, participant := range participants {
		switch avail.blocks(participant, queued) {
		case blockNone:
			return true, false, false, false
		case blockWindowFull, blockCutOff:
			anyBusy = true
		case blockPoCRequired:
			anyChainBlocked = true
		case blockExcluded:
			anyExcluded = true
		}
	}
	return false, anyBusy, anyChainBlocked, anyExcluded
}

// reservation is what a decision took: a serve the slot and the escrow hold, a burn the hold alone. See routing.md, "Where the nonce, the slot and the hold are taken".
type reservation struct {
	participant string
	escrowHold  func()
	hostSlot    func()
}

func (r reservation) releaseHold() {
	if r.escrowHold != nil {
		r.escrowHold()
	}
}

func (r reservation) releaseSlot() {
	if r.hostSlot != nil {
		r.hostSlot()
	}
}

func (d *dispatcher) giveBack(taken reservation) {
	taken.releaseSlot()
	taken.releaseHold()
}

// handOff reports whether the assignment reached its caller; anything else spent the nonce on nobody. See routing.md, "The forced send".
func (d *dispatcher) handOff(served *waiter, taken reservation, prepared Prepared) bool {
	d.dequeue(served)
	if prepared == nil {
		d.giveBack(taken)
		served.deliver(pickResult{err: fmt.Errorf("escrow %s: session committed no nonce", d.escrowID)})
		return false
	}
	assignment := Assignment{Escrow: d.escrowID, Host: taken.participant, Nonce: prepared, EscrowHold: taken.escrowHold, HostSlot: taken.hostSlot}
	if served.deliver(pickResult{assignment: assignment}) {
		d.recordAssigned(prepared.Nonce(), served.profile, served.enqueued)
		return true
	}
	d.giveBack(taken)
	d.recordGhost(Burn{
		Nonce: prepared.Nonce(), Participant: taken.participant,
		Reason: ghostAbandoned.reason(), RequestID: served.profile.RequestID,
	})
	return false
}
