package scheduler

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/limits"
	"devshard/types"
)

// EscrowRetired lets an observer forget an escrow, whose id is never reused. See README, "Dispatcher lifecycle".
type dispatchObserver interface {
	GhostBurned(escrowID string, burned Burn)
	NonceHeld(escrowID string)
	BurnBudgetExhausted(escrowID string)
	EscrowRetired(escrowID string)
	NonceAssigned(escrowID string, nonce uint64, profile RequestProfile)
	NonceQueued(escrowID string, waited time.Duration)
	ExcludedHostServed(escrowID, participant string)
	ForcedSend(escrowID, participant string, burnsInARow int64)
}

// dispatcherDeps wires one escrow's actor. See README, "Where the nonce, the slot and the hold are taken".
type dispatcherDeps struct {
	escrowID            string
	sessionID           uint64
	session             session
	snapshots           snapshotSource
	predicates          func(chain.PhaseSnapshot) availability
	acquireSlot         func(participant string, cost limits.TokenCost, overFullWindow bool) (func(), limits.Admission)
	holdEscrow          func() (func(), bool)
	escalationHold      func() (func(), bool)
	observer            dispatchObserver
	now                 func() time.Time
	matchWait           time.Duration
	maxConsecutiveBurns func() int64
	retirementReserve   func() uint64
	newTimer            func(time.Duration) (<-chan time.Time, func())
	retire              func(*dispatcher) bool
	idleGrace           time.Duration
	submitBuffer        int
	onExhausted         func(escrowID string, reason ExhaustionReason)
}

type submitOutcome int

const (
	submitAccepted submitOutcome = iota
	submitStopped
	submitFull
)

// dispatcher turns one escrow's nonce stream into assignments; its loop goroutine solely owns waiting and the session. See routing.md, "The per-escrow dispatcher".
type dispatcher struct {
	dispatcherDeps

	submit  chan *waiter
	stopCh  chan struct{}
	done    chan struct{}
	waiting []*waiter

	burnsInARow    int64
	pendingSubmits atomic.Int64

	lifecycleMu sync.RWMutex
	started     bool
	stopped     bool
}

func newDispatcher(deps dispatcherDeps) *dispatcher {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.newTimer == nil {
		deps.newTimer = realTimer
	}
	if deps.holdEscrow == nil {
		deps.holdEscrow = func() (func(), bool) { return nil, true }
	}
	if deps.maxConsecutiveBurns == nil {
		deps.maxConsecutiveBurns = func() int64 { return 0 }
	}
	if deps.retirementReserve == nil {
		deps.retirementReserve = func() uint64 { return 0 }
	}
	return &dispatcher{
		dispatcherDeps: deps,
		submit:         make(chan *waiter, deps.submitBuffer),
		stopCh:         make(chan struct{}),
		done:           make(chan struct{}),
	}
}

func realTimer(delay time.Duration) (<-chan time.Time, func()) {
	timer := time.NewTimer(delay)
	return timer.C, func() { timer.Stop() }
}

func (d *dispatcher) start() {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.started || d.stopped {
		return
	}
	d.started = true
	go d.loop()
}

// stop is idempotent and blocks until the loop has exited; the write lock keeps a submit out of a dead buffer.
func (d *dispatcher) stop() {
	d.lifecycleMu.Lock()
	if !d.stopped {
		d.stopped = true
		close(d.stopCh)
		if !d.started {
			d.absorb()
			d.failWaiting(ErrDispatcherStopped)
		}
	}
	started := d.started
	d.lifecycleMu.Unlock()

	if started {
		<-d.done
	}
}

// submitWaiter never blocks, and reports a full queue apart from a stopped one. See routing.md, "The per-escrow dispatcher".
func (d *dispatcher) submitWaiter(queued *waiter) submitOutcome {
	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	if d.stopped {
		return submitStopped
	}
	select {
	case d.submit <- queued:
		return submitAccepted
	default:
		return submitFull
	}
}

func (d *dispatcher) isStopped() bool {
	d.lifecycleMu.RLock()
	defer d.lifecycleMu.RUnlock()
	return d.stopped
}

// markStopped shuts the actor down from inside its own goroutine, refusing once a waiter is in the submit buffer. See routing.md, "Idle dispatchers are reaped".
func (d *dispatcher) markStopped() bool {
	d.lifecycleMu.Lock()
	defer d.lifecycleMu.Unlock()
	if d.stopped || len(d.submit) > 0 {
		return false
	}
	d.stopped = true
	close(d.stopCh)
	return true
}

func (d *dispatcher) loop() {
	defer close(d.done)
	var pending, idle armedTimer
	defer pending.disarm()
	defer idle.disarm()

	for {
		select {
		case queued := <-d.submit:
			d.waiting = append(d.waiting, queued)
			d.absorb()
		case <-pending.fired:
		case <-idle.fired:
			if d.retire != nil && d.retire(d) {
				return
			}
		case <-d.stopCh:
			d.absorb()
			d.failWaiting(ErrDispatcherStopped)
			return
		}

		pending.disarm()
		idle.disarm()
		switch until, held := d.drain(); {
		case held:
			pending.fired, pending.cancel = d.newTimer(until.Sub(d.now()))
		case len(d.waiting) == 0 && d.retire != nil && d.idleGrace > 0:
			idle.fired, idle.cancel = d.newTimer(d.idleGrace)
		}
	}
}

// failAdvance answers the whole queue: the session could not advance its nonce at all. See README, "Where the nonce, the slot and the hold are taken".
func (d *dispatcher) failAdvance(decision Decision, taken reservation, err error) {
	switch decision.(type) {
	case serve:
		d.giveBack(taken)
	case burn:
		taken.releaseHold()
	}
	if errors.Is(err, types.ErrInsufficientBalance) && d.onExhausted != nil && d.escrowIsSpent() {
		d.onExhausted(d.escrowID, ExhaustionInsufficientBalance)
	}
	if errors.Is(err, types.ErrInsufficientBalance) {
		d.failWaiting(fmt.Errorf("escrow %s: advancing nonce: %w", d.escrowID, err))
		return
	}
	d.failWaiting(fmt.Errorf("escrow %s: %w: %w", d.escrowID, ErrEscrowAdvanceFailed, err))
}

// escrowIsSpent asks whether the escrow can still pay for the largest request its model's hosts accept. See capacity.md, "The balance floor".
func (d *dispatcher) escrowIsSpent() bool {
	reserve := d.retirementReserve()
	if reserve == 0 || d.session == nil {
		return true
	}
	cost, ok := requestCost(d.session, reserve)
	if !ok {
		return true
	}
	return d.session.Balance() < cost
}

func (d *dispatcher) failWaiting(err error) {
	for index, queued := range d.waiting {
		queued.deliver(pickResult{err: err})
		d.waiting[index] = nil
	}
	d.waiting = d.waiting[:0]
}

// absorb takes everything already queued so co-arriving requests compete for the same nonce.
func (d *dispatcher) absorb() {
	for {
		select {
		case queued := <-d.submit:
			d.waiting = append(d.waiting, queued)
		default:
			return
		}
	}
}

func (d *dispatcher) dequeue(target *waiter) {
	for index, queued := range d.waiting {
		if queued != target {
			continue
		}
		copy(d.waiting[index:], d.waiting[index+1:])
		d.waiting[len(d.waiting)-1] = nil
		d.waiting = d.waiting[:len(d.waiting)-1]
		return
	}
}

func (d *dispatcher) recordGhost(burned Burn) {
	if d.observer != nil {
		d.observer.GhostBurned(d.escrowID, burned)
	}
}

func (d *dispatcher) recordHold() {
	if d.observer != nil {
		d.observer.NonceHeld(d.escrowID)
	}
}

func (d *dispatcher) recordBudgetTrip() {
	if d.observer != nil {
		d.observer.BurnBudgetExhausted(d.escrowID)
	}
}

func (d *dispatcher) recordAssigned(nonce uint64, profile RequestProfile, enqueued time.Time) {
	if d.observer != nil {
		d.observer.NonceAssigned(d.escrowID, nonce, profile)
		d.observer.NonceQueued(d.escrowID, d.now().Sub(enqueued))
	}
}

func (d *dispatcher) recordExcludedServe(participant string) {
	if d.observer != nil {
		d.observer.ExcludedHostServed(d.escrowID, participant)
	}
}

func (d *dispatcher) recordForcedSend(participant string, burnsInARow int64) {
	if d.observer != nil {
		d.observer.ForcedSend(d.escrowID, participant, burnsInARow)
	}
}

func intentFor(decision Decision) NonceIntent {
	switch outcome := decision.(type) {
	case serve:
		return NonceIntent{Commit: true, Params: outcome.waiter.profile.Params}
	case burn:
		return NonceIntent{Commit: true, Ghost: true}
	default:
		return NonceIntent{}
	}
}

// freeze memoises the ladder for the drain; a predicate nobody set is left nil, so "no allowlist" stays distinct from one that refuses everybody.
func freeze(live availability, hosts int) availability {
	live.frozen = make(map[string]blockReason, hosts)
	return live
}

// admit couples admission to the frozen predicates, and answers with the block that refused; blockNone means the tokens were taken. See routing.md, "Where the nonce, the slot and the hold are taken".
func admit(avail *availability, acquire func(string, limits.TokenCost, bool) (func(), limits.Admission)) func(string, limits.TokenCost, bool) (func(), blockReason) {
	if avail.frozen == nil {
		avail.frozen = map[string]blockReason{}
	}
	return func(participant string, cost limits.TokenCost, overFullWindow bool) (func(), blockReason) {
		release, admission := acquire(participant, cost, overFullWindow)
		refused := blockForAdmission(admission)
		if refused == blockNone {
			return release, blockNone
		}
		avail.refuseSlot(participant, refused)
		return nil, refused
	}
}
