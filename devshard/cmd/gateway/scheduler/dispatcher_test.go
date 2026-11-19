package scheduler

import (
	"cmp"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/types"
)

const escrowA = "escrow-a"

type committedNonce struct {
	nonce       uint64
	participant string
	ghost       bool
	params      any
}

// scriptedSession binds nonce N to slot N%len(slots), the same rule the real session uses.
type scriptedSession struct {
	balance         uint64
	feePerNonce     uint64
	mu              sync.Mutex
	slots           []string
	nonce           uint64
	advances        int
	declines        int
	commits         []committedNonce
	failWith        error
	failAfterDecide error
	failAfterSpend  error
	keepFailing     bool
	swallowCommit   bool
	gate            chan struct{}
	entered         chan struct{}
	afterDecide     func(HostBinding)
	budgetSpent     map[string]bool
}

func (s *scriptedSession) Advance(decide func(HostBinding) NonceIntent) (Prepared, error) {
	if s.gate != nil {
		select {
		case s.entered <- struct{}{}:
		default:
		}
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.advances++
	if s.failWith != nil {
		return nil, s.failWith
	}
	nonce := s.nonce + 1
	hostIdx := int(nonce % uint64(len(s.slots)))
	binding := HostBinding{Nonce: nonce, HostIdx: hostIdx, Participant: s.slots[hostIdx]}

	intent := decide(binding)
	if s.afterDecide != nil {
		s.afterDecide(binding)
	}
	if s.failAfterDecide != nil {
		return nil, s.failAfterDecide
	}
	if !intent.Commit {
		s.declines++
		return nil, nil
	}
	if s.failAfterSpend != nil {
		s.nonce = nonce
		err := s.failAfterSpend
		if !s.keepFailing {
			s.failAfterSpend = nil
		}
		return nil, NewSpentNonceError(nonce, err)
	}
	if s.swallowCommit {
		return nil, nil
	}
	s.nonce = nonce
	s.commits = append(s.commits, committedNonce{
		nonce:       nonce,
		participant: binding.Participant,
		ghost:       intent.Ghost,
		params:      intent.Params,
	})
	return preparedNonce{nonce: nonce, hostIdx: hostIdx}, nil
}

func (s *scriptedSession) ParticipantKeys() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]bool, len(s.slots))
	keys := make([]string, 0, len(s.slots))
	for _, slot := range s.slots {
		if seen[slot] {
			continue
		}
		seen[slot] = true
		keys = append(keys, slot)
	}
	return keys
}

func (s *scriptedSession) SlotParticipants() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.slots
}

func (s *scriptedSession) ChatBudgetSpent(participant string) bool {
	return s.budgetSpent[participant]
}

func (s *scriptedSession) GroupSize() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.slots)
}

func (s *scriptedSession) LatestNonce() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nonce
}

func (s *scriptedSession) failAdvancing(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failWith = err
}

func (s *scriptedSession) stopFailing() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failAfterDecide = nil
}

func (s *scriptedSession) report() (advances, declines int, commits []committedNonce) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.advances, s.declines, append([]committedNonce(nil), s.commits...)
}

type preparedNonce struct {
	nonce   uint64
	hostIdx int
}

func (p preparedNonce) Nonce() uint64 { return p.nonce }
func (p preparedNonce) HostIdx() int  { return p.hostIdx }

type recordingObserver struct {
	mu             sync.Mutex
	retired        []string
	ghosts         []string
	holds          int
	trips          int
	ghostNonces    []uint64
	burnRequestIDs []string
	excluded       []string
	forced         []forcedSend
	assigned       []assignedNonce
	queueWaits     []time.Duration
}

type assignedNonce struct {
	nonce     uint64
	requestID string
}

// forcedSend is one anti-burn rung firing, as the observer saw it.
type forcedSend struct {
	participant string
	burnsInARow int64
}

func (o *recordingObserver) GhostBurned(_ string, burned Burn) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.ghosts = append(o.ghosts, burned.Reason)
	o.ghostNonces = append(o.ghostNonces, burned.Nonce)
	o.burnRequestIDs = append(o.burnRequestIDs, burned.RequestID)
}

func (o *recordingObserver) NonceHeld(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.holds++
}

func (o *recordingObserver) BurnBudgetExhausted(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.trips++
}

func (o *recordingObserver) EscrowRetired(escrowID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.retired = append(o.retired, escrowID)
}

func (o *recordingObserver) burns() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.ghosts...)
}

func (o *recordingObserver) burnedNonces() []uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]uint64(nil), o.ghostNonces...)
}

func (o *recordingObserver) burnRequests() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.burnRequestIDs...)
}

func (o *recordingObserver) counts() (holds, trips int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.holds, o.trips
}

func (o *recordingObserver) retiredEscrows() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.retired...)
}

func (o *recordingObserver) assignments() []assignedNonce {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]assignedNonce(nil), o.assigned...)
}

func (o *recordingObserver) NonceQueued(_ string, waited time.Duration) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queueWaits = append(o.queueWaits, waited)
}

func (o *recordingObserver) waits() []time.Duration {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]time.Duration(nil), o.queueWaits...)
}

func (o *recordingObserver) NonceAssigned(_ string, nonce uint64, profile RequestProfile) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.assigned = append(o.assigned, assignedNonce{nonce: nonce, requestID: profile.RequestID})
}

func (o *recordingObserver) ExcludedHostServed(_ string, participant string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.excluded = append(o.excluded, participant)
}

func (o *recordingObserver) ForcedSend(_ string, participant string, burnsInARow int64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.forced = append(o.forced, forcedSend{participant: participant, burnsInARow: burnsInARow})
}

func (o *recordingObserver) forcedSends() []forcedSend {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]forcedSend(nil), o.forced...)
}

func (o *recordingObserver) excludedServes() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.excluded...)
}

type testClock struct {
	mu    sync.Mutex
	now   time.Time
	armed chan time.Duration
	fires chan time.Time
}

func newTestClock() *testClock {
	return &testClock{now: baseTime, armed: make(chan time.Duration, 16), fires: make(chan time.Time, 1)}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

func (c *testClock) newTimer(delay time.Duration) (<-chan time.Time, func()) {
	select {
	case c.armed <- delay:
	default:
	}
	return c.fires, func() {}
}

func (c *testClock) awaitArmed(t *testing.T) time.Duration {
	t.Helper()
	select {
	case delay := <-c.armed:
		return delay
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for an armed timer")
		return 0
	}
}

func (c *testClock) fireTimer() { c.fires <- c.Now() }

type fakeSnapshots struct {
	mu       sync.Mutex
	snapshot chain.PhaseSnapshot
	calls    int
}

func (f *fakeSnapshots) Snapshot() chain.PhaseSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.snapshot
}

func (f *fakeSnapshots) set(snapshot chain.PhaseSnapshot) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snapshot = snapshot
}

func (f *fakeSnapshots) fetches() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type harnessConfig struct {
	slots               []string
	pocRequired         func(string) bool
	windowFull          func(string) bool
	cutOff              func(string) bool
	maxConsecutiveBurns int64
	ejected             func(string) bool
	stateBlocked        func(string) bool
	unthrottled         func(string) bool
	afterDecide         func(HostBinding)
	failWith            error
	failAfterDecide     error
	failAfterSpend      error
	keepFailing         bool
	swallowCommit       bool
	refused             []string
	gate                chan struct{}
	submitBuffer        int
	holdStart           bool
	escrowHold          func() (func(), bool)
	escalationHold      func() (func(), bool)
	balance             uint64
	feePerNonce         uint64
	retirementReserve   uint64
}

type harness struct {
	dispatcher *dispatcher
	session    *scriptedSession
	clock      *testClock
	observer   *recordingObserver
	snapshots  *fakeSnapshots
	limiter    *fakeLimiter
	exhausted  *atomic.Pointer[string]
	burnLimit  *atomic.Int64
}

func newHarness(t *testing.T, cfg harnessConfig) *harness {
	t.Helper()
	if len(cfg.slots) == 0 {
		cfg.slots = []string{hostA, hostB}
	}
	session := &scriptedSession{
		slots:           cfg.slots,
		balance:         cfg.balance,
		feePerNonce:     cfg.feePerNonce,
		failWith:        cfg.failWith,
		failAfterDecide: cfg.failAfterDecide,
		failAfterSpend:  cfg.failAfterSpend,
		keepFailing:     cfg.keepFailing,
		swallowCommit:   cfg.swallowCommit,
		gate:            cfg.gate,
		afterDecide:     cfg.afterDecide,
	}
	clock := newTestClock()
	observer := &recordingObserver{}
	snapshots := &fakeSnapshots{}
	limiter := newFakeLimiter()
	for _, participant := range cfg.refused {
		limiter.refuse(participant)
	}

	var exhausted atomic.Pointer[string]
	burnLimit := &atomic.Int64{}
	burnLimit.Store(cfg.maxConsecutiveBurns)

	predicates := func(snapshot chain.PhaseSnapshot) availability {
		return availability{
			pocRequired: func(participant string) bool {
				return snapshot.RequestsBlocked || (cfg.pocRequired != nil && cfg.pocRequired(participant))
			},
			congested: func(participant string) blockReason {
				switch {
				case cfg.cutOff != nil && cfg.cutOff(participant):
					return blockCutOff
				case cfg.windowFull != nil && cfg.windowFull(participant):
					return blockWindowFull
				}
				return blockNone
			},
			ejected: func(participant string) bool {
				return cfg.ejected != nil && cfg.ejected(participant)
			},
			stateBlocked: func(participant string) bool {
				return cfg.stateBlocked != nil && cfg.stateBlocked(participant)
			},
			unthrottled: func(participant string) bool {
				return cfg.unthrottled != nil && cfg.unthrottled(participant)
			},
		}
	}

	dispatcher := newDispatcher(dispatcherDeps{
		escrowID:   escrowA,
		session:    session,
		snapshots:  snapshots,
		predicates: predicates,
		acquireSlot: func(participant string, cost limits.TokenCost, overFullWindow bool) (func(), limits.Admission) {
			if overFullWindow {
				return limiter.Overdraft(participant, modelA, cost)
			}
			return limiter.Acquire(participant, modelA, cost)
		},
		observer:            observer,
		now:                 clock.Now,
		matchWait:           matchWaitWindow,
		maxConsecutiveBurns: burnLimit.Load,
		retirementReserve:   func() uint64 { return cfg.retirementReserve },
		newTimer:            clock.newTimer,
		submitBuffer:        cmp.Or(cfg.submitBuffer, int(config.Defaults().Scheduler.DispatchQueueDepth)),
		onExhausted:         func(escrowID string, reason ExhaustionReason) { exhausted.Store(&escrowID) },
		holdEscrow:          cfg.escrowHold,
		escalationHold:      cfg.escalationHold,
	})
	t.Cleanup(dispatcher.stop)
	if !cfg.holdStart {
		dispatcher.start()
	}
	return &harness{
		dispatcher: dispatcher,
		session:    session,
		clock:      clock,
		observer:   observer,
		exhausted:  &exhausted,
		snapshots:  snapshots,
		limiter:    limiter,
		burnLimit:  burnLimit,
	}
}

// wantSlots asserts the limiter's books: every admission that did not reach a caller was given back.
func (h *harness) wantSlots(t *testing.T, held, admitted int) {
	t.Helper()
	gotHeld, gotAdmitted := h.limiter.slots()
	if gotHeld != held || gotAdmitted != admitted {
		t.Fatalf("slots held/admitted = %d/%d, want %d/%d", gotHeld, gotAdmitted, held, admitted)
	}
}

func (h *harness) submit(t *testing.T, enqueued time.Time, excluded ...string) *waiter {
	t.Helper()
	return h.submitAs(t, "", enqueued, excluded...)
}

func (h *harness) submitAs(t *testing.T, requestID string, enqueued time.Time, excluded ...string) *waiter {
	t.Helper()
	return h.submitCosting(t, RequestProfile{RequestID: requestID, Model: modelA, Exclude: excluded, Params: "payload"}, enqueued)
}

func (h *harness) submitCosting(t *testing.T, profile RequestProfile, enqueued time.Time) *waiter {
	t.Helper()
	queued := newWaiter(profile, enqueued)
	if outcome := h.dispatcher.submitWaiter(queued); outcome != submitAccepted {
		t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
	}
	return queued
}

func awaitReply(t *testing.T, queued *waiter) pickResult {
	t.Helper()
	select {
	case result := <-queued.replyCh:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for a dispatcher reply")
		return pickResult{}
	}
}

func wantAssignment(t *testing.T, result pickResult, host string, nonce uint64) {
	t.Helper()
	if result.err != nil {
		t.Fatalf("reply err = %v, want an assignment", result.err)
	}
	if result.assignment.Escrow != escrowA {
		t.Fatalf("assignment escrow = %q, want %q", result.assignment.Escrow, escrowA)
	}
	if result.assignment.Host != host {
		t.Fatalf("assignment host = %q, want %q", result.assignment.Host, host)
	}
	if result.assignment.Nonce == nil || result.assignment.Nonce.Nonce() != nonce {
		t.Fatalf("assignment nonce = %v, want %d", result.assignment.Nonce, nonce)
	}
}

func wantNoReply(t *testing.T, queued *waiter) {
	t.Helper()
	select {
	case result := <-queued.replyCh:
		t.Fatalf("unexpected reply %+v", result)
	default:
	}
}

// Test flow:
//  1. Build a default harness and submit a request.
//  2. Await the reply and assert it lands on `hostB` at nonce 1.
//  3. Assert the session advanced once, declined nothing, and committed the one real dispatch carrying the request's params.
//  4. Assert no ghost burns were recorded.
func TestDispatcherServesFirstUsableNonce(t *testing.T) {
	test := newHarness(t, harnessConfig{})

	queued := test.submit(t, test.clock.Now())

	wantAssignment(t, awaitReply(t, queued), hostB, 1)
	test.dispatcher.stop()

	advances, declines, commits := test.session.report()
	if advances != 1 || declines != 0 || len(commits) != 1 {
		t.Fatalf("advances=%d declines=%d commits=%d, want 1/0/1", advances, declines, len(commits))
	}
	if commits[0].ghost || commits[0].params != "payload" {
		t.Fatalf("commit = %+v, want a real dispatch carrying the request params", commits[0])
	}
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none", burns)
	}
}

// Test flow:
//  1. Build a default harness and submit a stale request excluding `hostB`.
//  2. Await the reply and assert it lands on `hostA` at nonce 2.
//  3. Assert the session committed a ghost on the first nonce, then a real dispatch on the second.
//  4. Assert the one recorded burn is `ghostExclude`.
func TestDispatcherBurnsUntilACompatibleHostIsBound(t *testing.T) {
	test := newHarness(t, harnessConfig{})
	stale := test.clock.Now().Add(-2 * matchWaitWindow)

	queued := test.submit(t, stale, hostB)

	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()

	_, _, commits := test.session.report()
	if len(commits) != 2 || !commits[0].ghost || commits[1].ghost {
		t.Fatalf("commits = %+v, want one ghost then one real dispatch", commits)
	}
	if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostExclude.reason() {
		t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostExclude.reason())
	}
}

// Test flow:
//  1. Build a default harness and submit two waiters at the same time: one excluding `hostB`, one compatible with either host.
//  2. Await both replies.
//  3. Assert the compatible waiter lands on `hostB` at nonce 1 and the excluding one lands on `hostA` at nonce 2.
//  4. Assert no ghost burns were recorded, since the excluded nonce was usable by the queued request.
func TestDispatcherConservesNonceAcrossWaiters(t *testing.T) {
	test := newHarness(t, harnessConfig{})
	now := test.clock.Now()

	excluding := test.submit(t, now, hostB)
	compatible := test.submit(t, now)

	wantAssignment(t, awaitReply(t, compatible), hostB, 1)
	wantAssignment(t, awaitReply(t, excluding), hostA, 2)
	test.dispatcher.stop()

	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none: the nonce was usable by a queued request", burns)
	}
}

// Test flow:
//  1. Build a harness where every participant is ejected.
//  2. Submit a request and await its reply.
//  3. Assert the error is `ErrNoAvailableHost`.
//  4. Assert the session advanced and committed nothing, so answering a dead end costs no nonce.
func TestDispatcherSweepsAnUnreachableWaiterWithoutAdvancing(t *testing.T) {
	test := newHarness(t, harnessConfig{ejected: func(string) bool { return true }})

	queued := test.submit(t, test.clock.Now())

	if result := awaitReply(t, queued); !errors.Is(result.err, ErrNoAvailableHost) {
		t.Fatalf("err = %v, want ErrNoAvailableHost", result.err)
	}
	test.dispatcher.stop()

	advances, _, commits := test.session.report()
	if advances != 0 || len(commits) != 0 {
		t.Fatalf("advances=%d commits=%d, want the nonce untouched", advances, len(commits))
	}
	if test.session.LatestNonce() != 0 {
		t.Fatalf("nonce = %d, want 0", test.session.LatestNonce())
	}
}

// Test flow:
//  1. Build a default harness and submit a stale request excluding both `hostA` and `hostB`.
//  2. Await the reply.
//  3. Assert there is no error and the assignment carries a nonce, since the request is served rather than swept out as unreachable.
func TestAWaiterEveryHostExcludedIsServedRatherThanFailed(t *testing.T) {
	test := newHarness(t, harnessConfig{})

	queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow), hostA, hostB)

	result := awaitReply(t, queued)
	if result.err != nil {
		t.Fatalf("err = %v, want the nonce spent on an attempt rather than on nobody", result.err)
	}
	if result.assignment.Nonce == nil {
		t.Fatal("the waiter was answered without a nonce to dispatch")
	}
	test.dispatcher.stop()
}

// Test flow:
//  1. Build a default harness and submit a stale request excluding both `hostA` and `hostB`.
//  2. Await the reply and assert it lands on `hostB`, the host nonce 1 binds, at nonce 1.
//  3. Assert the observer recorded `hostB` as an excluded serve.
func TestAServeDespiteExclusionIsReported(t *testing.T) {
	test := newHarness(t, harnessConfig{})

	queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow), hostA, hostB)

	wantAssignment(t, awaitReply(t, queued), hostB, 1)
	test.dispatcher.stop()
	if got := test.observer.excludedServes(); !slices.Equal(got, []string{hostB}) {
		t.Fatalf("excluded serves = %v, want the one host the request had excluded and still got", got)
	}
}

// Test flow:
//  1. Submit a waiter excluding `hostB`, then submit a compatible co-arrival shortly after; assert the co-arrival is served on the held nonce and the excluding waiter on the next one, with one recorded hold and no burns.
//  2. Submit a waiter excluding `hostB` alone, let its hold expire, and assert the expired hold burns the nonce before serving the waiter on the next one.
func TestDispatcherHoldsNonceForCoArrivingWaiter(t *testing.T) {
	t.Run("a compatible co-arrival is served on the held nonce", func(t *testing.T) {
		test := newHarness(t, harnessConfig{})
		now := test.clock.Now()

		holding := test.submit(t, now, hostB)
		if delay := test.clock.awaitArmed(t); delay != matchWaitWindow {
			t.Fatalf("hold delay = %v, want %v", delay, matchWaitWindow)
		}
		advances, declines, commits := test.session.report()
		if advances != 1 || declines != 1 || len(commits) != 0 {
			t.Fatalf("advances=%d declines=%d commits=%d, want the held nonce uncommitted", advances, declines, len(commits))
		}

		coArriving := test.submit(t, now.Add(10*time.Millisecond))

		wantAssignment(t, awaitReply(t, coArriving), hostB, 1)
		wantAssignment(t, awaitReply(t, holding), hostA, 2)
		if burns := test.observer.burns(); len(burns) != 0 {
			t.Fatalf("ghost burns = %v, want none: the held nonce went to the co-arrival", burns)
		}
		if holds, _ := test.observer.counts(); holds != 1 {
			t.Fatalf("recorded holds = %d, want 1", holds)
		}
	})

	t.Run("an expired hold burns the nonce", func(t *testing.T) {
		test := newHarness(t, harnessConfig{})

		holding := test.submit(t, test.clock.Now(), hostB)
		test.clock.awaitArmed(t)
		test.clock.advance(matchWaitWindow)
		test.clock.fireTimer()

		wantAssignment(t, awaitReply(t, holding), hostA, 2)
		test.dispatcher.stop()

		if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostExclude.reason() {
			t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostExclude.reason())
		}
	})
}

// Test flow:
//  1. Build a harness whose window-full predicate flips between calls, and submit a stale request.
//  2. Await the reply.
//  3. Assert there is no error, since a flipping predicate must not starve the waiter.
//  4. Assert no burns and no burn-budget trips were recorded, since availability was frozen for the drain.
func TestDispatcherFreezesAvailabilityWithinADrain(t *testing.T) {
	var calls int
	var mu sync.Mutex
	flipping := func(string) bool {
		mu.Lock()
		defer mu.Unlock()
		calls++
		return calls%2 == 1
	}
	test := newHarness(t, harnessConfig{windowFull: flipping})

	queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow))

	result := awaitReply(t, queued)
	if result.err != nil {
		t.Fatalf("err = %v, want an assignment: a flipping predicate must not starve the waiter", result.err)
	}
	test.dispatcher.stop()

	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none: availability was frozen for the drain", burns)
	}
	if _, trips := test.observer.counts(); trips != 0 {
		t.Fatalf("burn budget trips = %d, want 0", trips)
	}
}

// Test flow:
//  1. Submit a waiter that is abandoned right after its nonce is decided, then submit and await a second request.
//  2. Assert the second request lands on `hostA` at nonce 2, the abandoned waiter gets no reply, and the one recorded burn is `ghostAbandoned`, with the admitted slot given back.
//  3. Submit a waiter whose reply buffer is already full, then submit and await a second request.
//  4. Assert the second request lands the same way, with the same abandoned burn and slot accounting.
func TestDispatcherReclassifiesALostAssignmentAsAGhost(t *testing.T) {
	t.Run("a waiter abandoned after the commit", func(t *testing.T) {
		var abandonOnce sync.Once
		var lost *waiter
		test := newHarness(t, harnessConfig{afterDecide: func(HostBinding) {
			abandonOnce.Do(func() { lost.abandoned.Store(true) })
		}})

		lost = newWaiter(RequestProfile{Model: modelA}, test.clock.Now())
		if outcome := test.dispatcher.submitWaiter(lost); outcome != submitAccepted {
			t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
		}

		next := test.submit(t, test.clock.Now())
		wantAssignment(t, awaitReply(t, next), hostA, 2)
		wantNoReply(t, lost)

		if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostAbandoned.reason() {
			t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostAbandoned.reason())
		}
		test.wantSlots(t, 1, 2)
	})

	t.Run("a waiter whose reply buffer is already full", func(t *testing.T) {
		test := newHarness(t, harnessConfig{})

		lost := newWaiter(RequestProfile{Model: modelA}, test.clock.Now())
		lost.replyCh <- pickResult{err: errors.New("caller already gave up")}
		if outcome := test.dispatcher.submitWaiter(lost); outcome != submitAccepted {
			t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
		}

		next := test.submit(t, test.clock.Now())
		wantAssignment(t, awaitReply(t, next), hostA, 2)

		if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostAbandoned.reason() {
			t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostAbandoned.reason())
		}
		test.wantSlots(t, 1, 2)
	})
}

// Test flow:
//  1. Build a harness whose session fails right after deciding, and submit a request; assert the caller's error is the session's own, and the admitted slot was given back with its escrow hold.
//  2. Build a harness whose session swallows the commit, and submit a request; assert the caller gets a missing-nonce error and the admitted slot is given back.
func TestDispatcherReleasesAnAdmissionThatNeverReachesACaller(t *testing.T) {
	t.Run("the session fails after admitting the request", func(t *testing.T) {
		sessionErr := errors.New("commit rejected")
		holds := &escrowHolds{}
		test := newHarness(t, harnessConfig{failAfterDecide: sessionErr, escrowHold: holds.source()})

		queued := test.submit(t, test.clock.Now())

		if result := awaitReply(t, queued); !errors.Is(result.err, sessionErr) {
			t.Fatalf("err = %v, want the session error", result.err)
		}
		test.wantSlots(t, 0, 1)
		if outstanding := holds.outstanding(); outstanding != 0 {
			t.Fatalf("escrow holds still out = %d, want the serve's hold given back with its slot", outstanding)
		}
	})

	t.Run("the session commits no nonce", func(t *testing.T) {
		test := newHarness(t, harnessConfig{swallowCommit: true})

		queued := test.submit(t, test.clock.Now())

		if result := awaitReply(t, queued); result.err == nil {
			t.Fatalf("result = %+v, want the missing-nonce error", result)
		}
		test.wantSlots(t, 0, 1)
	})
}

// Test flow:
//  1. For each table case (admission refuses the bound host, the bound host owes proof of compute), build a harness whose commit fails after deciding, tracking escrow holds.
//  2. Submit a request and await its reply.
//  3. Assert the error is the failed commit's `types.ErrInsufficientBalance`.
//  4. Assert no escrow holds are left outstanding, so the burn's own hold came back.
func TestDispatcherGivesBackTheHoldOfABurnWhoseCommitFailed(t *testing.T) {
	testCases := []struct {
		name   string
		config harnessConfig
	}{
		{name: "admission refused the bound host", config: harnessConfig{refused: []string{hostB}}},
		{name: "the bound host owes proof of compute", config: harnessConfig{
			pocRequired: func(participant string) bool { return participant == hostB },
		}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			holds := &escrowHolds{}
			settings := testCase.config
			settings.failAfterDecide = types.ErrInsufficientBalance
			settings.escrowHold = holds.source()
			test := newHarness(t, settings)

			queued := test.submit(t, test.clock.Now())

			if result := awaitReply(t, queued); !errors.Is(result.err, types.ErrInsufficientBalance) {
				t.Fatalf("err = %v, want the failed commit's error", result.err)
			}
			if outstanding := holds.outstanding(); outstanding != 0 {
				t.Fatalf("escrow holds still out = %d, want the burn's hold given back", outstanding)
			}
			test.wantSlots(t, 0, 0)
		})
	}
}

// Test flow:
//  1. Build a harness with one host, refused at admission, and submit two requests before starting the dispatcher.
//  2. Start the dispatcher and await both replies.
//  3. Assert both fail with `ErrHostsBusy`.
//  4. Assert exactly one ghost was committed for the bound nonce, and the recorded burn is `ghostWindowFull` naming that same nonce.
//  5. Assert no slots are left held or admitted.
func TestDispatcherGhostsOnceWhenAdmissionRefusesEveryHost(t *testing.T) {
	test := newHarness(t, harnessConfig{slots: []string{hostA}, refused: []string{hostA}, holdStart: true})
	first := test.submit(t, test.clock.Now())
	second := test.submit(t, test.clock.Now())

	test.dispatcher.start()

	for _, queued := range []*waiter{first, second} {
		if result := awaitReply(t, queued); !errors.Is(result.err, ErrHostsBusy) {
			t.Fatalf("err = %v, want ErrHostsBusy", result.err)
		}
	}
	test.dispatcher.stop()

	_, _, commits := test.session.report()
	if len(commits) != 1 || !commits[0].ghost {
		t.Fatalf("commits = %+v, want exactly one ghost for the nonce bound when admission refused", commits)
	}
	if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostWindowFull.reason() {
		t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostWindowFull.reason())
	}
	if nonces := test.observer.burnedNonces(); len(nonces) != 1 || nonces[0] != commits[0].nonce {
		t.Fatalf("burned nonces = %v, want the committed nonce %d", nonces, commits[0].nonce)
	}
	test.wantSlots(t, 0, 0)
}

// Test flow:
//  1. Submit two abandoned waiters excluding `hostB`, then submit and await a live one.
//  2. Assert the live waiter lands on `hostB` at nonce 1.
//  3. Assert neither abandoned waiter gets a reply.
//  4. Assert no hold timer was armed and no hold was recorded, since an abandoned queue never holds.
func TestDispatcherNeverHoldsForAnAbandonedQueue(t *testing.T) {
	test := newHarness(t, harnessConfig{})
	now := test.clock.Now()

	abandoned := make([]*waiter, 2)
	for index := range abandoned {
		queued := newWaiter(RequestProfile{Model: modelA, Exclude: []string{hostB}}, now)
		queued.abandoned.Store(true)
		abandoned[index] = queued
		if outcome := test.dispatcher.submitWaiter(queued); outcome != submitAccepted {
			t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
		}
	}

	live := test.submit(t, now)
	wantAssignment(t, awaitReply(t, live), hostB, 1)
	test.dispatcher.stop()

	for _, queued := range abandoned {
		wantNoReply(t, queued)
	}
	select {
	case delay := <-test.clock.armed:
		t.Fatalf("armed a %v hold for an abandoned queue", delay)
	default:
	}
	if holds, _ := test.observer.counts(); holds != 0 {
		t.Fatalf("recorded holds = %d, want 0", holds)
	}
}

// Test flow:
//  1. Submit two waiters before the dispatcher's loop ever runs, then stop it; assert both fail with `ErrDispatcherStopped` and a later submit reports `submitStopped`.
//  2. Submit four waiters while the actor is busy behind a gate, close the gate, and stop the dispatcher; assert every waiter still received a reply.
func TestDispatcherFailsBufferedWaitersOnStop(t *testing.T) {
	t.Run("waiters submitted before the loop ever ran", func(t *testing.T) {
		test := newHarness(t, harnessConfig{holdStart: true})

		first := newWaiter(RequestProfile{Model: modelA}, test.clock.Now())
		second := newWaiter(RequestProfile{Model: modelA}, test.clock.Now())
		for _, queued := range []*waiter{first, second} {
			if outcome := test.dispatcher.submitWaiter(queued); outcome != submitAccepted {
				t.Fatalf("submitWaiter before stop = %v, want submitAccepted", outcome)
			}
		}

		test.dispatcher.stop()

		for _, queued := range []*waiter{first, second} {
			if result := awaitReply(t, queued); !errors.Is(result.err, ErrDispatcherStopped) {
				t.Fatalf("buffered waiter err = %v, want ErrDispatcherStopped", result.err)
			}
		}
		if outcome := test.dispatcher.submitWaiter(newWaiter(RequestProfile{Model: modelA}, test.clock.Now())); outcome != submitStopped {
			t.Fatalf("submitWaiter after stop = %v, want submitStopped", outcome)
		}
	})

	t.Run("waiters buffered while the actor is busy", func(t *testing.T) {
		gate := make(chan struct{})
		test := newHarness(t, harnessConfig{gate: gate})

		queued := make([]*waiter, 4)
		for index := range queued {
			queued[index] = test.submit(t, test.clock.Now())
		}

		close(gate)
		test.dispatcher.stop()

		for index, waiting := range queued {
			select {
			case <-waiting.replyCh:
			default:
				t.Fatalf("waiter %d was stranded with no reply", index)
			}
		}
	})
}

// Test flow:
//  1. Submit and await a request, served normally.
//  2. Set the snapshot to report requests blocked, then submit and await another request.
//  3. Assert the second reply fails with `ErrHostsBusy` from the refetched snapshot.
//  4. Assert at least two snapshot fetches happened, one per drain.
func TestDispatcherRefetchesTheSnapshotEachDrain(t *testing.T) {
	test := newHarness(t, harnessConfig{})

	served := test.submit(t, test.clock.Now())
	wantAssignment(t, awaitReply(t, served), hostB, 1)

	test.snapshots.set(chain.PhaseSnapshot{RequestsBlocked: true})
	blocked := test.submit(t, test.clock.Now())

	if result := awaitReply(t, blocked); !errors.Is(result.err, ErrHostsBusy) {
		t.Fatalf("err = %v, want ErrHostsBusy from the refetched snapshot", result.err)
	}
	test.dispatcher.stop()

	if fetches := test.snapshots.fetches(); fetches < 2 {
		t.Fatalf("snapshot fetches = %d, want one per drain", fetches)
	}
}

// Test flow:
//  1. Build a harness whose session fails with `types.ErrInsufficientBalance` and a balance below the retirement reserve, submit and await a request, then assert the escrow was reported exhausted.
//  2. Build the same failure with a balance that still covers a capped answer, and assert no report, since it can still recover.
//  3. Build the same failure with a balance that covers a capped answer but not the per-nonce fee on top, and assert the escrow was reported exhausted.
//  4. Build an unrelated session failure and assert no report either, since only a genuine deposit exhaustion is reported.
func TestDispatcherReportsASpentDepositForReplacement(t *testing.T) {
	t.Run("an exhausted deposit is reported", func(t *testing.T) {
		test := newHarness(t, harnessConfig{failWith: types.ErrInsufficientBalance, retirementReserve: 4_096, balance: 100})

		awaitReply(t, test.submit(t, test.clock.Now()))

		reported := test.exhausted.Load()
		if reported == nil || *reported != escrowA {
			t.Fatalf("reported escrow = %v, want %q", reported, escrowA)
		}
	})

	t.Run("a deposit that still covers a capped answer is not", func(t *testing.T) {
		test := newHarness(t, harnessConfig{failWith: types.ErrInsufficientBalance, retirementReserve: 4_096, balance: 1 << 20})

		awaitReply(t, test.submit(t, test.clock.Now()))

		if reported := test.exhausted.Load(); reported != nil {
			t.Fatalf("reported escrow = %q, want no report: it still affords a capped answer", *reported)
		}
	})

	t.Run("a deposit that covers a capped answer but not its nonce fee is reported", func(t *testing.T) {
		test := newHarness(t, harnessConfig{failWith: types.ErrInsufficientBalance, retirementReserve: 4_096, feePerNonce: 100, balance: 4_195})

		awaitReply(t, test.submit(t, test.clock.Now()))

		reported := test.exhausted.Load()
		if reported == nil || *reported != escrowA {
			t.Fatalf("reported escrow = %v, want %q: the chain charges the nonce fee on top of the reserve", reported, escrowA)
		}
	})

	t.Run("an unrelated session failure is not", func(t *testing.T) {
		test := newHarness(t, harnessConfig{failWith: errors.New("session broken")})

		awaitReply(t, test.submit(t, test.clock.Now()))

		if reported := test.exhausted.Load(); reported != nil {
			t.Fatalf("reported escrow = %q, want no report", *reported)
		}
	})
}

// Test flow:
//  1. Build a harness whose session always fails advancing with a plain error.
//  2. Submit a request and await its reply.
//  3. Assert the error is the session's own.
func TestDispatcherFailsWaitersWhenTheSessionErrors(t *testing.T) {
	sessionErr := errors.New("session broken")
	test := newHarness(t, harnessConfig{failWith: sessionErr})

	queued := test.submit(t, test.clock.Now())

	result := awaitReply(t, queued)
	if !errors.Is(result.err, sessionErr) {
		t.Fatalf("err = %v, want the session error", result.err)
	}
}

// Test flow:
//  1. Submit two waiters before starting a dispatcher whose advance fails after deciding, then start it; assert both fail with the depleted escrow's error and one slot stays admitted.
//  2. Build a harness that swallows the commit, submit a stale request excluding `hostB`; assert it fails with `ErrNoAvailableHost` and one burn-budget trip is recorded.
func TestDispatcherLeavesNoWaiterWithoutAWakeUp(t *testing.T) {
	t.Run("an advance that failed on the chosen waiter answers the rest too", func(t *testing.T) {
		test := newHarness(t, harnessConfig{failAfterDecide: types.ErrInsufficientBalance, holdStart: true})
		first := test.submit(t, test.clock.Now())
		second := test.submit(t, test.clock.Now())

		test.dispatcher.start()

		for index, queued := range []*waiter{first, second} {
			if result := awaitReply(t, queued); !errors.Is(result.err, types.ErrInsufficientBalance) {
				t.Fatalf("waiter %d err = %v, want the depleted escrow's error", index, result.err)
			}
		}
		test.wantSlots(t, 0, 1)
	})

	t.Run("a tripped burn budget answers the queue it stopped serving", func(t *testing.T) {
		test := newHarness(t, harnessConfig{swallowCommit: true})

		queued := test.submit(t, test.clock.Now().Add(-2*matchWaitWindow), hostB)

		if result := awaitReply(t, queued); !errors.Is(result.err, ErrNoAvailableHost) {
			t.Fatalf("err = %v, want ErrNoAvailableHost", result.err)
		}
		if _, trips := test.observer.counts(); trips != 1 {
			t.Fatalf("burn budget trips = %d, want 1", trips)
		}
	})
}

// Test flow:
//  1. Submit three waiters before starting a dispatcher whose advance fails after deciding, then start it.
//  2. Assert every one of the three fails, the failed advance reported to each.
//  3. Clear the failure and submit a later arrival.
//  4. Assert the later arrival succeeds once the escrow advances again, served after the failed queue that woke it rather than ahead of it.
func TestDispatcherServesNoLaterArrivalAheadOfAFailedQueue(t *testing.T) {
	test := newHarness(t, harnessConfig{failAfterDecide: types.ErrInsufficientBalance, holdStart: true})
	queued := []*waiter{
		test.submit(t, test.clock.Now()),
		test.submit(t, test.clock.Now()),
		test.submit(t, test.clock.Now()),
	}

	test.dispatcher.start()

	for index, waiting := range queued {
		if result := awaitReply(t, waiting); result.err == nil {
			t.Fatalf("waiter %d was served, want the failed advance reported to it", index)
		}
	}
	test.session.stopFailing()
	if result := awaitReply(t, test.submit(t, test.clock.Now())); result.err != nil {
		t.Fatalf("later arrival err = %v, want an assignment once the escrow advances again", result.err)
	}
}

// Test flow:
//  1. Start an already-started dispatcher, submit a holding waiter, and wait for its hold timer to arm.
//  2. Stop the dispatcher twice in a row.
//  3. Assert the held waiter's reply is `ErrDispatcherStopped`.
//  4. Start the dispatcher again and assert a fresh submit still reports `submitStopped`.
func TestDispatcherLifecycleIsIdempotent(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	test := newHarness(t, harnessConfig{})
	test.dispatcher.start()

	holding := test.submit(t, test.clock.Now(), hostB)
	test.clock.awaitArmed(t)

	test.dispatcher.stop()
	test.dispatcher.stop()

	if result := awaitReply(t, holding); !errors.Is(result.err, ErrDispatcherStopped) {
		t.Fatalf("queued waiter err = %v, want ErrDispatcherStopped", result.err)
	}
	test.dispatcher.start()
	if outcome := test.dispatcher.submitWaiter(newWaiter(RequestProfile{Model: modelA}, test.clock.Now())); outcome != submitStopped {
		t.Fatalf("submitWaiter after stop = %v, want submitStopped", outcome)
	}
}

func (s *scriptedSession) Balance() uint64     { return s.balance }
func (s *scriptedSession) TokenPrice() uint64  { return 1 }
func (s *scriptedSession) FeePerNonce() uint64 { return s.feePerNonce }

// Test flow:
//  1. Freeze an availability that omits the optional `stateBlocked` and `unthrottled` predicates.
//  2. Ask whether a participant is blocked.
//  3. Assert the answer is `blockNone`, so the missing predicates read as no block rather than panicking.
func TestFreezeLeavesAnOmittedOptionalPredicateMissing(t *testing.T) {
	t.Parallel()
	frozen := freeze(availability{
		pocRequired: always(false),
		congested:   windowFullWhen(always(false)),
		ejected:     always(false),
	}, 1)

	if got := frozen.participantBlocked(hostA); got != blockNone {
		t.Errorf("participantBlocked(%q) = %v, want blockNone when neither optional predicate was given", hostA, got)
	}
}

// Test flow:
//  1. Build a default harness and submit "request-7" stale, excluding `hostB`, the host nonce 1 binds.
//  2. Await the reply and assert it lands on `hostA` at nonce 2.
//  3. Assert the burn was recorded during "request-7", the drain that burned nonce 1.
func TestABurnNamesTheRequestItsDrainWasServing(t *testing.T) {
	test := newHarness(t, harnessConfig{})
	stale := test.clock.Now().Add(-2 * matchWaitWindow)

	queued := test.submitAs(t, "request-7", stale, hostB)

	wantAssignment(t, awaitReply(t, queued), hostA, 2)
	test.dispatcher.stop()
	if got := test.observer.burnRequests(); !slices.Equal(got, []string{"request-7"}) {
		t.Fatalf("burned during = %v, want the request the drain was serving", got)
	}
}

// Test flow:
//  1. Submit "request-head", which leaves while its serve takes the escrow hold and before the serve binds it, then "request-next", and start the dispatcher.
//  2. Assert "request-next" is served in the same drain, on `hostB` at nonce 1, the nonce the head's serve gave back uncommitted.
//  3. Assert "request-head" gets no reply, no nonce was burned, and only the served request holds a slot.
func TestAWaiterThatLeavesBeforeItsServeBindsDoesNotStallTheQueue(t *testing.T) {
	var leaveOnce sync.Once
	var head *waiter
	test := newHarness(t, harnessConfig{holdStart: true, escrowHold: func() (func(), bool) {
		leaveOnce.Do(func() { head.abandon() })
		return func() {}, true
	}})
	head = newWaiter(RequestProfile{RequestID: "request-head", Model: modelA}, test.clock.Now())
	if outcome := test.dispatcher.submitWaiter(head); outcome != submitAccepted {
		t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
	}
	next := test.submitAs(t, "request-next", test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, next), hostB, 1)
	test.dispatcher.stop()
	wantNoReply(t, head)
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none for a serve that never bound its waiter", burns)
	}
	test.wantSlots(t, 1, 2)
}

// Test flow:
//  1. Submit "request-lost", abandoned right after its nonce is decided, then submit and await "request-next".
//  2. Assert "request-next" lands on `hostA` at nonce 2.
//  3. Assert the burn was recorded during "request-lost", the request that left, not the one served next.
func TestAnAbandonedAssignmentNamesTheRequestThatLeft(t *testing.T) {
	var abandonOnce sync.Once
	var lost *waiter
	test := newHarness(t, harnessConfig{afterDecide: func(HostBinding) {
		abandonOnce.Do(func() { lost.abandoned.Store(true) })
	}})
	lost = newWaiter(RequestProfile{RequestID: "request-lost", Model: modelA}, test.clock.Now())
	if outcome := test.dispatcher.submitWaiter(lost); outcome != submitAccepted {
		t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
	}

	next := test.submitAs(t, "request-next", test.clock.Now())

	wantAssignment(t, awaitReply(t, next), hostA, 2)
	if got := test.observer.burnRequests(); !slices.Equal(got, []string{"request-lost"}) {
		t.Fatalf("burned during = %v, want the request that left before its nonce reached it", got)
	}
}

// Test flow:
//  1. Submit "request-first" excluding `hostB` and "request-second" compatible, before starting a dispatcher where `hostB` is refused at admission.
//  2. Start the dispatcher and await both replies, landing on `hostA` at nonces 2 and 4.
//  3. Assert both recorded burns name "request-second", the oldest waiter left when each slot meant for it was throttled.
func TestAThrottledBurnNamesTheRequestItsSlotWasMeantFor(t *testing.T) {
	test := newHarness(t, harnessConfig{refused: []string{hostB}, holdStart: true})
	first := test.submitAs(t, "request-first", test.clock.Now(), hostB)
	second := test.submitAs(t, "request-second", test.clock.Now())

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, first), hostA, 2)
	wantAssignment(t, awaitReply(t, second), hostA, 4)
	test.dispatcher.stop()
	if got := test.observer.burnRequests(); !slices.Equal(got, []string{"request-second", "request-second"}) {
		t.Fatalf("burned during = %v, want the request each burned nonce was meant for, never the head of the queue", got)
	}
}

// Test flow:
//  1. Submit "request-head" excluding `hostB`, abandoned mid-decide, then submit "request-middle" and "request-tail" also excluding `hostB`, all stale, before starting the dispatcher.
//  2. Start the dispatcher and await "request-middle" and "request-tail", landing on `hostA` at nonces 2 and 4.
//  3. Assert "request-head" never gets a reply.
//  4. Assert the recorded burns name "request-middle" then "request-tail", the oldest live waiter each time, skipping the abandoned head.
func TestABurnSkipsAnAbandonedWaiterAndNamesTheOldestLiveOne(t *testing.T) {
	var abandonOnce sync.Once
	var head *waiter
	test := newHarness(t, harnessConfig{holdStart: true, afterDecide: func(HostBinding) {
		abandonOnce.Do(func() { head.abandoned.Store(true) })
	}})
	stale := test.clock.Now().Add(-2 * matchWaitWindow)
	head = newWaiter(RequestProfile{RequestID: "request-head", Model: modelA, Exclude: []string{hostB}}, stale)
	if outcome := test.dispatcher.submitWaiter(head); outcome != submitAccepted {
		t.Fatalf("submitWaiter on a running dispatcher = %v, want submitAccepted", outcome)
	}
	middle := test.submitAs(t, "request-middle", stale, hostB)
	tail := test.submitAs(t, "request-tail", stale, hostB)

	test.dispatcher.start()

	wantAssignment(t, awaitReply(t, middle), hostA, 2)
	wantAssignment(t, awaitReply(t, tail), hostA, 4)
	test.dispatcher.stop()
	wantNoReply(t, head)
	if got := test.observer.burnRequests(); !slices.Equal(got, []string{"request-middle", "request-tail"}) {
		t.Fatalf("burned during = %v, want each burn charged to the oldest live waiter, skipping the abandoned head", got)
	}
}

// Test flow:
//  1. Build a harness whose session commits a nonce and then fails reading the host's catch-up, once, and submit two requests.
//  2. Assert the request that nonce was committed for fails alone, the next is served, the spent nonce is burned in its name, and its slot was given back.
func TestDispatcherFailsOnlyTheRequestWhoseNonceWasSpentUnprepared(t *testing.T) {
	test := newHarness(t, harnessConfig{failAfterSpend: errors.New("database is locked")})

	first := test.submitAs(t, "request-first", test.clock.Now())
	second := test.submitAs(t, "request-second", test.clock.Now())

	var spent *SpentNonceError
	if result := awaitReply(t, first); !errors.As(result.err, &spent) || spent.Nonce != 1 {
		t.Fatalf("first request err = %v, want the spent nonce 1", result.err)
	}
	if result := awaitReply(t, second); result.err != nil {
		t.Fatalf("second request err = %v, want it served", result.err)
	}
	if burns, nonces, requests := test.observer.burns(), test.observer.burnedNonces(), test.observer.burnRequests(); len(burns) != 1 || burns[0] != GhostReasonPrepareFailed || nonces[0] != 1 || requests[0] != "request-first" {
		t.Fatalf("burns = %v nonces = %v requests = %v, want nonce 1 burned for request-first's failed prepare", burns, nonces, requests)
	}
	test.wantSlots(t, 1, 2)
}

// Test flow:
//  1. Build a harness whose session commits a nonce and fails preparing it on every pass, and submit two requests.
//  2. Assert each request fails once and the drain stops: a lasting store failure costs one nonce per request, not one per pass.
func TestDispatcherStopsSpendingNoncesOnALastingPrepareFailure(t *testing.T) {
	test := newHarness(t, harnessConfig{failAfterSpend: errors.New("database is locked"), keepFailing: true})

	first := test.submitAs(t, "request-first", test.clock.Now())
	second := test.submitAs(t, "request-second", test.clock.Now())

	if result := awaitReply(t, first); result.err == nil {
		t.Fatal("first request served, want it failed")
	}
	if result := awaitReply(t, second); result.err == nil {
		t.Fatal("second request served, want it failed")
	}
	if burns := test.observer.burns(); len(burns) != 2 {
		t.Fatalf("burns = %v, want one spent nonce per request", burns)
	}
}
