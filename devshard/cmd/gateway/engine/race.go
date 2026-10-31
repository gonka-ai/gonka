package engine

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
)

// Chunk progress must not park a host's goroutine; terminal events block regardless.
const eventBuffer = 32

var errNoDispatchTarget = errors.New("assigned escrow has no dispatch target")

type picker interface {
	Pick(ctx context.Context, profile scheduler.RequestProfile) (scheduler.Assignment, error)
	HostDiverged(escrowID, participant string, at time.Time) bool
	HostServed(escrowID, participant string, sentAt time.Time)
	HostAbsent(escrowID, participant string)
}

// DispatchTarget is one escrow's session, seen only through what a race needs from it.
type DispatchTarget interface {
	dispatcher
	HostCount() int
	HostLabel(hostIdx int) string
	NonceFinished(nonce uint64) bool
	RewindHostCatchUp(hostIdx int, cause string) bool
}

// Escrows rotate, so the handle is fetched per race rather than held.
type targets interface {
	Target(escrowID string) (DispatchTarget, bool)
}

type hostPerf interface {
	CapabilityRecorder
	Acquire(participant string)
	Release(participant string)
	Ejected(participant, model string) bool
	Degraded(participant, model string) bool
	FirstContentP75(participant, model string) (time.Duration, bool)
	Pressure(participant, model string) perf.Pressure
	RecordOutputOverrun(participant, model string)
}

// crownGate carries the empty-stream crowning penalty between races.
type crownGate interface {
	Denied(participant, model string) bool
	Observe(participant, model string, contentless bool)
}

type snapshotSource interface{ Snapshot() chain.PhaseSnapshot }

// Hold is given back only once the vote is posted, and Report runs on whichever goroutine ends the race.
type raceDeps struct {
	Picker       picker
	Targets      targets
	Limiter      hostWindows
	Perf         hostPerf
	Crown        crownGate
	Snapshots    snapshotSource
	Policy       EscalationPolicy
	Modes        config.Modes
	DrainTimeout time.Duration
	CancelGrace  time.Duration
	Classify     func(participant string) streamClassifier
	PrefixBudget *carryBudget
	Fleet        *fleetFirstToken
	Now          func() time.Time
	Timer        func() raceTimer
	Journal      raceJournal

	ModelContextLength uint64

	Hold func(release func())

	Report func(RaceOutcome)
}

type raceRequest struct {
	Request
	Client io.Writer
}

const (
	triggerNone deadlineTrigger = iota
	triggerHardTimeout
	triggerEscalation
	triggerMissedDeadline
	triggerPick
	triggerStall
)

const (
	cancelledAttemptGrace = 2 * time.Second
	maxCatchUpBeforeSend  = time.Minute
)

// Written only by the coordinator: from AttemptEvents, and from its own deadline firings.
type liveAttempt struct {
	nonce       uint64
	hostIdx     int
	participant string
	suspicious  bool
	cancel      context.CancelFunc

	launchTime   time.Time
	catchUpFault time.Time
	sendTime     time.Time
	receiptTime  time.Time
	firstToken   time.Time
	firstContent time.Time
	lastChunk    time.Time
	completed    time.Time

	observedFirst time.Duration
	fleetScale    float64
	escalated     bool
	done          bool
	abandoned     bool
	stalled       bool
	backstopped   bool
	nonceFinished bool
	inInference   bool

	receiptDeadline    deadlineJudgement
	firstTokenDeadline deadlineJudgement

	outcome   *AttemptOutcome
	lifecycle Lifecycle
}

type raceCoordinator struct {
	drain   drain
	deps    raceDeps
	request raceRequest

	events chan AttemptEvent
	crown  chan crownRequest
	picked chan pickedHost
	done   chan struct{}
	timer  raceTimer

	escrowID     string
	target       DispatchTarget
	decision     string
	budget       int
	attemptLimit int
	started      time.Time

	attempts []*liveAttempt
	byNonce  map[uint64]*liveAttempt
	scratch  []EscalationAttempt
	claims   []crownRequest

	winner           *liveAttempt
	pending          int
	pickCancel       context.CancelFunc
	pickStarted      time.Time
	pickStopped      bool
	pickReason       string
	moreImmediate    int
	excluded         []string
	clientGoneAt     time.Time
	cancelled        bool
	abandonAfter     <-chan time.Time
	handedOff        bool
	pocBypass        bool
	balanceExhausted bool
	startErr         error

	retryRuledOut bool
	reportOnce    sync.Once
}

// raceExit is why await stopped; only exitComplete means the race is over.
type raceExit int

const (
	exitComplete raceExit = iota
	exitWinnerServed
	exitClientGone
)

// The returned outcome is the client's view; the reported one may be finished later, after losers stream.
func runRace(clientCtx context.Context, deps raceDeps, request raceRequest) (RaceOutcome, error) {
	coordinator := newCoordinator(clientCtx, deps, request)
	defer func() {
		if panicked := recover(); panicked != nil {
			coordinator.abandon()
			panic(panicked)
		}
	}()
	if err := coordinator.begin(); err != nil {
		return coordinator.fail(err)
	}
	exit := coordinator.await()
	if exit == exitComplete {
		outcome := coordinator.report()
		if len(outcome.Attempts) == 0 && coordinator.startErr != nil {
			return outcome, coordinator.startErr
		}
		return outcome, nil
	}
	served := coordinator.outcome()
	var err error
	if exit == exitClientGone {
		err = coordinator.drain.clientErr()
	}
	go func() {
		coordinator.await()
		coordinator.report()
	}()
	return served, err
}

func newCoordinator(clientCtx context.Context, deps raceDeps, request raceRequest) *raceCoordinator {
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Timer == nil {
		deps.Timer = newWallTimer
	}
	contexts := newDrain(clientCtx, deps.DrainTimeout)
	request.Client = contexts.gate(request.Client)
	return &raceCoordinator{
		drain:    contexts,
		deps:     deps,
		request:  request,
		events:   make(chan AttemptEvent, eventBuffer),
		crown:    make(chan crownRequest),
		picked:   make(chan pickedHost, 1),
		done:     make(chan struct{}),
		timer:    deps.Timer(),
		byNonce:  map[uint64]*liveAttempt{},
		escrowID: request.Escrow,
		started:  deps.Now(),
	}
}

// The pick is bounded because a scheduler waiting for capacity would hang, and made under the client's context so a client that leaves while its pick waits gets no primary, unless its nonce was already committed.
func (c *raceCoordinator) begin() error {
	pickCtx, cancelPick := context.WithTimeout(c.drain.client, schedulerPickTimeout)
	defer cancelPick()
	assignment, err := c.pick(pickCtx)
	if err != nil {
		if c.clientDeparted() {
			c.clientGoneAt = c.deps.Now()
		}
		return err
	}
	target, ok := c.deps.Targets.Target(assignment.Escrow)
	if !ok {
		c.strand(assignment, RolePrimary)
		return errNoDispatchTarget
	}
	c.escrowID, c.target = assignment.Escrow, target
	if c.request.OnEscrow != nil {
		c.request.OnEscrow(c.escrowID)
	}

	snapshot := c.deps.Snapshots.Snapshot()
	c.pocBypass = pocBypassActive(snapshot, c.deps.Modes)
	nonceScarce := snapshot.RequestsBlocked && !c.pocBypass
	c.budget = c.deps.Policy.AttemptBudget(target.HostCount(), nonceScarce)
	c.attemptLimit = c.deps.Policy.AttemptLimit(target.HostCount(), nonceScarce)

	plan := c.deps.Policy.Decide(c.budget, c.denied(assignment.Host), c.degraded(assignment.Host))
	c.decision = plan.Reason
	c.launch(assignment, RolePrimary, plan.Reason)
	c.moreImmediate = min(plan.ImmediateAttempts, c.budget) - len(c.attempts)
	c.startNextImmediate()
	return nil
}

// A stranded nonce is committed, paid for and answered by nobody, so it is carried into the timeout plan.
func (c *raceCoordinator) strand(assignment scheduler.Assignment, role string) {
	c.traceStep(RaceStep{
		Kind: RaceStepNonceStranded, RequestID: c.request.RequestID, EscrowID: assignment.Escrow,
		Nonce: assignment.Nonce.Nonce(), Participant: assignment.Host, Role: role,
	})
	c.escrowID = assignment.Escrow
	assignment.ReleaseHostSlot()
	if c.deps.Hold != nil {
		c.deps.Hold(assignment.EscrowHold)
	}
	c.attempts = append(c.attempts, &liveAttempt{
		nonce:       assignment.Nonce.Nonce(),
		hostIdx:     assignment.Nonce.HostIdx(),
		participant: assignment.Host,
		done:        true,
		outcome: &AttemptOutcome{
			Participant: assignment.Host,
			HostIdx:     assignment.Nonce.HostIdx(),
			Nonce:       assignment.Nonce.Nonce(),
			Role:        role,
			Terminal:    TerminalNoReceipt,
		},
	})
}

func (c *raceCoordinator) fail(err error) (RaceOutcome, error) {
	return c.report(), err
}

// An armed escalation outlives the last pending attempt: a race whose every attempt failed is owed another.
func (c *raceCoordinator) await() raceExit {
	defer c.timer.Stop()
	for {
		c.settleClaims()
		arm := nextDeadline(c.deps.Now(), c.plan())
		if c.nothingInFlight() && arm.Trigger != triggerEscalation {
			return exitComplete
		}
		c.rearm(arm)
		select {
		case event := <-c.events:
			c.apply(event)
		case result := <-c.picked:
			c.applyPick(result)
		case claim := <-c.crown:
			c.answer(claim)
		case <-c.timer.Fired():
			c.expire(arm)
		case <-c.abandonAfter:
			c.abandonCancelled()
		case <-c.clientGone():
			return c.depart()
		}
		if c.release() {
			return exitWinnerServed
		}
	}
}

// Every select arm that reads state a queued event can change starts here.
func (c *raceCoordinator) catchUp() {
	for {
		select {
		case event := <-c.events:
			c.apply(event)
		default:
			return
		}
	}
}

func (c *raceCoordinator) depart() raceExit {
	c.catchUp()
	if c.release() {
		return exitWinnerServed
	}
	c.detach()
	if c.nothingInFlight() {
		return exitComplete
	}
	return exitClientGone
}

// Started attempts run on for their votes; the pick does not, since an unstarted attempt serves nobody.
func (c *raceCoordinator) detach() {
	c.handedOff = true
	c.clientGoneAt = c.deps.Now()
	c.stopPicking()
}

func (c *raceCoordinator) clientGone() <-chan struct{} {
	if c.handedOff {
		return nil
	}
	return c.drain.clientGone()
}

// release hands the still-pending losers to a second await on another goroutine.
func (c *raceCoordinator) release() bool {
	if c.handedOff || c.pending == 0 || c.winner == nil || !c.winner.done {
		return false
	}
	c.handedOff = true
	return true
}

func (attempt *liveAttempt) finishWith(event AttemptEvent, nonceFinished bool) {
	attempt.markDone(event.At, nonceFinished)
	attempt.outcome, attempt.lifecycle = event.Outcome, event.Lifecycle
}

func (attempt *liveAttempt) abandon(at time.Time, nonceFinished bool) {
	attempt.markDone(at, nonceFinished)
	attempt.abandoned = true
}

func (attempt *liveAttempt) markDone(at time.Time, nonceFinished bool) {
	attempt.done, attempt.completed, attempt.nonceFinished = true, at, nonceFinished
}
