package scheduler

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/types"
)

const escrowB = "escrow-b"

// fakeLimiter keeps the peek (`unavailable`) and the admission authority (`refused`) separately settable.
type fakeLimiter struct {
	mu          sync.Mutex
	unavailable map[string]bool
	cutOff      map[string]bool
	refused     map[string]bool
	window      int
	inflight    map[string]int
	admitted    int
	forced      int
	models      []string
	charged     []limits.TokenCost
}

func newFakeLimiter() *fakeLimiter {
	return &fakeLimiter{
		unavailable: map[string]bool{},
		cutOff:      map[string]bool{},
		refused:     map[string]bool{},
		inflight:    map[string]int{},
	}
}

func (f *fakeLimiter) Admits(participant, model string) limits.Admission {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.models = append(f.models, model)
	switch {
	case f.cutOff[participant]:
		return limits.AdmissionCutOff
	case f.unavailable[participant] || !f.hasRoomLocked(participant):
		return limits.AdmissionWindowFull
	}
	return limits.AdmissionOpen
}

func (f *fakeLimiter) Acquire(participant, model string, cost limits.TokenCost) (func(), limits.Admission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charged = append(f.charged, cost)
	if f.cutOff[participant] {
		return nil, limits.AdmissionCutOff
	}
	if f.refused[participant] || !f.hasRoomLocked(participant) {
		return nil, limits.AdmissionWindowFull
	}
	f.inflight[participant]++
	f.admitted++
	return func() { f.release(participant) }, limits.AdmissionOpen
}

// Overdraft takes the tokens whatever the window says, which is what the forced send asks of the real limiter.
func (f *fakeLimiter) Overdraft(participant, model string, cost limits.TokenCost) (func(), limits.Admission) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.charged = append(f.charged, cost)
	if f.cutOff[participant] {
		return nil, limits.AdmissionCutOff
	}
	f.inflight[participant]++
	f.admitted++
	f.forced++
	return func() { f.release(participant) }, limits.AdmissionOpen
}

// release is not idempotent on purpose, so a slot given back twice shows up as a negative hold.
func (f *fakeLimiter) release(participant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inflight[participant]--
}

func (f *fakeLimiter) hasRoomLocked(participant string) bool {
	return f.window <= 0 || f.inflight[participant] < f.window
}

func (f *fakeLimiter) block(participant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable[participant] = true
}

// refuse leaves the peek saying yes, which is the window filling between selection and the commit.
func (f *fakeLimiter) refuse(participant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refused[participant] = true
}

// cutOffHost is the block a forced send may not cross, unlike a full window.
func (f *fakeLimiter) cutOffHost(participant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutOff[participant] = true
}

func (f *fakeLimiter) overdrafts() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forced
}

func (f *fakeLimiter) chargedCosts() []limits.TokenCost {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]limits.TokenCost(nil), f.charged...)
}

func (f *fakeLimiter) askedModels() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.models...)
}

// slots reports how many admissions have not been given back.
func (f *fakeLimiter) slots() (held, admitted int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, count := range f.inflight {
		held += count
	}
	return held, f.admitted
}

type fakePerf struct {
	mu      sync.Mutex
	ejected map[string]bool
}

func (f *fakePerf) Ejected(participant, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ejected[participant]
}

func (f *fakePerf) eject(participant string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ejected[participant] = true
}

type schedulerConfig struct {
	escrows       []string
	slots         []string
	slotsByEscrow map[string][]string
	matchWaitMS   int64
	submitBuffer  int
	hostWindow    int
	gate          chan struct{}
	health        hostHealth
	budgetSpent   []string
}

// escrowHolds counts the in-flight holds the commit takes.
type escrowHolds struct {
	mu   sync.Mutex
	open int
}

func (h *escrowHolds) source() func() (func(), bool) {
	return func() (func(), bool) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.open++
		var once sync.Once
		return func() {
			once.Do(func() {
				h.mu.Lock()
				defer h.mu.Unlock()
				h.open--
			})
		}, true
	}
}

func (h *escrowHolds) outstanding() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.open
}

// exhaustionLog records the escrows routing asked the rotation lifecycle to replace.
type exhaustionLog struct {
	mu       sync.Mutex
	reported []string
}

func (e *exhaustionLog) record(escrowID string, reason ExhaustionReason) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.reported = append(e.reported, escrowID+":"+string(reason))
}

func (e *exhaustionLog) all() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.reported...)
}

type schedulerHarness struct {
	scheduler *Scheduler
	exhausted *exhaustionLog
	reserves  *reserveTakenRecorder
	escrows   *fakeEscrows
	holds     *escrowHolds
	weights   *fakeWeights
	sessions  map[string]*scriptedSession
	limiter   *fakeLimiter
	perf      *fakePerf
	snapshots *fakeSnapshots
	observer  *recordingObserver
	clock     *testClock
}

func newSchedulerHarness(t *testing.T, cfg schedulerConfig) *schedulerHarness {
	t.Helper()
	if len(cfg.escrows) == 0 {
		cfg.escrows = []string{escrowA}
	}
	if len(cfg.slots) == 0 {
		cfg.slots = []string{hostA, hostB}
	}

	escrows := &fakeEscrows{byModel: map[string][]Escrow{}}
	weights := &fakeWeights{byEscrow: map[string]float64{}}
	sessions := map[string]*scriptedSession{}
	holds := &escrowHolds{}
	for _, escrowID := range cfg.escrows {
		slots := cfg.slots
		if own, named := cfg.slotsByEscrow[escrowID]; named {
			slots = own
		}
		session := &scriptedSession{balance: 1 << 40, slots: slots, gate: cfg.gate, entered: make(chan struct{}, 1)}
		session.budgetSpent = map[string]bool{}
		for _, participant := range cfg.budgetSpent {
			session.budgetSpent[participant] = true
		}
		sessions[escrowID] = session
		escrows.byModel[modelA] = append(escrows.byModel[modelA], Escrow{ID: escrowID, Model: modelA, Session: session, Hold: holds.source()})
		weights.byEscrow[escrowID] = 10
	}

	settings := config.Defaults()
	settings.Scheduler.MatchWaitMS = cfg.matchWaitMS
	if cfg.submitBuffer > 0 {
		settings.Scheduler.DispatchQueueDepth = int64(cfg.submitBuffer)
	}

	limiter := newFakeLimiter()
	limiter.window = cfg.hostWindow
	test := &schedulerHarness{
		escrows:   escrows,
		holds:     holds,
		weights:   weights,
		sessions:  sessions,
		limiter:   limiter,
		perf:      &fakePerf{ejected: map[string]bool{}},
		snapshots: &fakeSnapshots{},
		observer:  &recordingObserver{},
		clock:     newTestClock(),
		exhausted: &exhaustionLog{},
		reserves:  &reserveTakenRecorder{},
	}
	health := cfg.health
	if health == nil {
		health = test.perf
	}
	scheduler, err := NewScheduler(Deps{
		Escrows:           escrows,
		Capacity:          weights,
		Limiter:           test.limiter,
		Perf:              health,
		Snapshots:         test.snapshots,
		Config:            config.NewHolder(&settings),
		Observer:          test.observer,
		Now:               test.clock.Now,
		OnEscrowExhausted: test.exhausted.record,
		OnReserveTaken:    test.reserves.record,
	})
	if err != nil {
		t.Fatalf("NewScheduler() = %v, want a wired scheduler", err)
	}
	test.scheduler = scheduler
	test.scheduler.newTimer = test.clock.newTimer
	t.Cleanup(test.scheduler.Stop)
	return test
}

func (h *schedulerHarness) session(t *testing.T, escrowID string) *scriptedSession {
	t.Helper()
	session, ok := h.sessions[escrowID]
	if !ok {
		t.Fatalf("no session for escrow %q", escrowID)
	}
	return session
}

func (h *schedulerHarness) escrow(t *testing.T, escrowID string) Escrow {
	t.Helper()
	for _, candidate := range h.escrows.byModel[modelA] {
		if candidate.ID == escrowID {
			return candidate
		}
	}
	t.Fatalf("no escrow %q", escrowID)
	return Escrow{}
}

func (h *schedulerHarness) liveDispatchers() map[string]*dispatcher {
	h.scheduler.registryMu.Lock()
	defer h.scheduler.registryMu.Unlock()
	live := make(map[string]*dispatcher, len(h.scheduler.dispatchers))
	maps.Copy(live, h.scheduler.dispatchers)
	return live
}

// loadEscrow raises a candidate's in-flight count.
func (h *schedulerHarness) loadEscrow(t *testing.T, escrowID string, activeUsers int) {
	t.Helper()
	for index, candidate := range h.escrows.byModel[modelA] {
		if candidate.ID == escrowID {
			h.escrows.byModel[modelA][index].ActiveUsers = activeUsers
			return
		}
	}
	t.Fatalf("no escrow %q to load", escrowID)
}

func (h *schedulerHarness) queueDepth(escrowID string) int {
	h.scheduler.registryMu.Lock()
	defer h.scheduler.registryMu.Unlock()
	active, ok := h.scheduler.dispatchers[escrowID]
	if !ok {
		return 0
	}
	return len(active.submit)
}

func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func wantHost(t *testing.T, assignment Assignment, err error, escrowID, host string, nonce uint64) {
	t.Helper()
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}
	if assignment.Escrow != escrowID {
		t.Fatalf("assignment escrow = %q, want %q", assignment.Escrow, escrowID)
	}
	if assignment.Host != host {
		t.Fatalf("assignment host = %q, want %q", assignment.Host, host)
	}
	if assignment.Nonce == nil || assignment.Nonce.Nonce() != nonce {
		t.Fatalf("assignment nonce = %v, want %d", assignment.Nonce, nonce)
	}
}

// Test flow:
//  1. Build a scheduler harness and pick for a request.
//  2. Assert the assignment lands on `escrowA`'s `hostB` at nonce 1.
//  3. Assert the escrow's session committed one real dispatch carrying the request's params.
func TestPickAssignsFromTheEscrowItChose(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, Params: "payload"})

	wantHost(t, assignment, err, escrowA, hostB, 1)
	_, _, commits := test.session(t, escrowA).report()
	if len(commits) != 1 || commits[0].ghost || commits[0].params != "payload" {
		t.Fatalf("commits = %+v, want one real dispatch carrying the request params", commits)
	}
}

// Test flow:
//  1. Build a scheduler harness with two escrows and load `escrowA` with 9 active users.
//  2. Pick for a request.
//  3. Assert the assignment lands on `escrowB`.
//  4. Assert the busier `escrowA` advanced no nonces.
func TestPickRoutesToTheLeastLoadedEscrow(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA, escrowB}})
	test.escrows.byModel[modelA][0].ActiveUsers = 9

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, assignment, err, escrowB, hostB, 1)
	if advances, _, _ := test.session(t, escrowA).report(); advances != 0 {
		t.Fatalf("the busier escrow advanced %d nonces, want 0", advances)
	}
}

// Test flow:
//  1. Pick a first request and assert it lands on `escrowA`'s `hostB` at nonce 1, recording the live dispatchers and weight lookups so far.
//  2. Pick again, pinned to the same escrow and excluding the first host.
//  3. Assert the escalation lands on `hostA` at nonce 2.
//  4. Assert only one dispatcher is live, the same one as before, and no new weight lookup happened.
func TestPickEscalationReusesTheEscrowsDispatcher(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})

	first, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
	wantHost(t, first, err, escrowA, hostB, 1)
	created := test.liveDispatchers()
	weighed := len(test.weights.lookups)

	escalated, err := test.scheduler.Pick(context.Background(), RequestProfile{
		Model:   modelA,
		Escrow:  first.Escrow,
		Exclude: []string{first.Host},
	})

	wantHost(t, escalated, err, escrowA, hostA, 2)
	live := test.liveDispatchers()
	if len(live) != 1 || live[escrowA] != created[escrowA] {
		t.Fatalf("escalation ran on %d dispatcher(s) and replaced the original: %v", len(live), live)
	}
	if len(test.weights.lookups) != weighed {
		t.Fatalf("escalation re-derived the escrow: weighed %v", test.weights.lookups[weighed:])
	}
}

// Test flow:
//  1. Build a scheduler harness with a 200ms match wait, and start a `Pick` in a goroutine for a request excluding the one host its nonce binds, so it holds rather than serves or burns.
//  2. Assert the hold timer armed with the configured 200ms delay.
//  3. Cancel the context and assert `Pick` returns `context.Canceled`.
//  4. Advance the clock past the hold and fire the timer, waiting for the drain that follows.
//  5. Assert the session advanced once, declined once, and committed nothing, with no ghost burns, since the abandoned request cost no nonce.
func TestPickReturnsTheContextErrorWhenCancelledWhileQueued(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{matchWaitMS: 200})
	ctx, cancel := context.WithCancel(context.Background())

	picked := make(chan error, 1)
	go func() {
		_, err := test.scheduler.Pick(ctx, RequestProfile{Model: modelA, Exclude: []string{hostB}})
		picked <- err
	}()
	if delay := test.clock.awaitArmed(t); delay != 200*time.Millisecond {
		t.Fatalf("hold delay = %v, want the configured grace", delay)
	}

	cancel()

	select {
	case err := <-picked:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Pick err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pick did not return after its context was cancelled")
	}

	drains := test.snapshots.fetches()
	test.clock.advance(200 * time.Millisecond)
	test.clock.fireTimer()
	eventually(t, "the drain that follows the expired hold", func() bool { return test.snapshots.fetches() > drains })

	advances, declines, commits := test.session(t, escrowA).report()
	if advances != 1 || declines != 1 || len(commits) != 0 {
		t.Fatalf("advances=%d declines=%d commits=%+v, want the abandoned request to cost no nonce", advances, declines, commits)
	}
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none", burns)
	}
	test.scheduler.Stop()
}

// Test flow:
//  1. Run 200 races of `Pick` against an immediately cancelled context, releasing the host slot and escrow hold whenever a caller wins one.
//  2. Assert no slots are left held after every race.
//  3. Assert no escrow holds are left outstanding.
//  4. Assert every committed nonce was taken by a caller and none was burned as abandoned.
func TestPickReleasesTheSlotWhenCancellationRacesTheAssignment(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})
	const races = 200

	takenByCallers := 0
	for range races {
		ctx, cancel := context.WithCancel(context.Background())
		go cancel()
		assignment, err := test.scheduler.Pick(ctx, RequestProfile{Model: modelA})
		if err == nil {
			takenByCallers++
			assignment.ReleaseHostSlot()
			assignment.ReleaseEscrow()
		}
	}
	test.scheduler.Stop()

	held, _ := test.limiter.slots()
	if held != 0 {
		t.Fatalf("slots held = %d after %d races, want every admission accounted for", held, races)
	}
	if outstanding := test.holds.outstanding(); outstanding != 0 {
		t.Fatalf("escrow holds still out = %d after %d races, want every hold accounted for", outstanding, races)
	}
	if _, _, commits := test.session(t, escrowA).report(); len(commits) != takenByCallers {
		t.Fatalf("commits = %d, want the %d taken by callers", len(commits), takenByCallers)
	}
	if burns := test.observer.burns(); slices.Contains(burns, ghostAbandoned.reason()) {
		t.Fatalf("ghost burns = %v, want no committed nonce burned as abandoned", burns)
	}
}

// Test flow:
//  1. Build a scheduler harness with two escrows and block `hostB` on `escrowA` alone.
//  2. Pick pinned to `escrowA` and assert it lands on `hostA` at nonce 2.
//  3. Pick pinned to `escrowB` and assert it lands on `hostB` at nonce 1, unaffected.
//  4. Assert `escrowA`'s commits show the blocked host ghosted then a real dispatch, and `escrowB`'s commits show one plain dispatch.
func TestBlockHostAppliesToOneEscrowOnly(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA, escrowB}})

	test.scheduler.BlockHost(escrowA, hostB)

	blocked, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, Escrow: escrowA})
	wantHost(t, blocked, err, escrowA, hostA, 2)
	unaffected, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, Escrow: escrowB})
	wantHost(t, unaffected, err, escrowB, hostB, 1)

	_, _, blockedCommits := test.session(t, escrowA).report()
	if len(blockedCommits) != 2 || !blockedCommits[0].ghost || blockedCommits[0].participant != hostB {
		t.Fatalf("blocked escrow commits = %+v, want the blocked host ghosted then a real dispatch", blockedCommits)
	}
	_, _, otherCommits := test.session(t, escrowB).report()
	if len(otherCommits) != 1 || otherCommits[0].ghost {
		t.Fatalf("unblocked escrow commits = %+v, want a single real dispatch on the same host", otherCommits)
	}
}

// Test flow:
//  1. Read the scheduler's state-blocked predicate for `escrowA` before blocking `hostB`.
//  2. Assert it does not yet report `hostB` blocked.
//  3. Block `hostB` on `escrowA`.
//  4. Assert the same predicate, held from before the block, now reports `hostB` blocked.
func TestABlockIsVisibleToThePredicateADrainAlreadyHolds(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA}})

	blocked := test.scheduler.stateBlocked(escrowA)
	if blocked(hostB) {
		t.Fatal("stateBlocked() before BlockHost = true, want false")
	}

	test.scheduler.BlockHost(escrowA, hostB)

	if !blocked(hostB) {
		t.Fatal("stateBlocked() held from before the block = false, want true")
	}
}

// Test flow:
//  1. For each table case, arrange one source of unavailability for `hostB`: outside the PoC-preserved set, reported unavailable by the limiter, cut off by the limiter, ejected by the outlier detector, or already excluded by the request.
//  2. Pick for a request.
//  3. Assert the assignment lands on `hostA` at nonce 2.
//  4. Assert exactly one burn was recorded, with the reason matching the case's source.
func TestPickWiresEachAvailabilityPredicateToItsSource(t *testing.T) {
	testCases := []struct {
		name       string
		arrange    func(test *schedulerHarness)
		profile    RequestProfile
		wantReason string
	}{
		{
			name:       "a participant outside the PoC-preserved set",
			arrange:    func(test *schedulerHarness) { test.snapshots.set(chain.PhaseSnapshot{Preserved: []string{hostA}}) },
			profile:    RequestProfile{Model: modelA},
			wantReason: ghostPoC.reason(),
		},
		{
			name:       "a participant the limiter reports unavailable",
			arrange:    func(test *schedulerHarness) { test.limiter.block(hostB) },
			profile:    RequestProfile{Model: modelA},
			wantReason: ghostWindowFull.reason(),
		},
		{
			name:       "a participant the limiter has cut off",
			arrange:    func(test *schedulerHarness) { test.limiter.cutOffHost(hostB) },
			profile:    RequestProfile{Model: modelA},
			wantReason: ghostCutOff.reason(),
		},
		{
			name:       "a participant the outlier detector ejected",
			arrange:    func(test *schedulerHarness) { test.perf.eject(hostB) },
			profile:    RequestProfile{Model: modelA},
			wantReason: ghostEjected.reason(),
		},
		{
			name:       "a participant the request already raced",
			arrange:    func(*schedulerHarness) {},
			profile:    RequestProfile{Model: modelA, Exclude: []string{hostB}},
			wantReason: ghostExclude.reason(),
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			test := newSchedulerHarness(t, schedulerConfig{})
			testCase.arrange(test)

			assignment, err := test.scheduler.Pick(context.Background(), testCase.profile)

			wantHost(t, assignment, err, escrowA, hostA, 2)
			if burns := test.observer.burns(); len(burns) != 1 || burns[0] != testCase.wantReason {
				t.Fatalf("ghost burns = %v, want exactly one %q", burns, testCase.wantReason)
			}
		})
	}
}

// failUntilEjected feeds the failing hosts enough consecutive failures to trip the detector, and the healthy hosts one success each.
func failUntilEjected(tracker *perf.Tracker, healthy []string, failing ...string) {
	for _, participant := range healthy {
		tracker.RecordSample(perf.Sample{ParticipantKey: participant, Model: modelA, Responsive: true})
	}
	for range config.Defaults().Perf.ConsecutiveFailThreshold {
		for _, participant := range failing {
			tracker.RecordSample(perf.Sample{ParticipantKey: participant, Model: modelA})
		}
	}
}

// Test flow:
//  1. Build a real `perf.Tracker` and feed it enough failure samples to eject `hostB`, then build a scheduler harness using that tracker as its health source.
//  2. Pick for a request.
//  3. Assert the assignment lands on `hostA` at nonce 2.
//  4. Assert exactly one burn was recorded for `ghostEjected`.
//  5. Assert no commit ever dispatched for real to the ejected host.
func TestPickWithholdsAHostTheOutlierDetectorEjected(t *testing.T) {
	leakcheck.VerifyNone(t)
	settings := config.Defaults()
	settings.Perf.MinAvailableHosts = 1
	tracker := perf.NewTracker(config.NewHolder(&settings), time.Now)
	failUntilEjected(tracker, []string{hostA}, hostB)
	test := newSchedulerHarness(t, schedulerConfig{health: tracker})

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, assignment, err, escrowA, hostA, 2)
	if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostEjected.reason() {
		t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostEjected.reason())
	}
	_, _, commits := test.session(t, escrowA).report()
	for _, commit := range commits {
		if commit.participant == hostB && !commit.ghost {
			t.Fatalf("commits = %+v, want no real dispatch to the ejected host", commits)
		}
	}
}

// Test flow:
//  1. Build a real `perf.Tracker` and feed it enough failure samples to fail both hosts, then build a scheduler harness using that tracker.
//  2. Pick for a request.
//  3. Assert the pick succeeds with a host the pool-wide cap kept in rotation.
//  4. Assert the tracker does not report that host ejected, though it reports it degraded.
func TestPickStillServesWhenEveryHostIsFailingAtOnce(t *testing.T) {
	leakcheck.VerifyNone(t)
	settings := config.Defaults()
	tracker := perf.NewTracker(config.NewHolder(&settings), time.Now)
	failUntilEjected(tracker, nil, hostA, hostB)
	test := newSchedulerHarness(t, schedulerConfig{health: tracker})

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
	if err != nil {
		t.Fatalf("Pick: %v, want a host the cap kept in rotation", err)
	}
	if tracker.Ejected(assignment.Host, modelA) {
		t.Fatalf("assignment host = %q, which the gate reports as withheld from routing", assignment.Host)
	}
	if !tracker.Degraded(assignment.Host, modelA) {
		t.Fatalf("assignment host = %q, want one the detector wanted out but the cap kept", assignment.Host)
	}
}

// Test flow:
//  1. Build a scheduler harness and make `hostB` pass the peek but fail admission.
//  2. Pick for a request.
//  3. Assert the assignment lands on `hostA` at nonce 2.
//  4. Assert the refused host's nonce was committed as a ghost and the admitted host's nonce dispatched for real.
//  5. Assert exactly one burn was recorded for `ghostWindowFull`.
//  6. Assert only the served host's slot is held and admitted.
func TestPickGhostsTheNonceWhenAdmissionRefusesTheBoundHost(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})
	test.limiter.refuse(hostB)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, assignment, err, escrowA, hostA, 2)
	_, _, commits := test.session(t, escrowA).report()
	if len(commits) != 2 || !commits[0].ghost || commits[0].participant != hostB {
		t.Fatalf("commits = %+v, want the refused host's nonce committed as a ghost", commits)
	}
	if commits[1].ghost || commits[1].participant != hostA {
		t.Fatalf("commits = %+v, want the admitted host's nonce dispatched for real", commits)
	}
	if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostWindowFull.reason() {
		t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostWindowFull.reason())
	}
	if held, admitted := test.limiter.slots(); held != 1 || admitted != 1 {
		t.Fatalf("slots held/admitted = %d/%d, want only the served host's slot taken", held, admitted)
	}
}

// Test flow:
//  1. Build a scheduler harness with one host and a window of one, and run 8 callers picking concurrently.
//  2. Assert exactly one caller is served and the rest are rejected with `ErrHostsBusy`.
//  3. Assert every nonce but the served one was ghosted and recorded as a burn.
//  4. Assert only the one served slot is held and admitted.
//  5. Release the served caller's host slot and assert no slots are left held.
func TestPickAdmitsExactlyOneCallerThroughAWindowOfOne(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{slots: []string{hostA}, hostWindow: 1})
	const callers = 8

	var group sync.WaitGroup
	served := make(chan Assignment, callers)
	rejected := make(chan error, callers)
	for range callers {
		group.Go(func() {
			assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
			if err != nil {
				rejected <- err
				return
			}
			served <- assignment
		})
	}
	group.Wait()
	close(served)
	close(rejected)

	if len(served) != 1 {
		t.Fatalf("served = %d callers, want exactly one through a window of one", len(served))
	}
	for err := range rejected {
		if !errors.Is(err, ErrHostsBusy) {
			t.Fatalf("rejected caller err = %v, want ErrHostsBusy", err)
		}
	}
	_, _, commits := test.session(t, escrowA).report()
	ghosts := 0
	for _, commit := range commits {
		if commit.ghost {
			ghosts++
		}
	}
	if burns := test.observer.burns(); ghosts != len(commits)-1 || len(burns) != ghosts {
		t.Fatalf("commits = %+v with burns %v, want every nonce but the served one ghosted and recorded", commits, burns)
	}
	if held, admitted := test.limiter.slots(); held != 1 || admitted != 1 {
		t.Fatalf("slots held/admitted = %d/%d, want the one admission still held by its caller", held, admitted)
	}

	(<-served).ReleaseHostSlot()

	if held, _ := test.limiter.slots(); held != 0 {
		t.Fatalf("slots held after the caller released = %d, want 0", held)
	}
}

// Test flow:
//  1. Pick for a request naming `modelA`.
//  2. Assert the pick succeeds.
//  3. Assert every model the limiter was asked about is `modelA`.
func TestPickAsksTheLimiterAboutTheRequestsOwnModel(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}

	for _, model := range test.limiter.askedModels() {
		if model != modelA {
			t.Fatalf("limiter asked about model %q, want %q", model, modelA)
		}
	}
}

// Test flow:
//  1. Pick for a request carrying 900 input tokens and 120 output tokens.
//  2. Assert the pick succeeds.
//  3. Assert the limiter was charged exactly once, for input 900 and output 120, each window taken in its own currency.
func TestPickChargesTheHostWindowsWhatTheRequestIsWorth(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, InputTokens: 900, OutputTokens: 120})
	if err != nil {
		t.Fatalf("Pick: %v", err)
	}

	charged := test.limiter.chargedCosts()
	if len(charged) != 1 {
		t.Fatalf("limiter charged %d times, want once for the one admitted attempt", len(charged))
	}
	if charged[0] != (limits.TokenCost{Input: 900, Output: 120}) {
		t.Errorf("charged %+v, want input 900 and output 120: each window is taken in its own currency", charged[0])
	}
}

// Test flow:
//  1. For each table case of a request profile, call `slotCost`.
//  2. Assert the result matches the case: both halves priced when both are set, only the input half when nothing capped the output, and negative counts priced as zero.
func TestSlotCostPricesARequestInBothCurrencies(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		profile RequestProfile
		want    limits.TokenCost
	}{
		{"both halves", RequestProfile{InputTokens: 900, OutputTokens: 120}, limits.TokenCost{Input: 900, Output: 120}},
		{"an answer nobody capped", RequestProfile{InputTokens: 900}, limits.TokenCost{Input: 900}},
		{"counts below zero are worth nothing", RequestProfile{InputTokens: -1, OutputTokens: -1}, limits.TokenCost{}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := slotCost(testCase.profile); got != testCase.want {
				t.Errorf("slotCost() = %+v, want %+v: prefill is priced from the prompt and decode from the answer", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Set a nil preserved set on the snapshot, pick for a request, and assert it lands on `hostB` at nonce 1 with no burns, since an unloaded set must not ghost the whole group.
//  2. Set an empty, loaded preserved set and assert the pick fails with `ErrHostsBusy`, since a loaded but empty set preserves nobody.
//  3. Set a global preserved set and a narrower per-model one, and assert the per-model set wins, ghosting the host it excludes with `ghostPoC`.
//  4. Set a per-model set for a different model beside a global one, and assert the other model's set does not shadow the global one.
func TestPickTreatsAnUnloadedPreservedSetAsAllPreserved(t *testing.T) {
	t.Run("a nil set serves every participant", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{})
		test.snapshots.set(chain.PhaseSnapshot{Preserved: nil, PreservedByModel: nil})

		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		wantHost(t, assignment, err, escrowA, hostB, 1)
		if burns := test.observer.burns(); len(burns) != 0 {
			t.Fatalf("ghost burns = %v, want none: an unloaded set must not ghost the whole group", burns)
		}
	})

	t.Run("a loaded but empty set preserves nobody", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{})
		test.snapshots.set(chain.PhaseSnapshot{Preserved: []string{}})

		_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		if !errors.Is(err, ErrHostsBusy) {
			t.Fatalf("err = %v, want ErrHostsBusy", err)
		}
	})

	t.Run("the per-model set wins over the global one", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{})
		test.snapshots.set(chain.PhaseSnapshot{
			Preserved:        []string{hostA, hostB},
			PreservedByModel: map[string][]string{modelA: {hostA}},
		})

		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		wantHost(t, assignment, err, escrowA, hostA, 2)
		if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostPoC.reason() {
			t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostPoC.reason())
		}
	})

	t.Run("another model's set does not shadow the global one", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{})
		test.snapshots.set(chain.PhaseSnapshot{
			Preserved:        []string{hostA},
			PreservedByModel: map[string][]string{"model-other": {hostA, hostB}},
		})

		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		wantHost(t, assignment, err, escrowA, hostA, 2)
	})
}

// Test flow:
//  1. Build a scheduler harness with two escrows and pick concurrently 8 times pinned to each.
//  2. Assert no picks failed.
//  3. Assert each escrow served exactly 8 requests.
//  4. Assert exactly one dispatcher is live per escrow.
//  5. Assert each escrow's session advanced and committed exactly 8 times.
func TestPickRunsEscrowsOnIndependentDispatchers(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{escrows: []string{escrowA, escrowB}})
	const perEscrow = 8

	var group sync.WaitGroup
	results := make(chan Assignment, 2*perEscrow)
	failures := make(chan error, 2*perEscrow)
	for _, escrowID := range []string{escrowA, escrowB} {
		for range perEscrow {
			group.Go(func() {
				assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, Escrow: escrowID})
				if err != nil {
					failures <- err
					return
				}
				results <- assignment
			})
		}
	}
	group.Wait()
	close(results)
	close(failures)

	if err := <-failures; err != nil {
		t.Fatalf("Pick: %v", err)
	}
	served := map[string]int{}
	for assignment := range results {
		served[assignment.Escrow]++
	}
	if served[escrowA] != perEscrow || served[escrowB] != perEscrow {
		t.Fatalf("served = %v, want %d per escrow", served, perEscrow)
	}
	if live := test.liveDispatchers(); len(live) != 2 {
		t.Fatalf("live dispatchers = %d, want one per escrow", len(live))
	}
	for _, escrowID := range []string{escrowA, escrowB} {
		if advances, _, commits := test.session(t, escrowID).report(); advances != perEscrow || len(commits) != perEscrow {
			t.Fatalf("escrow %q: advances=%d commits=%d, want %d each", escrowID, advances, len(commits), perEscrow)
		}
	}
}

// Test flow:
//  1. Build a scheduler harness with a submit buffer of 1 and a gated session, then start one `Pick` and wait until it reaches the session.
//  2. Start a second `Pick` and wait until the submit queue fills to depth 1.
//  3. Pick a third time.
//  4. Assert the third pick fails with `ErrEscrowBusy`.
//  5. Open the gate and let the first two picks finish.
func TestPickSurfacesBackPressureWhenTheQueueIsFull(t *testing.T) {
	gate := make(chan struct{})
	test := newSchedulerHarness(t, schedulerConfig{submitBuffer: 1, gate: gate})
	session := test.session(t, escrowA)

	var inFlight sync.WaitGroup
	inFlight.Go(func() {
		if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
			t.Errorf("Pick: %v", err)
		}
	})
	select {
	case <-session.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the dispatcher never reached the session")
	}

	inFlight.Go(func() {
		if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
			t.Errorf("Pick: %v", err)
		}
	})
	eventually(t, "the submit queue to fill", func() bool { return test.queueDepth(escrowA) == 1 })

	_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if !errors.Is(err, ErrEscrowBusy) {
		t.Fatalf("err = %v, want ErrEscrowBusy", err)
	}
	close(gate)
	inFlight.Wait()
}

// Test flow:
//  1. Claim `escrowA`'s dispatcher directly.
//  2. Attempt to retire it and assert the retirement is refused while it is still claimed.
//  3. Release the claim and attempt to retire it again.
//  4. Assert the retirement now succeeds and no dispatcher is left live.
func TestClaimedDispatcherOutlivesTheIdleReaper(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})
	escrow := test.escrow(t, escrowA)

	claimed, err := test.scheduler.dispatcherFor(escrow)
	if err != nil {
		t.Fatalf("dispatcherFor: %v", err)
	}

	if test.scheduler.retire(claimed) {
		t.Fatal("retired a dispatcher a caller had already claimed")
	}
	claimed.pendingSubmits.Add(-1)
	if !test.scheduler.retire(claimed) {
		t.Fatal("refused to retire an unclaimed idle dispatcher")
	}

	if live := test.liveDispatchers(); len(live) != 0 {
		t.Fatalf("live dispatchers = %d, want the retired one gone", len(live))
	}
	test.scheduler.Stop()
}

// Test flow:
//  1. Start a `Pick` in a goroutine for a request excluding the host its nonce would bind, and wait for its hold timer to arm.
//  2. Attempt to retire the escrow's dispatcher and assert it is refused while the waiting `Pick` still holds it.
//  3. Cancel the context and assert `Pick` returns `context.Canceled`.
//  4. Attempt to retire the dispatcher again and assert it now succeeds.
//  5. Assert the escrow was announced retired.
func TestAWaitingPickKeepsItsDispatcherClaimed(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{matchWaitMS: 200})
	ctx, cancel := context.WithCancel(t.Context())

	picked := make(chan error, 1)
	go func() {
		_, err := test.scheduler.Pick(ctx, RequestProfile{Model: modelA, Exclude: []string{hostB}})
		picked <- err
	}()
	test.clock.awaitArmed(t)
	waiting := test.liveDispatchers()[escrowA]

	if test.scheduler.retire(waiting) {
		t.Fatal("retired the dispatcher a waiting Pick still holds")
	}

	cancel()
	select {
	case err := <-picked:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Pick err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Pick did not return after its context was cancelled")
	}
	if !test.scheduler.retire(waiting) {
		t.Fatal("refused to retire the dispatcher after the Pick that claimed it returned")
	}
	if retired := test.observer.retiredEscrows(); !slices.Equal(retired, []string{escrowA}) {
		t.Fatalf("escrows announced as retired = %v, want %v", retired, []string{escrowA})
	}
}

// Test flow:
//  1. Claim `escrowA`'s dispatcher, release the claim, and mark it stopped.
//  2. Pick for a request.
//  3. Assert the assignment lands on `hostB` at nonce 1.
//  4. Assert the live registry no longer holds the stopped dispatcher.
func TestPickReplacesAStoppedDispatcher(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})

	stale, err := test.scheduler.dispatcherFor(test.escrow(t, escrowA))
	if err != nil {
		t.Fatalf("dispatcherFor: %v", err)
	}
	stale.pendingSubmits.Add(-1)
	if !stale.markStopped() {
		t.Fatal("markStopped refused an idle dispatcher")
	}

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, assignment, err, escrowA, hostB, 1)
	if live := test.liveDispatchers(); live[escrowA] == stale {
		t.Fatal("Pick submitted to the stopped dispatcher still in the registry")
	}
	test.scheduler.Stop()
}

// Test flow:
//  1. Pick for a request and assert it succeeds.
//  2. Stop the scheduler twice in a row.
//  3. Pick again and assert the error is `ErrDispatcherStopped`.
func TestSchedulerStopIsIdempotent(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err != nil {
		t.Fatalf("Pick: %v", err)
	}

	test.scheduler.Stop()
	test.scheduler.Stop()

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); !errors.Is(err, ErrDispatcherStopped) {
		t.Fatalf("err = %v, want ErrDispatcherStopped", err)
	}
}

// Test flow:
//  1. Pick a first request and assert it lands on `hostB` at nonce 1.
//  2. Assert the idle timer armed for `idleDispatcherGrace`, then fire it and wait for the dispatcher to retire.
//  3. Assert the escrow was announced retired.
//  4. Pick again and assert the recreated dispatcher lands on `hostA` at nonce 2, with one dispatcher live.
func TestSchedulerReapsAnIdleDispatcherAndRecreatesItOnDemand(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})

	first, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})
	wantHost(t, first, err, escrowA, hostB, 1)

	if delay := test.clock.awaitArmed(t); delay != idleDispatcherGrace {
		t.Fatalf("idle delay = %v, want %v", delay, idleDispatcherGrace)
	}
	test.clock.fireTimer()
	eventually(t, "the idle dispatcher to retire", func() bool { return len(test.liveDispatchers()) == 0 })

	if retired := test.observer.retiredEscrows(); !slices.Equal(retired, []string{escrowA}) {
		t.Fatalf("escrows announced as retired = %v, want %v: an observer never told keeps the escrow's series forever", retired, []string{escrowA})
	}

	recreated, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	wantHost(t, recreated, err, escrowA, hostA, 2)
	if live := test.liveDispatchers(); len(live) != 1 {
		t.Fatalf("live dispatchers = %d, want the escrow's dispatcher back", len(live))
	}
	test.scheduler.Stop()
}

// Test flow:
//  1. Claim `escrowA`'s dispatcher, release the claim, and block a host on that escrow.
//  2. Retire the idle dispatcher and assert the retirement succeeds.
//  3. Assert the host is still reported state-blocked after the dispatcher went idle.
//  4. Claim a fresh dispatcher for the same escrow and assert the host stays blocked on it too.
func TestReapingADispatcherKeepsTheEscrowBlockedForADivergentHost(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})
	escrow := test.escrow(t, escrowA)
	claimed, err := test.scheduler.dispatcherFor(escrow)
	if err != nil {
		t.Fatalf("dispatcherFor: %v", err)
	}
	claimed.pendingSubmits.Add(-1)
	test.scheduler.BlockHost(escrowA, "host-a")

	if !test.scheduler.retire(claimed) {
		t.Fatal("refused to retire an unclaimed idle dispatcher")
	}

	if !test.scheduler.stateBlocked(escrowA)("host-a") {
		t.Error("the host was unblocked by the dispatcher going idle")
	}
	revived, err := test.scheduler.dispatcherFor(escrow)
	if err != nil {
		t.Fatalf("dispatcherFor after retirement: %v", err)
	}
	revived.pendingSubmits.Add(-1)
	if !test.scheduler.stateBlocked(escrowA)("host-a") {
		t.Error("the dispatcher recreated for the same escrow serves a host its chain cannot follow")
	}
}

// Test flow:
//  1. Claim `escrowA`'s dispatcher, release the claim, and spend a host's divergence replay credit.
//  2. Assert the first divergence did spend the replay.
//  3. Retire the idle dispatcher and assert the retirement succeeds.
//  4. Assert a later divergence on the same host is not given a second replay just for the dispatcher having gone idle.
func TestReapingADispatcherDoesNotHandBackTheSpentReplay(t *testing.T) {
	leakcheck.VerifyNone(t)
	test := newSchedulerHarness(t, schedulerConfig{})
	escrow := test.escrow(t, escrowA)
	claimed, err := test.scheduler.dispatcherFor(escrow)
	if err != nil {
		t.Fatalf("dispatcherFor: %v", err)
	}
	claimed.pendingSubmits.Add(-1)
	diverged := time.Unix(1_700_000_000, 0)
	if !test.scheduler.HostDiverged(escrowA, "host-a", diverged) {
		t.Fatal("the first divergence did not spend the replay")
	}

	if !test.scheduler.retire(claimed) {
		t.Fatal("refused to retire an unclaimed idle dispatcher")
	}

	if test.scheduler.HostDiverged(escrowA, "host-a", diverged.Add(time.Hour)) {
		t.Error("the host was given a second replay for having been idle")
	}
}

// Test flow:
//  1. Build a scheduler whose session cancels the caller's context right after deciding the nonce, and pick under that context.
//  2. Assert the pick returns the committed assignment on `hostB` at nonce 1 rather than the context's error.
//  3. Assert no nonce was burned and the assignment still holds its host slot and escrow hold.
func TestAPickCancelledAfterTheCommitReturnsTheAssignment(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	test.session(t, escrowA).afterDecide = func(HostBinding) { cancel() }

	assignment, err := test.scheduler.Pick(ctx, RequestProfile{RequestID: "request-gone", Model: modelA})
	if err != nil {
		t.Fatalf("Pick after the commit = %v, want the committed assignment", err)
	}
	if assignment.Host != hostB || assignment.Nonce.Nonce() != 1 {
		t.Fatalf("assignment = %s at nonce %d, want %s at nonce 1", assignment.Host, assignment.Nonce.Nonce(), hostB)
	}
	if burns := test.observer.burns(); len(burns) != 0 {
		t.Fatalf("ghost burns = %v, want none for a committed nonce handed to its caller", burns)
	}
	if held, _ := test.limiter.slots(); held != 1 {
		t.Fatalf("slots held = %d, want the assignment's own", held)
	}
	if outstanding := test.holds.outstanding(); outstanding != 1 {
		t.Fatalf("escrow holds out = %d, want the assignment's own", outstanding)
	}
	assignment.ReleaseHostSlot()
	assignment.ReleaseEscrow()
	test.scheduler.Stop()
}

func (h *schedulerHarness) markReserve(escrowID string) {
	for index, candidate := range h.escrows.byModel[modelA] {
		if candidate.ID == escrowID {
			h.escrows.byModel[modelA][index].IsReserve = true
		}
	}
}

// Test flow:
//  1. Build a scheduler whose only escrow is a reserve, and pick for a request.
//  2. Assert the pick is served on the reserve and the take is reported once, after the dispatcher handed out the nonce.
func TestAServedPickOnAReserveReportsItTaken(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})
	test.markReserve(escrowA)

	assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

	if err != nil || assignment.Escrow != escrowA {
		t.Fatalf("Pick = %+v, %v; want a nonce on the reserve", assignment, err)
	}
	if taken := test.reserves.recorded(); !slices.Equal(taken, []string{escrowA}) {
		t.Fatalf("reserves taken = %v, want [%s]", taken, escrowA)
	}
}

// Test flow:
//  1. Build a scheduler whose only escrow is a reserve whose session refuses to advance.
//  2. Pick for a request and assert it fails.
//  3. Assert no take was reported: a reserve that served nothing stays a reserve, so no second one is funded for it.
func TestAFailedPickOnAReserveDoesNotTakeIt(t *testing.T) {
	test := newSchedulerHarness(t, schedulerConfig{})
	test.markReserve(escrowA)
	test.session(t, escrowA).failWith = errors.New("session refused")

	if _, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA}); err == nil {
		t.Fatal("Pick succeeded on a session that refuses every advance")
	}
	if taken := test.reserves.recorded(); len(taken) != 0 {
		t.Fatalf("reserves taken = %v, want none", taken)
	}
}

// Test flow:
//  1. For each table case, build a scheduler whose escrow holds plenty, its model's context length named by an operator pin or by the chain's --max-model-len, with the session's advance held at a gate.
//  2. Pick for a small request; once the advance is entered, drop the balance between one capped answer and one full-context request and make the session refuse it as unaffordable.
//  3. Release the gate and assert the pick fails and the escrow is reported exhausted, priced on the model's context rather than on one capped answer.
func TestADispatcherRetiresAnEscrowThatCanNoLongerAffordAFullContextRequest(t *testing.T) {
	testCases := []struct {
		name   string
		pinned bool
	}{
		{name: "operator_pin", pinned: true},
		{name: "chain_length"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			gate := make(chan struct{})
			test := newSchedulerHarness(t, schedulerConfig{gate: gate})
			if testCase.pinned {
				pinContextLength(test.scheduler)
			} else {
				test.snapshots.mu.Lock()
				test.snapshots.snapshot.Models = map[string]chain.ModelParams{modelA: {MaxModelLen: fullContextTokens}}
				test.snapshots.mu.Unlock()
			}
			session := test.session(t, escrowA)
			picked := make(chan error, 1)
			go func() {
				_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA, InputBytes: 10, OutputTokens: 16})
				picked <- err
			}()
			<-session.entered
			session.mu.Lock()
			session.balance = 100_000
			session.failWith = types.ErrInsufficientBalance
			session.mu.Unlock()

			close(gate)

			if err := <-picked; !errors.Is(err, types.ErrInsufficientBalance) {
				t.Fatalf("Pick = %v, want the refused advance's error", err)
			}
			if reported := test.exhausted.all(); !slices.Equal(reported, []string{escrowA + ":" + string(ExhaustionInsufficientBalance)}) {
				t.Fatalf("reported = %v, want %s exhausted for an unaffordable balance", reported, escrowA)
			}
		})
	}
}

// Test flow:
//  1. Spend host B's chat budget and pick for a request.
//  2. Assert the pick lands on host A, burning the nonce bound to host B as a full window.
//  3. Spend both hosts' chat budgets and assert the pick fails with `ErrHostsBusy`, so the client is answered at once instead of a request waiting on a host's budget.
func TestPickTreatsAHostWithNoChatBudgetAsAFullWindow(t *testing.T) {
	t.Run("the host with budget serves", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{budgetSpent: []string{hostB}})

		assignment, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		wantHost(t, assignment, err, escrowA, hostA, 2)
		if burns := test.observer.burns(); len(burns) != 1 || burns[0] != ghostWindowFull.reason() {
			t.Fatalf("ghost burns = %v, want exactly one %q", burns, ghostWindowFull.reason())
		}
	})

	t.Run("no host has budget", func(t *testing.T) {
		test := newSchedulerHarness(t, schedulerConfig{budgetSpent: []string{hostA, hostB}})

		_, err := test.scheduler.Pick(context.Background(), RequestProfile{Model: modelA})

		if !errors.Is(err, ErrHostsBusy) {
			t.Fatalf("err = %v, want ErrHostsBusy", err)
		}
	})
}
