package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
)

// Errors the engine returns. See README.md, "Errors the engine returns".
var (
	ErrStopped           = errors.New("engine stopped")
	ErrAllAttemptsFailed = errors.New("every attempt failed")
	ErrHostsUnavailable  = errors.New("every host refused the request as unavailable")
	ErrPoCTookTheHosts   = fmt.Errorf("%w: proof of compute started on every host the request reached", ErrHostsUnavailable)
)

const (
	crownDenialStrikes         = 3
	refusedVoteFirstRetryDelay = 30 * time.Second
	refusedVoteRetries         = 4
)

// hostTracker is satisfied by *perf.Tracker.
type hostTracker interface {
	hostPerf
	RecordSample(sample perf.Sample)
}

// hostWindows is satisfied by *limits.ParticipantLimiter; neither Acquire nor a release is on it. See rules.md, "5. The slot and the escrow hold are taken with the nonce, and given back after the vote".
type hostWindows interface {
	OnResult(result limits.Result)
	CountRefusalIfIdle(participant, model string)
}

type raceMetrics interface {
	RecordRace(outcome RaceOutcome)
	RecordAwaitedFinish(outcome RaceOutcome, attempt AttemptOutcome)
	RecordTimeout(event TimeoutEvent)
	RecordClassifyOverflow(participant, model string)
}

// raceLedger runs on the client's response path, so it must return without waiting on its storage.
type raceLedger interface {
	RecordRequest(outcome RaceOutcome)
}

// escrowLifecycle is called on the response path, so an implementation must mark and return, not reach the chain.
type escrowLifecycle interface {
	OnEscrowMissing(escrowID string)
}

// escrowTargets is the dispatch boundary main wires; resolving a target also takes a hold that outlives the race.
type escrowTargets interface {
	Target(escrowID string) (target DispatchTarget, release func(), ok bool)
}

// Deps is what an engine is wired to. See README, "Timeout votes", for why Timeouts is resolved per race.
type Deps struct {
	Picker    picker
	Targets   escrowTargets
	Windows   hostWindows
	Perf      hostTracker
	Snapshots snapshotSource
	Config    *config.Holder
	Metrics   raceMetrics
	Ledger    raceLedger
	Lifecycle escrowLifecycle
	Journal   raceJournal

	Suspicious func(participant string) bool

	Timeouts func(escrowID string, params any) (TimeoutPoster, bool)

	Now   func() time.Time
	Timer func() raceTimer

	E2E E2EOverrides
}

// Request is one client request as a race sees it; OnEscrow fires at the last moment a header can still be set, and Run calls OnFinished exactly once, after the race's last report.
type Request struct {
	RequestID               string
	Model                   string
	Escrow                  string
	InputTokens             uint64
	InputTokensSource       filters.InputTokensSource
	InputTokensFromBytes    uint64
	InputBytes              uint64
	OutputTokens            uint64
	ClientStream            bool
	EmptyAnswerCountsTokens bool

	Params any

	OnEscrow   func(escrowID string)
	OnFinished func()
}

// Engine admits races and is the barrier that outlives them. See race.md, "Stop".
type Engine struct {
	deps           Deps
	carry          *carryBudget
	prefixBudget   *carryBudget
	fleets         *fleetsByModel
	crown          *crownStrikes
	lookups        *lookupRefusals
	settles        *settleQueue
	heldWinners    *heldWinners
	stopping       context.Context
	cancelStopping context.CancelFunc

	mu      sync.Mutex
	stopped bool
	tracked sync.WaitGroup
}

// NewEngine refuses a dependency set it cannot race with, rather than panicking on the first request.
func NewEngine(deps Deps) (*Engine, error) {
	switch {
	case deps.Config == nil:
		return nil, errors.New("engine: Config is required")
	case deps.Picker == nil:
		return nil, errors.New("engine: Picker is required")
	case deps.Targets == nil:
		return nil, errors.New("engine: Targets is required")
	case deps.Windows == nil:
		return nil, errors.New("engine: Windows is required")
	case deps.Perf == nil:
		return nil, errors.New("engine: Perf is required")
	case deps.Snapshots == nil:
		return nil, errors.New("engine: Snapshots is required")
	}
	clock := deps.Now
	if clock == nil {
		clock = time.Now
	}
	stopping, cancelStopping := context.WithCancel(context.Background())
	return &Engine{
		deps:           deps,
		carry:          newCarryBudget(deps.Config.Load().Stream),
		prefixBudget:   newPrefixBudget(),
		fleets:         newFleetsByModel(),
		crown:          newCrownStrikes(deps.Journal),
		lookups:        newLookupRefusals(),
		settles:        newSettleQueue(clock),
		heldWinners:    newHeldWinners(),
		stopping:       stopping,
		cancelStopping: cancelStopping,
	}, nil
}

// Run races one request to a single winner and streams that winner's bytes to client. See README, "From pick to report".
func (e *Engine) Run(ctx context.Context, request Request, client io.Writer) (RaceOutcome, error) {
	request.OnFinished = finishOnce(request.OnFinished)
	registration := e.admit()
	if registration == nil {
		request.OnFinished()
		return RaceOutcome{}, ErrStopped
	}
	// A panicking race still owes the barrier its release. See race.md, "Stop".
	defer func() {
		if panicked := recover(); panicked != nil {
			registration.release()
			request.OnFinished()
			panic(panicked)
		}
	}()
	settings := e.deps.Config.Load()
	outcome, err := runRace(ctx, e.raceDeps(settings, request, registration), raceRequest{Request: request, Client: client})
	if err != nil {
		return outcome, err
	}
	return outcome, outcome.failure()
}

func finishOnce(finished func()) func() {
	if finished == nil {
		return func() {}
	}
	return sync.OnceFunc(finished)
}

// OwedTimeoutVotes reports the votes taken and not yet posted. See race.md, "The timeout-vote queue".
func (e *Engine) OwedTimeoutVotes() int64 { return e.settles.Owed() }

// Stop returns once every race it admitted has posted the vote that settles its nonces. See race.md, "Stop".
func (e *Engine) Stop() {
	e.mu.Lock()
	e.stopped = true
	e.mu.Unlock()
	e.cancelStopping()
	e.tracked.Wait()
}

func (e *Engine) policy(settings config.Engine) EscalationPolicy {
	policy := EscalationPolicyFromConfig(settings)
	policy.HardTimeout = e.deps.E2E.HardTimeout
	return policy
}

// admit registers a race before it starts, returning nil once the engine is stopped. See race.md, "Stop".
func (e *Engine) admit() *raceRegistration {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopped {
		return nil
	}
	e.tracked.Add(1)
	return &raceRegistration{engine: e}
}

// raceRegistration is one race's place in the Stop barrier; releasing is idempotent. See README, "The Stop barrier and the escrow hold".
type raceRegistration struct {
	engine   *Engine
	released atomic.Bool

	mu         sync.Mutex
	escrowHold func()
	target     DispatchTarget
}

// holdEscrow keeps the first hold for as long as the vote is owed; a later one counts the same escrow and goes back.
func (r *raceRegistration) holdEscrow(release func()) {
	if release == nil {
		return
	}
	r.mu.Lock()
	if r.escrowHold == nil && !r.released.Load() {
		r.escrowHold, release = release, nil
	}
	r.mu.Unlock()
	if release != nil {
		release()
	}
}

func (r *raceRegistration) holdTarget(target DispatchTarget, release func()) {
	r.mu.Lock()
	if r.target == nil {
		r.target = target
	}
	r.mu.Unlock()
	r.holdEscrow(release)
}

func (r *raceRegistration) nonceFinished(nonce uint64) bool {
	r.mu.Lock()
	target := r.target
	r.mu.Unlock()
	return target != nil && target.NonceFinished(nonce)
}

func (r *raceRegistration) release() {
	if !r.released.CompareAndSwap(false, true) {
		return
	}
	r.mu.Lock()
	hold := r.escrowHold
	r.escrowHold = nil
	r.mu.Unlock()
	if hold != nil {
		hold()
	}
	r.engine.tracked.Done()
}

// heldTargets hands the coordinator a plain target and parks its hold on the registration.
type heldTargets struct {
	escrows      escrowTargets
	registration *raceRegistration
}

func (h heldTargets) Target(escrowID string) (DispatchTarget, bool) {
	target, release, ok := h.escrows.Target(escrowID)
	if !ok {
		return nil, false
	}
	h.registration.holdTarget(target, release)
	return target, true
}

func (e *Engine) raceDeps(settings *config.Config, request Request, registration *raceRegistration) raceDeps {
	return raceDeps{
		Picker:       e.deps.Picker,
		Targets:      heldTargets{escrows: e.deps.Targets, registration: registration},
		Limiter:      e.deps.Windows,
		Perf:         e.deps.Perf,
		Crown:        suspicionGate{pinned: e.deps.Suspicious, strikes: e.crown},
		Snapshots:    e.deps.Snapshots,
		Policy:       e.policy(settings.Engine),
		Modes:        settings.Modes,
		DrainTimeout: DrainTimeoutFromConfig(settings.Stream),
		Classify:     e.classify(request.Model, request.EmptyAnswerCountsTokens),
		PrefixBudget: e.prefixBudget,
		Fleet:        e.fleets.forModel(request.Model),
		Now:          e.deps.Now,
		Timer:        e.deps.Timer,
		Journal:      e.deps.Journal,

		ModelContextLength: settings.Limits.ContextLength(request.Model, e.deps.Snapshots.Snapshot().Models[request.Model].MaxModelLen),

		Hold: registration.holdEscrow,
		Report: func(outcome RaceOutcome) {
			e.record(outcome, request.Params, registration)
			request.OnFinished()
		},
	}
}

// suspicionGate folds the operator's manual pins into the automatic crowning penalty. See race.md, "Crown denial".
type suspicionGate struct {
	pinned  func(participant string) bool
	strikes *crownStrikes
}

func (g suspicionGate) Denied(participant, model string) bool {
	return (g.pinned != nil && g.pinned(participant)) || g.strikes.Denied(participant, model)
}

func (g suspicionGate) Observe(participant, model string, contentless bool) {
	g.strikes.Observe(participant, model, contentless)
}

func (e *Engine) classify(model string, emptyAnswerCountsTokens bool) func(string) streamClassifier {
	return func(participant string) streamClassifier {
		return newSSEClassifier(e.carry, participant, emptyAnswerCountsTokens, func() {
			if e.deps.Metrics != nil {
				e.deps.Metrics.RecordClassifyOverflow(participant, model)
			}
		})
	}
}

// latencyPressure reads how far a host sits above its own best, in the windows' vocabulary. See capacity.md, "The participant limiter: IOCW".
func latencyPressure(tracker hostPerf, participant, model string) limits.Pressure {
	observed := tracker.Pressure(participant, model)
	return limits.Pressure{Input: observed.FirstContent, Output: observed.Decode}
}

// record translates one outcome into every consumer's vocabulary, applying the exemption ladder once. See rules.md, "2. Exactly one outcome and exactly one winner per race, on every path".
func (e *Engine) record(outcome RaceOutcome, params any, registration *raceRegistration) {
	for index, attempt := range outcome.Attempts {
		outcome.Attempts[index] = e.lookups.judge(attempt, outcome.Model)
	}
	for _, attempt := range outcome.Attempts {
		e.recordSample(outcome, attempt, registration)
		if outcome.overranReservation(attempt) {
			e.deps.Perf.RecordOutputOverrun(attempt.Participant, outcome.Model)
		}
		if verdict, moves := outcome.Verdict(attempt); moves {
			e.deps.Windows.OnResult(limits.Result{
				Participant: attempt.Participant,
				Model:       outcome.Model,
				Verdict:     verdict,
				Carried:     limits.TokenCost{Input: int64(outcome.InputTokens), Output: int64(outcome.OutputTokens)},
				Pressure:    latencyPressure(e.deps.Perf, attempt.Participant, outcome.Model),
			})
		}
	}
	if e.deps.Metrics != nil {
		e.deps.Metrics.RecordRace(outcome)
	}
	// Marked before the row is queued, so an observer that has seen the row has also seen the mark.
	if e.deps.Lifecycle != nil && outcome.Lifecycle.EscrowMissing && outcome.EscrowID != "" {
		e.deps.Lifecycle.OnEscrowMissing(outcome.EscrowID)
	}
	if e.deps.Ledger != nil {
		e.deps.Ledger.RecordRequest(outcome)
	}
	e.settle(outcome, params, registration)
}

// settle posts the chain vote for every nonce the race left unfinished, on its own goroutine. See race.md, "Timeout votes".
func (e *Engine) settle(outcome RaceOutcome, params any, registration *raceRegistration) {
	if len(outcome.TimeoutPlan()) == 0 {
		outcome.releaseHeldFinishes()
		registration.release()
		return
	}
	var poster TimeoutPoster
	if e.deps.Timeouts != nil {
		if resolved, ok := e.deps.Timeouts(outcome.EscrowID, params); ok {
			poster = resolved
		}
	}
	groups := voteGroups(outcome.TimeoutPlan(), poster)
	markPosted := afterAll(len(groups), outcome.releaseHeldFinishes)
	markSettled := afterAll(len(groups), registration.release)
	for _, group := range groups {
		task := settleTask{
			deadline: func() time.Time { return earliestDue(group.steps, poster) },
			post: func() {
				retries := settleSteps(settleContext(outcome.RequestID), poster, group.steps, outcome.Lifecycle.EscrowMissing, e.reportTimeout)
				e.decideHeldWinners(outcome.EscrowID, group.steps, registration)
				markPosted()
				e.retryRefusedVotes(outcome, poster, retries, markSettled, 0)
			},
		}
		e.settles.Add(task, int(e.deps.Config.Load().Engine.MaxConcurrentTimeoutVotes))
	}
}

func (e *Engine) retryRefusedVotes(outcome RaceOutcome, poster TimeoutPoster, steps []TimeoutStep, markSettled func(), round int) {
	if len(steps) == 0 || round >= refusedVoteRetries || e.stopping.Err() != nil {
		markSettled()
		return
	}
	retryAt := e.settles.now().Add(refusedVoteFirstRetryDelay << round)
	task := settleTask{
		deadline: func() time.Time { return retryAt },
		wake:     e.stopping,
		post: func() {
			remaining := postTimeouts(settleContext(outcome.RequestID), poster, steps, outcome.Lifecycle.EscrowMissing, e.reportTimeout)
			e.retryRefusedVotes(outcome, poster, remaining, markSettled, round+1)
		},
	}
	e.settles.Add(task, int(e.deps.Config.Load().Engine.MaxConcurrentTimeoutVotes))
}

func (e *Engine) reportTimeout(event TimeoutEvent) {
	if e.deps.Metrics != nil {
		e.deps.Metrics.RecordTimeout(event)
	}
}
