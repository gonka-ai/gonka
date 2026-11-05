package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
	"devshard/user"
)

// stubPicker answers picks from a queue, parking any it cannot answer until released.
type stubPicker struct {
	mu       sync.Mutex
	queue    []scheduler.Assignment
	blocked  [][2]string
	diverged [][2]string
	absent   [][2]string
	replays  int
	profiles []scheduler.RequestProfile
	hold     chan struct{}
	parked   chan struct{}
}

func (p *stubPicker) Pick(ctx context.Context, profile scheduler.RequestProfile) (scheduler.Assignment, error) {
	p.mu.Lock()
	p.profiles = append(p.profiles, profile)
	if len(p.queue) > 0 {
		next := p.queue[0]
		p.queue = p.queue[1:]
		p.mu.Unlock()
		return next, nil
	}
	hold, parked := p.hold, p.parked
	p.mu.Unlock()

	if hold == nil {
		return scheduler.Assignment{}, scheduler.ErrNoAvailableHost
	}
	if parked != nil {
		parked <- struct{}{}
	}
	select {
	case <-hold:
		return p.next()
	case <-ctx.Done():
		return scheduler.Assignment{}, ctx.Err()
	}
}

// next answers a released pick with whatever was queued while it was parked.
func (p *stubPicker) next() (scheduler.Assignment, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.queue) == 0 {
		return scheduler.Assignment{}, scheduler.ErrNoAvailableHost
	}
	next := p.queue[0]
	p.queue = p.queue[1:]
	return next, nil
}

// Mirrors the scheduler's one-replay-per-participant-per-escrow policy.
func (p *stubPicker) HostDiverged(escrowID, participant string, _ time.Time) bool {
	p.mu.Lock()
	seen := 0
	for _, entry := range p.diverged {
		if entry == [2]string{escrowID, participant} {
			seen++
		}
	}
	p.diverged = append(p.diverged, [2]string{escrowID, participant})
	p.mu.Unlock()
	if seen == 0 {
		p.replays++
		return true
	}
	p.blockHost(escrowID, participant)
	return false
}

func (p *stubPicker) HostServed(string, string, time.Time) {}

func (p *stubPicker) HostAbsent(escrowID, participant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.absent = append(p.absent, [2]string{escrowID, participant})
}

func (p *stubPicker) blockHost(escrowID, participant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.blocked = append(p.blocked, [2]string{escrowID, participant})
}

// hostScript is one nonce's scripted host: arrive/release hold an attempt before it sends, streaming/resume hold it mid-stream, feed writes each chunk a test sends until it is closed, and flush flushes each chunk as it is written.
type hostScript struct {
	arrive      chan<- uint64
	release     <-chan struct{}
	streaming   chan<- uint64
	resume      <-chan struct{}
	feed        <-chan string
	receipt     bool
	chunks      []string
	flush       bool
	hold        bool
	deaf        <-chan struct{}
	confirmed   bool
	finished    bool
	finishLater bool
	err         error
	catchUpHeld bool
	catchUp     *catchUpScript
}

// catchUpScript holds an attempt in its catch-up: it reports the host unreachable once fault closes and fails once fail closes.
type catchUpScript struct {
	arrive chan<- uint64
	fault  <-chan struct{}
	fail   <-chan struct{}
}

// fed is the script's feed, or a closed channel when it has none, so a range over it ends at once.
func (s *hostScript) fed() <-chan string {
	if s.feed != nil {
		return s.feed
	}
	closed := make(chan string)
	close(closed)
	return closed
}

type scriptedTarget struct {
	mu      sync.Mutex
	scripts map[uint64]*hostScript
	hosts   int
	labels  map[int]string
	rewound []int
}

func (t *scriptedTarget) script(nonce uint64) *hostScript {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.scripts[nonce]
}

func (t *scriptedTarget) CatchUp(ctx context.Context, nonce scheduler.Prepared, onFault func()) error {
	script := t.script(nonce.Nonce())
	switch {
	case script == nil:
	case script.catchUpHeld:
		<-ctx.Done()
		return ctx.Err()
	case script.catchUp != nil:
		return script.catchUp.run(ctx, nonce.Nonce(), onFault)
	}
	return nil
}

func (s *catchUpScript) run(ctx context.Context, nonce uint64, onFault func()) error {
	s.arrive <- nonce
	select {
	case <-s.fault:
		onFault()
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-s.fail:
		return errors.New("catch-up chunk: http2: client connection lost")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *scriptedTarget) Send(ctx context.Context, nonce scheduler.Prepared, stream io.Writer, onReceipt func()) (Response, error) {
	script := t.script(nonce.Nonce())
	if script == nil {
		return nil, errors.New("unscripted nonce")
	}
	if script.arrive != nil {
		script.arrive <- nonce.Nonce()
	}
	if script.release != nil {
		select {
		case <-script.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if script.receipt {
		onReceipt()
	}
	for index, chunk := range script.chunks {
		if _, err := stream.Write([]byte(chunk)); err != nil {
			return nil, err
		}
		if script.flush {
			if flusher, ok := stream.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if index > 0 || script.streaming == nil {
			continue
		}
		script.streaming <- nonce.Nonce()
		select {
		case <-script.resume:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	for chunk := range script.fed() {
		if _, err := stream.Write([]byte(chunk)); err != nil {
			return nil, err
		}
	}
	if script.hold {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if script.deaf != nil {
		<-script.deaf
		return nil, ctx.Err()
	}
	return fakeResponse{confirmed: script.confirmed}, script.err
}

func (t *scriptedTarget) HostCount() int { return t.hosts }

func (t *scriptedTarget) HostLabel(hostIdx int) string {
	if label, ok := t.labels[hostIdx]; ok {
		return label
	}
	return fmt.Sprintf("host-%d", hostIdx)
}

func (t *scriptedTarget) RewindHostCatchUp(hostIdx int, _ string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rewound = append(t.rewound, hostIdx)
	return true
}

func (t *scriptedTarget) NonceFinished(nonce uint64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	script := t.scripts[nonce]
	if script == nil {
		return false
	}
	finished := script.finished
	script.finished = finished || script.finishLater
	return finished
}

func (t *scriptedTarget) finish(nonce uint64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.scripts[nonce].finished = true
}

func (t *scriptedTarget) Target(string) (DispatchTarget, bool) { return t, true }

// simTargets is the boundary main wires, holding a resolved target's escrow until the race's vote is posted.
type simTargets struct {
	*scriptedTarget
	panicking atomic.Bool
	holds     atomic.Int64
	releases  atomic.Int64
}

func (t *simTargets) Target(string) (DispatchTarget, func(), bool) {
	t.holds.Add(1)
	release := func() { t.releases.Add(1) }
	if t.panicking.Load() {
		return unusableTarget{DispatchTarget: t.scriptedTarget}, release, true
	}
	return t.scriptedTarget, release, true
}

func (t *simTargets) held() (holds, releases int64) { return t.holds.Load(), t.releases.Load() }

// unusableTarget fails the race after the hold has been taken, on the goroutine that called Run.
type unusableTarget struct{ DispatchTarget }

func (unusableTarget) HostCount() int { panic("dispatch target is unusable") }

// markerClassifier reads its verdict out of the chunk's own marker text.
type markerClassifier struct{}

func (markerClassifier) Classify(chunk []byte) chunkFacts {
	switch {
	case bytes.Contains(chunk, []byte("content")):
		return chunkFacts{Content: true, ContentSource: "delta.content"}
	case bytes.Contains(chunk, []byte("too-long")):
		return chunkFacts{
			ErrorSource:       "error.BadRequestError",
			ErrorType:         "BadRequestError",
			ErrorMessage:      vllmContextTotalMessage,
			CapabilityRefused: true,
		}
	case bytes.Contains(chunk, []byte("malformed-request")):
		return chunkFacts{
			Error:        true,
			ErrorSource:  "error.BadRequestError",
			ErrorCode:    "400",
			ErrorType:    "BadRequestError",
			ErrorMessage: malformedToolCallMessage,
			ErrorPayload: malformedToolCallRejection,
		}
	}
	return chunkFacts{}
}

func (markerClassifier) Flush() chunkFacts { return chunkFacts{} }
func (markerClassifier) Release()          {}

// claimWatch reports each content chunk just before it is handed to its sink.
type claimWatch struct {
	streamClassifier
	claiming chan<- struct{}
}

func (w claimWatch) Classify(chunk []byte) chunkFacts {
	facts := w.streamClassifier.Classify(chunk)
	if facts.Content {
		select {
		case w.claiming <- struct{}{}:
		default:
		}
	}
	return facts
}

type stubPerf struct {
	mu           sync.Mutex
	acquired     []string
	released     []string
	ejected      map[string]bool
	degraded     map[string]bool
	toolCalls    []string
	versionCalls []string
	limits       []contextLimitCall
	observed     map[string]time.Duration
	pressure     perf.Pressure

	contextLimitRecorded chan struct{}
	overruns             []string
}

func (p *stubPerf) RecordOutputOverrun(participant, _ string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.overruns = append(p.overruns, participant)
}

func (p *stubPerf) outputOverruns() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.overruns...)
}

func (p *stubPerf) FirstContentP75(participant, _ string) (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	observed, known := p.observed[participant]
	return observed, known
}

func (p *stubPerf) Pressure(participant, model string) perf.Pressure {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.pressure
}

func (p *stubPerf) Acquire(participant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.acquired = append(p.acquired, participant)
}

func (p *stubPerf) Release(participant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.released = append(p.released, participant)
}

func (p *stubPerf) Ejected(participant, _ string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ejected[participant]
}

func (p *stubPerf) Degraded(participant, _ string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.degraded[participant]
}

func (p *stubPerf) RecordContextLimit(participant, model string, maxTokens uint64) {
	p.mu.Lock()
	p.limits = append(p.limits, contextLimitCall{participant: participant, maxTokens: maxTokens})
	recorded := p.contextLimitRecorded
	p.mu.Unlock()
	if recorded != nil {
		recorded <- struct{}{}
	}
}

func (p *stubPerf) RecordVersionUnsupported(participant string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.versionCalls = append(p.versionCalls, participant)
}

func (p *stubPerf) RecordToolUnsupported(participant, model string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.toolCalls = append(p.toolCalls, participant)
}

func (p *stubPerf) counts() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.acquired), len(p.released)
}

type stubSnapshots struct{ snapshot chain.PhaseSnapshot }

func (s stubSnapshots) Snapshot() chain.PhaseSnapshot { return s.snapshot }

type stubCrown struct {
	mu       sync.Mutex
	denied   map[string]bool
	observed map[string]bool
}

func (g *stubCrown) Denied(participant, _ string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.denied[participant]
}

func (g *stubCrown) Observe(participant, _ string, contentless bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.observed == nil {
		g.observed = map[string]bool{}
	}
	g.observed[participant] = contentless
}

// slotLedger stands in for the host windows the scheduler drew each attempt's slot from.
type slotLedger struct {
	releases chan string
	verdicts chan windowMove
}

func newSlotLedger() *slotLedger {
	return &slotLedger{releases: make(chan string, 64), verdicts: make(chan windowMove, 64)}
}

// hostSlot is the release the scheduler hands an assignment, reporting the participant it gives back.
func (l *slotLedger) hostSlot(participant string) func() {
	return func() { l.releases <- participant }
}

func (l *slotLedger) OnResult(result limits.Result) {
	l.verdicts <- windowMove{participant: result.Participant, verdict: result.Verdict}
}

func (l *slotLedger) CountRefusalIfIdle(string, string) {}

type recordingSink struct {
	mu      sync.Mutex
	writes  [][]byte
	flushes int
}

func (s *recordingSink) Write(chunk []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, bytes.Clone(chunk))
	return len(chunk), nil
}

func (s *recordingSink) Flush() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flushes++
}

func (s *recordingSink) flushed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.flushes
}

func (s *recordingSink) forwarded() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return bytes.Join(s.writes, nil)
}

type raceFixture struct {
	deps     raceDeps
	request  raceRequest
	picker   *stubPicker
	target   *scriptedTarget
	perf     *stubPerf
	crown    *stubCrown
	limiter  *slotLedger
	clock    *virtualTime
	client   *recordingSink
	reported chan RaceOutcome
}

func newRaceFixture(policy EscalationPolicy, hosts int) *raceFixture {
	fixture := &raceFixture{
		picker:   &stubPicker{},
		target:   &scriptedTarget{scripts: map[uint64]*hostScript{}, hosts: hosts, labels: map[int]string{}},
		perf:     &stubPerf{ejected: map[string]bool{}, degraded: map[string]bool{}},
		crown:    &stubCrown{denied: map[string]bool{}},
		limiter:  newSlotLedger(),
		clock:    newVirtualTime(),
		client:   &recordingSink{},
		reported: make(chan RaceOutcome, 4),
	}
	fixture.deps = raceDeps{
		PrefixBudget: newPrefixBudget(),
		Picker:       fixture.picker,
		Targets:      fixture.target,
		Limiter:      fixture.limiter,
		Perf:         fixture.perf,
		Crown:        fixture.crown,
		Snapshots:    stubSnapshots{},
		Policy:       policy,
		Modes:        config.Modes{},
		Classify:     func(string) streamClassifier { return markerClassifier{} },
		Now:          fixture.clock.Now,
		Timer:        func() raceTimer { return fixture.clock },
		Report:       func(outcome RaceOutcome) { fixture.reported <- outcome },
	}
	fixture.request = raceRequest{
		Request: Request{
			RequestID:            "request-1",
			Model:                testModel,
			InputTokens:          1_000,
			InputTokensFromBytes: 1_000,
		},
		Client: fixture.client,
	}
	return fixture
}

func (f *raceFixture) host(nonce uint64, hostIdx int, participant string, script *hostScript) {
	f.target.mu.Lock()
	f.target.scripts[nonce] = script
	f.target.labels[hostIdx] = "label-" + participant
	f.target.mu.Unlock()
	f.picker.mu.Lock()
	f.picker.queue = append(f.picker.queue, scheduler.Assignment{
		Escrow:   "escrow-1",
		Host:     participant,
		Nonce:    fakePrepared{nonce: nonce, hostIdx: hostIdx},
		HostSlot: f.limiter.hostSlot(participant),
	})
	f.picker.mu.Unlock()
}

func (f *raceFixture) run(ctx context.Context) (RaceOutcome, error) {
	return runRace(ctx, f.deps, f.request)
}

// racePolicy escalates the moment an attempt is dispatched without a receipt.
func racePolicy(maxAttempts int) EscalationPolicy {
	return EscalationPolicy{
		ReceiptTimeout:        0,
		FirstTokenFloor:       time.Hour,
		InterChunkStall:       time.Hour,
		LoserGrace:            10 * time.Minute,
		MaxAttemptsPerRequest: maxAttempts,
	}
}

func settledPolicy() EscalationPolicy {
	return EscalationPolicy{
		ReceiptTimeout:        time.Hour,
		FirstTokenFloor:       time.Hour,
		InterChunkStall:       time.Hour,
		LoserGrace:            10 * time.Minute,
		MaxAttemptsPerRequest: 1,
	}
}

// waitForValue bounds a wait on the race so a regression that blocks it fails the test.
func waitForValue[T any](t *testing.T, values <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-values:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never happened", what)
		var zero T
		return zero
	}
}

func contentChunk(nonce uint64) string {
	return fmt.Sprintf("data: {\"content\":\"%d\"}\n\n", nonce)
}

// Test flow:
//  1. Start a race with two hosts each scripted to receipt and then race to produce content once released.
//  2. Let both attempts arrive, then release them together so their first content chunks contend for the crown.
//  3. Assert exactly one attempt is reported as `TerminalWon` and the race succeeded.
//  4. Assert the client stream carries the winner's content chunk.
//  5. Assert only one outcome is reported.
func TestRunRaceCrownsExactlyOneWinnerUnderConcurrentContent(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	arrive := make(chan uint64, 2)
	release := make(chan struct{})
	for index, nonce := range []uint64{10, 11} {
		fixture.host(nonce, index, fmt.Sprintf("host-%d", index), &hostScript{
			arrive:    arrive,
			release:   release,
			receipt:   true,
			chunks:    []string{"data: role\n\n", contentChunk(nonce)},
			confirmed: true,
			finished:  true,
		})
	}

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()

	<-arrive
	<-arrive
	close(release)

	reported := <-fixture.reported
	<-outcomes

	if len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(reported.Attempts))
	}
	if reported.WinnerNonce != 10 && reported.WinnerNonce != 11 {
		t.Fatalf("winner nonce = %d, want one of the two attempts", reported.WinnerNonce)
	}
	won := 0
	for _, attempt := range reported.Attempts {
		if attempt.Terminal == TerminalWon {
			won++
		}
	}
	if won != 1 {
		t.Fatalf("attempts with TerminalWon = %d, want 1", won)
	}
	if !reported.Succeeded {
		t.Fatal("Succeeded = false, want true")
	}
	forwarded := string(fixture.client.forwarded())
	if !bytes.Contains([]byte(forwarded), []byte(contentChunk(reported.WinnerNonce))) {
		t.Fatalf("client stream %q missing the winner's chunk", forwarded)
	}
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
}

// Test flow:
//  1. Start a race with two hosts each scripted to race to content once released.
//  2. Let both attempts arrive, then release them together.
//  3. Determine the losing nonce from the reported winner.
//  4. Assert the client stream does not contain the losing attempt's content chunk.
func TestRunRaceWithholdsLosingAttemptBytes(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	arrive := make(chan uint64, 2)
	release := make(chan struct{})
	for index, nonce := range []uint64{20, 21} {
		fixture.host(nonce, index, fmt.Sprintf("host-%d", index), &hostScript{
			arrive:    arrive,
			release:   release,
			receipt:   true,
			chunks:    []string{"data: role\n\n", contentChunk(nonce)},
			confirmed: true,
			finished:  true,
		})
	}

	go func() {
		if _, err := fixture.run(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	<-arrive
	<-arrive
	close(release)
	reported := <-fixture.reported

	loser := uint64(20)
	if reported.WinnerNonce == 20 {
		loser = 21
	}
	forwarded := fixture.client.forwarded()
	if bytes.Contains(forwarded, []byte(contentChunk(loser))) {
		t.Fatalf("client stream %q carries the losing attempt's bytes", forwarded)
	}
}

// Test flow:
//  1. Start a race with a single host whose attempt fails immediately with a dial error.
//  2. Run the race to completion.
//  3. Assert `runRace` returns no error but the outcome and the report both mark the race unsuccessful.
//  4. Assert the one attempt is reported as `TerminalDialFailure` with no winner nonce.
//  5. Assert the perf tracker records one `Acquire` and one `Release`.
func TestRunRaceReportsOneOutcomeWhenEveryAttemptFails(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(30, 0, "host-0", &hostScript{receipt: true, err: errors.New("dial failed")})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if outcome.Succeeded || reported.Succeeded {
		t.Fatal("Succeeded = true, want false")
	}
	if len(reported.Attempts) != 1 || reported.Attempts[0].Terminal != TerminalDialFailure {
		t.Fatalf("attempts = %+v, want one dial failure", reported.Attempts)
	}
	if reported.WinnerNonce != 0 {
		t.Fatalf("winner nonce = %d, want 0", reported.WinnerNonce)
	}
	acquired, released := fixture.perf.counts()
	if acquired != 1 || released != 1 {
		t.Fatalf("perf bracket = %d/%d, want 1/1", acquired, released)
	}
}

// Test flow:
//  1. Start a race with two hosts: one scripted to send only the role chunk and hang until released, the other scripted to win with content once released.
//  2. Let both attempts arrive, release the winner, and assert the race returns nonce 41 to the client.
//  3. Release the still-pending loser and read the coordinator's final report.
//  4. Assert the report carries both attempts and only one outcome was reported.
//  5. Assert the perf tracker records two acquires and two releases.
func TestRunRaceReportsOneOutcomeAfterHandingOffPendingLosers(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	arrive := make(chan uint64, 2)
	winnerRelease := make(chan struct{})
	loserRelease := make(chan struct{})

	fixture.host(40, 0, "host-0", &hostScript{
		arrive:  arrive,
		release: loserRelease,
		receipt: true,
		chunks:  []string{"data: role\n\n"},
	})
	fixture.host(41, 1, "host-1", &hostScript{
		arrive:    arrive,
		release:   winnerRelease,
		receipt:   true,
		chunks:    []string{contentChunk(41)},
		confirmed: true,
		finished:  true,
	})

	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()

	<-arrive
	<-arrive
	close(winnerRelease)
	served := <-returned
	if served.WinnerNonce != 41 {
		t.Fatalf("winner nonce = %d, want 41", served.WinnerNonce)
	}

	close(loserRelease)
	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 2 {
		t.Fatalf("reported attempts = %d, want both", len(reported.Attempts))
	}
	acquired, released := fixture.perf.counts()
	if acquired != 2 || released != 2 {
		t.Fatalf("perf bracket = %d/%d, want 2/2", acquired, released)
	}
}

// Test flow:
//  1. Start a race with two hosts both scripted to hold their attempt until the context ends, a fast drain timeout, and a clock that advances a second on every read.
//  2. Let both attempts arrive, then cancel the client context.
//  3. Assert `runRace` returns `context.Canceled`.
//  4. Assert the report carries both attempts terminalled `TerminalClientCancelled` and only one outcome was reported.
//  5. Assert the perf tracker records two acquires and two releases.
func TestRunRaceReportsOneOutcomeWhenTerminatedWithEveryAttemptPending(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 4)
	fixture.deps.DrainTimeout = time.Millisecond
	fixture.clock.step = time.Second
	arrive := make(chan uint64, 2)
	for index, nonce := range []uint64{50, 51} {
		fixture.host(nonce, index, fmt.Sprintf("host-%d", index), &hostScript{
			arrive: arrive,
			hold:   true,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if _, err := fixture.run(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("runRace error = %v, want context.Canceled", err)
		}
	}()

	<-arrive
	<-arrive
	cancel()

	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 2 {
		t.Fatalf("reported attempts = %d, want both", len(reported.Attempts))
	}
	for _, attempt := range reported.Attempts {
		if attempt.Terminal != TerminalClientCancelled {
			t.Fatalf("attempt %d terminal = %v, want client cancelled", attempt.Nonce, attempt.Terminal)
		}
	}
	acquired, released := fixture.perf.counts()
	if acquired != 2 || released != 2 {
		t.Fatalf("perf bracket = %d/%d, want 2/2", acquired, released)
	}
}

// Test flow:
//  1. Start a race with a single host that receipts and then holds forever, using a clock that jumps 30 minutes on every read to reach the hard timeout without sleeping.
//  2. Run the race to completion.
//  3. Assert `runRace` returns no error and the report carries exactly one attempt.
//  4. Assert that attempt's terminal is `TerminalHardTimeout`.
func TestRunRaceReportsOneOutcomeOnHardTimeoutWithPendingAttempts(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(60, 0, "host-0", &hostScript{receipt: true, hold: true})
	fixture.clock.step = 30 * time.Minute

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 1 || len(outcome.Attempts) != 1 {
		t.Fatalf("attempts = %d, want 1", len(reported.Attempts))
	}
	if reported.Attempts[0].Terminal != TerminalHardTimeout {
		t.Fatalf("terminal = %v, want hard_timeout", reported.Attempts[0].Terminal)
	}
}

// Test flow:
//  1. Start a race whose only host is still being caught up by the session, a catch-up that never ends.
//  2. Run the race with a clock that passes the hard timeout.
//  3. Assert the race ends on the hard timeout: the backstop runs from the attempt's launch, so a session wait that outlives it cannot hold the client past it.
func TestRunRaceHardTimeoutCoversTheSessionsCatchUp(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(60, 0, "host-0", &hostScript{catchUpHeld: true})
	fixture.clock.step = 30 * time.Minute

	ran := make(chan error, 1)
	go func() {
		_, err := fixture.run(context.Background())
		ran <- err
	}()
	select {
	case <-ran:
	case <-time.After(10 * time.Second):
		t.Fatal("the race still waits on the session's catch-up past the hard timeout")
	}
	reported := <-fixture.reported
	if len(reported.Attempts) != 1 || reported.Attempts[0].Terminal != TerminalHardTimeout {
		t.Fatalf("attempts = %+v, want one ended by the hard timeout", reported.Attempts)
	}
}

// Test flow:
//  1. Start a race with no host queued behind the picker.
//  2. Run the race.
//  3. Assert the error wraps `scheduler.ErrNoAvailableHost`.
//  4. Assert exactly one outcome is reported with no attempts.
func TestRunRaceReturnsPickErrorAndReportsNoAttempts(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)

	_, err := fixture.run(context.Background())
	if !errors.Is(err, scheduler.ErrNoAvailableHost) {
		t.Fatalf("error = %v, want ErrNoAvailableHost", err)
	}
	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 0 {
		t.Fatalf("reported attempts = %+v, want none", reported.Attempts)
	}
}

// Test flow:
//  1. Start a race whose picker parks the primary pick instead of answering it.
//  2. Wait for the pick to reach the scheduler, then cancel the client context.
//  3. Assert the race gives up the pick and returns `context.Canceled`.
//  4. Assert the report has no attempts, no vote in its timeout plan, and a `Lifecycle` recording only that the client left.
func TestRunRaceGivesUpThePrimaryPickWhenTheClientLeavesWhileItWaits(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	hold := make(chan struct{})
	openHold := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(openHold)
	fixture.picker.hold, fixture.picker.parked = hold, make(chan struct{}, 1)
	clientCtx, leave := context.WithCancel(context.Background())
	t.Cleanup(leave)

	returned := make(chan error, 1)
	go func() {
		_, err := fixture.run(clientCtx)
		returned <- err
	}()
	waitForValue(t, fixture.picker.parked, "the primary's pick waiting in the scheduler")
	leave()
	err := waitForValue(t, returned, "the race giving up the primary's pick once the client left")
	openHold()

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runRace error = %v, want the client's own cancellation, neither a scheduler timeout nor a capacity refusal", err)
	}
	reported := <-fixture.reported
	if len(fixture.reported) != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+len(fixture.reported))
	}
	if len(reported.Attempts) != 0 {
		t.Fatalf("reported attempts = %+v, want none", reported.Attempts)
	}
	if plan := reported.TimeoutPlan(); len(plan) != 0 {
		t.Fatalf("timeout plan = %+v, want no vote", plan)
	}
	if reported.Lifecycle != (Lifecycle{ClientGone: true}) {
		t.Fatalf("lifecycle = %+v, want only the client's departure, with neither a missing escrow nor an exhausted balance", reported.Lifecycle)
	}
}

// expiredPicker answers every pick the way the scheduler does once the pick's own deadline has passed.
type expiredPicker struct{ *stubPicker }

func (expiredPicker) Pick(context.Context, scheduler.RequestProfile) (scheduler.Assignment, error) {
	return scheduler.Assignment{}, context.DeadlineExceeded
}

// Test flow:
//  1. Start a race whose picker always answers the primary pick with its own deadline exceeded, not a client cancellation.
//  2. Run the race.
//  3. Assert the error wraps `context.DeadlineExceeded`.
//  4. Assert the report has no attempts and does not mark the client as gone.
func TestRunRaceDoesNotReadAPrimaryPickDeadlineAsADeparture(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.Picker = expiredPicker{stubPicker: fixture.picker}

	_, err := fixture.run(context.Background())

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("runRace error = %v, want the pick's own deadline", err)
	}
	reported := <-fixture.reported
	if len(reported.Attempts) != 0 {
		t.Fatalf("reported attempts = %+v, want none", reported.Attempts)
	}
	if reported.Lifecycle.ClientGone {
		t.Fatal("a pick that ran out its own deadline was recorded as the client leaving")
	}
}

// Test flow:
//  1. Start a race with one host queued but a target registry that no longer resolves the escrow.
//  2. Run the race.
//  3. Assert the error wraps `errNoDispatchTarget` and the scheduler slot taken for host-0 is released.
//  4. Assert the report's timeout plan posts one vote for the undispatched nonce 300.
//  5. Settle that vote through `settleEvents` and assert it posts carrying the race's own start time, not a zero time that would land the vote too early to collect.
//  6. Assert the attempt's sample exemption is `ExemptNeverDispatched`.
func TestRunRaceGivesBackTheSlotAndVotesForANonceItCannotDispatch(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(300, 0, "host-0", &hostScript{receipt: true})
	fixture.deps.Targets = missingTarget{}

	_, err := fixture.run(context.Background())

	if !errors.Is(err, errNoDispatchTarget) {
		t.Fatalf("error = %v, want errNoDispatchTarget", err)
	}
	if released := waitForValue(t, fixture.limiter.releases, "the release of the slot the scheduler took for this assignment"); released != "host-0" {
		t.Fatalf("released slot = %q, want host-0", released)
	}
	reported := <-fixture.reported
	plan := reported.TimeoutPlan()
	if len(plan) != 1 || !plan[0].Post || plan[0].Nonce != 300 {
		t.Fatalf("timeout plan = %+v, want one posted vote for nonce 300", plan)
	}
	poster := &stubPoster{vote: "refused"}
	settleEvents(reported, poster)
	if len(poster.posts) != 1 || !poster.posts[0].sentAt.Equal(testEpoch) {
		t.Fatalf("posted votes = %+v, want one carrying the race's start %v", poster.posts, testEpoch)
	}
	if _, exemption := reported.Sample(reported.Attempts[0]); exemption != ExemptNeverDispatched {
		t.Fatalf("sample exemption = %v, want ExemptNeverDispatched", exemption)
	}
}

// missingTarget is the registry after the escrow the pick chose has already rotated out.
type missingTarget struct{}

func (missingTarget) Target(string) (DispatchTarget, bool) { return nil, false }

// Test flow:
//  1. Start a race with two hosts: one that receipts and returns immediately, one that receipts, streams content, and finishes.
//  2. Run the race to completion.
//  3. Assert the report carries both attempts.
//  4. Assert exactly one host-slot release is reported per attempt, with none left over.
func TestRunRaceReleasesOneHostSlotPerAttempt(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 2)
	fixture.host(70, 0, "host-0", &hostScript{receipt: true})
	fixture.host(71, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(71)},
		confirmed: true,
		finished:  true,
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported

	if len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2", len(reported.Attempts))
	}
	for range reported.Attempts {
		waitForValue(t, fixture.limiter.releases, "a host-slot release for every attempt")
	}
	if extra := len(fixture.limiter.releases); extra != 0 {
		t.Fatalf("host-slot releases = %d more than attempts, want one each", extra)
	}
}

// Test flow:
//  1. Start a race with a single host whose attempt fails with `ErrStateRootDivergence`.
//  2. Run the race to completion.
//  3. Assert the reported attempt is flagged `StateDivergent`.
//  4. Assert the picker recorded the host as diverged once but did not block it on this first divergence.
//  5. Assert the target rewound the divergent host's catch-up.
func TestRunRaceRewindsADivergentHostBeforeBlockingIt(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(80, 0, "host-0", &hostScript{receipt: true, err: ErrStateRootDivergence})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported
	if !reported.Attempts[0].StateDivergent {
		t.Fatal("StateDivergent = false, want true")
	}
	fixture.picker.mu.Lock()
	blocked, diverged := fixture.picker.blocked, fixture.picker.diverged
	fixture.picker.mu.Unlock()
	if len(diverged) != 1 || diverged[0] != [2]string{"escrow-1", "host-0"} {
		t.Fatalf("diverged hosts = %v, want the divergent one", diverged)
	}
	if len(blocked) != 0 {
		t.Fatalf("blocked hosts = %v, want none on the first divergence", blocked)
	}
	fixture.target.mu.Lock()
	rewound := fixture.target.rewound
	fixture.target.mu.Unlock()
	if len(rewound) != 1 || rewound[0] != 0 {
		t.Fatalf("rewound host indexes = %v, want the divergent host's", rewound)
	}
}

// Test flow:
//  1. Start a race with a denied host and a rival inside its receipt deadline, both scripted to report arrival before producing any content.
//  2. Let both attempts arrive, then release the rival to produce its content.
//  3. Assert the rival wins the race and it succeeded.
//  4. Assert the client stream carries none of the denied host's bytes.
func TestRunRaceSuppressesCrownForDeniedHostWhileARivalIsLive(t *testing.T) {
	policy := racePolicy(2)
	policy.ReceiptTimeout = time.Hour
	fixture := newRaceFixture(policy, 2)
	fixture.crown.denied["host-0"] = true
	arrive := make(chan uint64, 2)
	rivalRelease := make(chan struct{})
	fixture.host(90, 0, "host-0", &hostScript{
		arrive:    arrive,
		receipt:   true,
		chunks:    []string{contentChunk(90), "data: tail\n\n"},
		confirmed: true,
		finished:  true,
	})
	fixture.host(91, 1, "host-1", &hostScript{
		arrive:    arrive,
		release:   rivalRelease,
		receipt:   true,
		chunks:    []string{contentChunk(91)},
		confirmed: true,
		finished:  true,
	})

	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()

	<-arrive
	<-arrive
	close(rivalRelease)
	<-returned
	reported := <-fixture.reported

	if reported.WinnerNonce != 91 || !reported.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want the rival to serve", reported.WinnerNonce, reported.Succeeded)
	}
	if forwarded := fixture.client.forwarded(); bytes.Contains(forwarded, []byte(contentChunk(90))) {
		t.Fatalf("client stream %q carries the denied host's bytes", forwarded)
	}
}

// Test flow:
//  1. Start a race with a denied host, a receipt deadline the replacement cannot miss, and a picker that parks its escalation pick, using a classifier that reports the moment content is about to claim the crown.
//  2. Let the denied host's content reach the point of claiming the crown while its replacement pick is still parked.
//  3. Queue the replacement host and release the parked pick.
//  4. Assert the replacement wins the race and it succeeded.
//  5. Assert the client stream carries none of the denied host's bytes.
func TestRunRaceSuppressesCrownForDeniedHostWhileItsReplacementIsBeingPicked(t *testing.T) {
	policy := racePolicy(2)
	policy.ReceiptTimeout = time.Hour
	fixture := newRaceFixture(policy, 2)
	fixture.crown.denied["host-0"] = true
	fixture.picker.hold = make(chan struct{})
	fixture.picker.parked = make(chan struct{}, 1)
	claiming := make(chan struct{}, 1)
	fixture.deps.Classify = func(string) streamClassifier {
		return claimWatch{streamClassifier: markerClassifier{}, claiming: claiming}
	}
	fixture.host(90, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(90), "data: tail\n\n"},
		confirmed: true,
		finished:  true,
	})

	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()

	<-fixture.picker.parked
	<-claiming
	fixture.host(91, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(91)},
		confirmed: true,
		finished:  true,
	})
	close(fixture.picker.hold)
	<-returned
	reported := <-fixture.reported

	if reported.WinnerNonce != 91 || !reported.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want the replacement to serve", reported.WinnerNonce, reported.Succeeded)
	}
	if forwarded := fixture.client.forwarded(); bytes.Contains(forwarded, []byte(contentChunk(90))) {
		t.Fatalf("client stream %q carries the denied host's bytes", forwarded)
	}
}

// Test flow:
//  1. Start a race with a denied host and a picker that parks its escalation pick with no replacement queued behind it.
//  2. Let the attempt arrive, then release the parked pick so it comes back with no available host.
//  3. Assert the denied host still wins the race and it succeeded.
//  4. Assert the client stream is exactly the denied host's content and the report carries only that one attempt.
func TestRunRaceCrownsADeniedHostWhenItsReplacementPickFindsNoHost(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 2)
	fixture.crown.denied["host-0"] = true
	fixture.picker.hold = make(chan struct{})
	fixture.picker.parked = make(chan struct{}, 1)
	arrive := make(chan uint64, 1)
	fixture.host(90, 0, "host-0", &hostScript{
		arrive:    arrive,
		receipt:   true,
		chunks:    []string{contentChunk(90)},
		confirmed: true,
		finished:  true,
	})

	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()

	<-fixture.picker.parked
	<-arrive
	close(fixture.picker.hold)
	served := <-returned
	reported := <-fixture.reported

	if served.WinnerNonce != 90 || !served.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want the denied host's answer", served.WinnerNonce, served.Succeeded)
	}
	if forwarded := string(fixture.client.forwarded()); forwarded != contentChunk(90) {
		t.Fatalf("client stream = %q, want the answer the race paid for", forwarded)
	}
	if len(reported.Attempts) != 1 {
		t.Fatalf("attempts = %d, want only the denied host's", len(reported.Attempts))
	}
}

// Test flow:
//  1. Start a race with a single denied host that answers with content.
//  2. Run the race to completion.
//  3. Assert the outcome's winner is that host and the race succeeded.
//  4. Assert the client stream is exactly the host's content.
//  5. Assert the report does not deny the host's crown for producing content.
func TestRunRaceCrownsADeniedHostThatIsTheLastOneStanding(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.crown.denied["host-0"] = true
	fixture.host(90, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(90)},
		confirmed: true,
		finished:  true,
	})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	if outcome.WinnerNonce != 90 || !outcome.Succeeded {
		t.Fatalf("outcome = winner %d succeeded %v, want 90/true", outcome.WinnerNonce, outcome.Succeeded)
	}
	reported := <-fixture.reported
	if forwarded := string(fixture.client.forwarded()); forwarded != contentChunk(90) {
		t.Fatalf("client stream = %q, want the answer the race paid for", forwarded)
	}
	if reported.DeniesCrowning(reported.Attempts[0]) {
		t.Error("a host that produced content was charged as if it had answered with nothing")
	}
}

// Test flow:
//  1. Build attempt/phase combinations covering stream truncation, phase timing, nonce completion, host errors, and a missing receipt: "aborted mid stream", "started during generation", "generation over", "nonce finished", "host answered with an error", "never receipted".
//  2. Call `phaseAborted` with each case's `attempt`, `startedInInference`, and `generating` flags.
//  3. Assert each result matches the case's `want`.
func TestPhaseAborted(t *testing.T) {
	cases := []struct {
		name               string
		attempt            AttemptOutcome
		startedInInference bool
		generating         bool
		want               bool
	}{
		{"aborted mid stream", AttemptOutcome{Terminal: TerminalStreamTruncated}, true, true, true},
		{"started during generation", AttemptOutcome{Terminal: TerminalStreamTruncated}, false, true, false},
		{"generation over", AttemptOutcome{Terminal: TerminalStreamTruncated}, true, false, false},
		{"nonce finished", AttemptOutcome{Terminal: TerminalStreamTruncated, NonceFinished: true}, true, true, false},
		{"host answered with an error", AttemptOutcome{Terminal: TerminalErrorStream, ErrorSource: "error"}, true, true, false},
		{"never receipted", AttemptOutcome{Terminal: TerminalNoReceipt}, true, true, false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := phaseAborted(testCase.attempt, testCase.startedInInference, testCase.generating)
			if got != testCase.want {
				t.Fatalf("phaseAborted = %v, want %v", got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a PoC-validate snapshot and a PoC-generate snapshot, plus relaxed and strict PoC modes.
//  2. Assert the validate snapshot reads as a bypass but not as generation under relaxed mode.
//  3. Assert the generate snapshot reads as generation under relaxed mode.
//  4. Assert neither bypass nor generation is active for the generate snapshot under strict mode.
func TestPoCFactsAreDistinct(t *testing.T) {
	generating := chain.PhaseSnapshot{
		RequestsBlocked: true,
		EpochPhase:      chain.EpochPhasePoCGenerate,
	}
	validating := chain.PhaseSnapshot{
		RequestsBlocked: true,
		EpochPhase:      chain.EpochPhasePoCValidate,
	}
	relaxed := config.Modes{PoCMode: config.PoCModeRelaxed}
	strict := config.Modes{PoCMode: config.PoCModeOff}

	if !pocBypassActive(validating, relaxed) || pocGenerating(validating, relaxed) {
		t.Fatal("validation must read as a bypass but not as generation")
	}
	if !pocGenerating(generating, relaxed) {
		t.Fatal("generation phase must read as generation")
	}
	if pocBypassActive(generating, strict) || pocGenerating(generating, strict) {
		t.Fatal("strict mode never serves through a PoC phase")
	}
}

func pendingAttempt(sendTime time.Time) EscalationAttempt {
	return EscalationAttempt{LaunchTime: sendTime, ClockStart: sendTime}
}

func streamingAttempt(sendTime, lastChunk time.Time) EscalationAttempt {
	return EscalationAttempt{
		LaunchTime:   sendTime,
		ClockStart:   sendTime,
		ReceiptTime:  sendTime,
		FirstToken:   sendTime,
		FirstContent: sendTime,
		LastChunk:    lastChunk,
	}
}

func failedAfterEscalating(sendTime time.Time) EscalationAttempt {
	return EscalationAttempt{Done: true, Escalated: true, ClockStart: sendTime, ReceiptTime: sendTime}
}

// judgedPendingAttempt was already judged on its receipt deadline, so only an escalation can still arm on it.
func judgedPendingAttempt(sendTime time.Time) EscalationAttempt {
	attempt := pendingAttempt(sendTime)
	attempt.ReceiptDeadlineJudged = true
	return attempt
}

// Test flow:
//  1. Build a `deadlinePlan` from each case's policy timings (`receipt`, `stall`), `attempts`, `budget`, host-group `limit`, `drain` deadline, `pick` time, `cancelled` flag, and `retryRuledOut` flag.
//  2. Call `nextDeadline` with the plan.
//  3. Assert the returned trigger and deadline match the case's `wantTrigger` and `wantAt`, covering precedence and ties among escalation, stall, and hard-timeout triggers; budget exhaustion; a running pick; a crowned winner; a failure freeing the budget; host-group limits; a ruled-out retry; an already-stalled attempt; a missing stall knob; loser grace; the drain deadline; cancellation; and no attempts armed yet.
//  4. Assert an escalation stage is carried only on a `triggerEscalation` arm, and is `StageReceiptTimeout` there.
func TestNextDeadlinePrecedence(t *testing.T) {
	base := testEpoch
	const hard = streamingHardTimeout
	const hostGroupSize = 16

	cases := []struct {
		name          string
		receipt       time.Duration
		stall         time.Duration
		loserGrace    time.Duration
		budget        int
		limit         int
		drain         time.Time
		pick          time.Time
		cancelled     bool
		retryRuledOut bool
		attempts      []EscalationAttempt
		wantAt        time.Time
		wantTrigger   deadlineTrigger
	}{
		{
			name: "escalation earliest, winning the tie with the deadline it arms on", receipt: time.Second, stall: 2 * time.Second, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerEscalation,
		},
		{
			name: "stall earliest", receipt: 2 * time.Second, stall: time.Second, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerStall,
		},
		{
			name: "hard timeout earliest", receipt: 25 * time.Minute, stall: 30 * time.Minute, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "hard timeout wins a tie with escalation", receipt: hard, stall: 30 * time.Minute, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "hard timeout wins a tie with stall", receipt: 30 * time.Minute, stall: hard, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "escalation wins a tie with stall", receipt: 5 * time.Second, stall: 5 * time.Second, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(5 * time.Second),
			wantTrigger: triggerEscalation,
		},
		{
			name: "a running pick is already the escalation", receipt: time.Second, stall: 2 * time.Second, budget: 4,
			pick:        base.Add(schedulerPickTimeout),
			attempts:    []EscalationAttempt{judgedPendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(2 * time.Second),
			wantTrigger: triggerStall,
		},
		{
			name: "a crowned winner ends escalation", receipt: time.Second, stall: 2 * time.Second, budget: 4,
			attempts: []EscalationAttempt{
				judgedPendingAttempt(base),
				func() EscalationAttempt {
					attempt := streamingAttempt(base, base)
					attempt.Crowned = true
					return attempt
				}(),
			},
			wantAt:      base.Add(2 * time.Second),
			wantTrigger: triggerStall,
		},
		{
			name: "an exhausted budget ends escalation", receipt: time.Second, stall: 2 * time.Second, budget: 2,
			attempts:    []EscalationAttempt{judgedPendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(2 * time.Second),
			wantTrigger: triggerStall,
		},
		{
			name: "a missed deadline arms where escalation cannot", receipt: time.Second, stall: 2 * time.Second, budget: 2,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerMissedDeadline,
		},
		{
			name: "a missed deadline wins a tie with stall", receipt: 2 * time.Second, stall: 2 * time.Second, budget: 2,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(2 * time.Second),
			wantTrigger: triggerMissedDeadline,
		},
		{
			name: "a failed attempt frees its place in the budget", receipt: time.Second, stall: 2 * time.Second, budget: 2,
			attempts:    []EscalationAttempt{failedAfterEscalating(base), pendingAttempt(base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerEscalation,
		},
		{
			name: "the host group bounds the attempts a failure freed", receipt: time.Second, stall: 2 * time.Second, budget: 2, limit: 2,
			attempts:    []EscalationAttempt{failedAfterEscalating(base), judgedPendingAttempt(base)},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "a refusal that rules out a retry ends escalation", receipt: time.Second, stall: 2 * time.Second, budget: 4,
			retryRuledOut: true,
			attempts:      []EscalationAttempt{judgedPendingAttempt(base), streamingAttempt(base, base)},
			wantAt:        base.Add(2 * time.Second),
			wantTrigger:   triggerStall,
		},
		{
			name: "an already stalled attempt is not re-armed", receipt: time.Hour, stall: time.Second, budget: 1,
			attempts: []EscalationAttempt{
				func() EscalationAttempt {
					attempt := streamingAttempt(base, base)
					attempt.Stalled = true
					return attempt
				}(),
			},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "no stall knob leaves escalation", receipt: time.Second, stall: 0, budget: 4,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerEscalation,
		},
		{
			name:       "loser grace bounds a finished winner",
			receipt:    time.Hour,
			stall:      time.Hour,
			loserGrace: time.Minute,
			budget:     4,
			attempts: []EscalationAttempt{
				pendingAttempt(base),
				{
					Done:          true,
					Crowned:       true,
					NonceFinished: true,
					ClockStart:    base,
					ReceiptTime:   base,
					Completed:     base,
				},
			},
			wantAt:      base.Add(time.Minute),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "the drain deadline bounds a departed client's race", receipt: time.Second, stall: time.Hour, budget: 4,
			drain:       base.Add(time.Minute),
			attempts:    []EscalationAttempt{judgedPendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Minute),
			wantTrigger: triggerHardTimeout,
		},
		{
			name: "a departed client is owed no escalation", receipt: time.Second, stall: time.Second, budget: 4,
			drain:       base.Add(time.Hour),
			attempts:    []EscalationAttempt{judgedPendingAttempt(base), streamingAttempt(base, base)},
			wantAt:      base.Add(time.Second),
			wantTrigger: triggerStall,
		},
		{
			name: "cancellation disarms everything", receipt: time.Second, stall: time.Second, budget: 4,
			cancelled:   true,
			attempts:    []EscalationAttempt{pendingAttempt(base), streamingAttempt(base, base)},
			wantTrigger: triggerNone,
		},
		{
			name: "nothing armed before dispatch", receipt: time.Second, stall: time.Second, budget: 4,
			attempts:    []EscalationAttempt{{}},
			wantTrigger: triggerNone,
		},
		{
			name: "the hard timeout runs from launch while the session catches the host up", receipt: time.Second, stall: time.Second, budget: 4,
			attempts:    []EscalationAttempt{{LaunchTime: base}},
			wantAt:      base.Add(hard),
			wantTrigger: triggerHardTimeout,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			plan := deadlinePlan{
				Policy: EscalationPolicy{
					ReceiptTimeout:  testCase.receipt,
					FirstTokenFloor: time.Hour,
					InterChunkStall: testCase.stall,
					LoserGrace:      testCase.loserGrace,
				},
				Attempts:  testCase.attempts,
				Budget:    testCase.budget,
				Limit:     cmp.Or(testCase.limit, hostGroupSize),
				Drain:     testCase.drain,
				Pick:      testCase.pick,
				Cancelled: testCase.cancelled,

				RetryRuledOut: testCase.retryRuledOut,
			}
			arm := nextDeadline(base, plan)
			if arm.Trigger != testCase.wantTrigger {
				t.Fatalf("trigger = %v, want %v", arm.Trigger, testCase.wantTrigger)
			}
			if !arm.At.Equal(testCase.wantAt) {
				t.Fatalf("deadline = %v, want %v", arm.At, testCase.wantAt)
			}
			if testCase.wantTrigger != triggerEscalation && arm.Escalation.Stage != StageNone {
				t.Fatalf("escalation carried on a %v arm: %+v", arm.Trigger, arm.Escalation)
			}
			if testCase.wantTrigger == triggerEscalation && arm.Escalation.Stage != StageReceiptTimeout {
				t.Fatalf("escalation stage = %q, want the receipt timeout", arm.Escalation.Stage)
			}
		})
	}
}

// pausedCoordinator holds attempts the test wrote itself, so one wake-up can be driven in isolation
// instead of being reached through a whole race.
func pausedCoordinator(fixture *raceFixture, budget int, attempts ...*liveAttempt) *raceCoordinator {
	return pausedCoordinatorForClient(context.Background(), fixture, budget, attempts...)
}

// departedClient is the context of a client that has already hung up.
func departedClient() context.Context {
	clientCtx, leave := context.WithCancel(context.Background())
	leave()
	return clientCtx
}

func pausedCoordinatorForClient(clientCtx context.Context, fixture *raceFixture, budget int, attempts ...*liveAttempt) *raceCoordinator {
	coordinator := newCoordinator(clientCtx, fixture.deps, fixture.request)
	coordinator.target = fixture.target
	coordinator.budget = budget
	coordinator.attemptLimit = fixture.deps.Policy.AttemptLimit(fixture.target.HostCount(), false)
	coordinator.attempts = attempts
	coordinator.pending = len(attempts)
	for _, attempt := range attempts {
		coordinator.byNonce[attempt.nonce] = attempt
	}
	return coordinator
}

func stalledFixtureCoordinator(policy EscalationPolicy, attempts ...*liveAttempt) *raceCoordinator {
	return pausedCoordinator(newRaceFixture(policy, 1), 1, attempts...)
}

// Test flow:
//  1. Start a paused coordinator with one running attempt, a speculative pick still waiting in the scheduler and another immediate pick owed.
//  2. Crown the running attempt.
//  3. Assert the waiting pick was cancelled and no further pick is owed: a nonce the scheduler hands over after the crown is still sent, for a client that already has its answer.
func TestCrowningAWinnerGivesUpThePickStillWaiting(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 2)
	running := &liveAttempt{nonce: 100, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 3, running)
	pickCancelled := false
	coordinator.pickCancel = func() { pickCancelled = true }
	coordinator.moreImmediate = 1

	coordinator.crownWinner(running, "first_content")

	if !pickCancelled {
		t.Fatal("the waiting pick was not cancelled when the race crowned its winner")
	}
	if coordinator.moreImmediate != 0 {
		t.Fatalf("immediate picks still owed = %d, want 0 once a winner is crowned", coordinator.moreImmediate)
	}
}

// Test flow:
//  1. Start a paused coordinator with one refused attempt.
//  2. Queue an `AttemptDone` event for that attempt reporting a trusted context-length rejection.
//  3. Apply a pick's answer for a second nonce after the rejection event is already queued.
//  4. Assert the coordinator now tracks two attempts: the rejected one and the picked one.
//  5. Assert the picked attempt is marked done with `TerminalNoReceipt`, since the rejection had already ended the search before its nonce could be dispatched.
func TestAPickAnswerIsJudgedAgainstTheEventsAlreadyDelivered(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 2)
	refused := &liveAttempt{nonce: 100, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 2, refused)
	coordinator.pickCancel = func() {}
	coordinator.events <- AttemptEvent{Kind: AttemptDone, Nonce: 100, At: testEpoch, Outcome: &AttemptOutcome{
		Nonce:        100,
		Participant:  "host-0",
		Terminal:     TerminalCapabilityRefused,
		ErrorSource:  "error.BadRequestError",
		ErrorType:    "BadRequestError",
		ErrorMessage: vllmContextTotalMessage,
	}}

	coordinator.applyPick(pickedHost{assignment: scheduler.Assignment{
		Escrow: "escrow-1",
		Host:   "host-1",
		Nonce:  fakePrepared{nonce: 101, hostIdx: 1},
	}})

	if len(coordinator.attempts) != 2 {
		t.Fatalf("attempts = %d, want the refused one and the stranded pick", len(coordinator.attempts))
	}
	if picked := coordinator.attempts[1]; !picked.done || picked.outcome.Terminal != TerminalNoReceipt {
		t.Fatalf("picked attempt done = %v outcome = %+v, want stranded without a receipt", picked.done, picked.outcome)
	}
}

// Test flow:
//  1. Start a paused coordinator with a refused attempt and a sibling attempt still running.
//  2. Apply an `AttemptDone` event on the first attempt reporting a trusted context-length rejection.
//  3. Assert the next deadline no longer arms an escalation.
//  4. Assert the sibling's context was not cancelled.
func TestATrustedContextLengthRejectionDisarmsEscalationWithoutCancellingASibling(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	siblingCancelled := false
	refused := &liveAttempt{nonce: 100, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
	sibling := &liveAttempt{
		nonce:       101,
		participant: "host-1",
		sendTime:    testEpoch,
		receiptTime: testEpoch,
		cancel:      func() { siblingCancelled = true },
	}
	coordinator := pausedCoordinator(fixture, 3, refused, sibling)

	coordinator.apply(AttemptEvent{Kind: AttemptDone, Nonce: 100, At: testEpoch, Outcome: &AttemptOutcome{
		Nonce:        100,
		Participant:  "host-0",
		Terminal:     TerminalCapabilityRefused,
		ErrorSource:  "error.BadRequestError",
		ErrorType:    "BadRequestError",
		ErrorMessage: vllmContextTotalMessage,
	}})

	if arm := nextDeadline(coordinator.deps.Now(), coordinator.plan()); arm.Trigger == triggerEscalation {
		t.Fatalf("arm = %+v, want no escalation once a trusted host rejected the prompt", arm)
	}
	if siblingCancelled {
		t.Fatal("the rejection cancelled an attempt still running")
	}
}

// Test flow:
//  1. For "a delivered first token withdraws the escalation it would have confirmed", "a delivered context-length rejection withdraws the escalation it would have confirmed", and "a delivered chunk withdraws the stall it would have flagged": compute the next deadline, queue the event that already resolves it, then call `expire` on that deadline.
//  2. Assert each queued event pre-empts its deadline: no successor pick is started and the attempt is not flagged stalled.
//  3. For "a delivered completion releases the client instead of cancelling it": queue the winner's completion event, then call `depart`.
//  4. Assert `depart` reports the client as released rather than gone, since the winner had already completed.
func TestADeadlineIsJudgedAgainstTheEventsAlreadyDelivered(t *testing.T) {
	t.Run("a delivered first token withdraws the escalation it would have confirmed", func(t *testing.T) {
		policy := settledPolicy()
		policy.FirstTokenFloor = time.Second
		fixture := newRaceFixture(policy, 2)
		fixture.host(401, 1, "host-1", &hostScript{receipt: true})
		receipted := &liveAttempt{
			nonce:       400,
			participant: "host-0",
			sendTime:    testEpoch.Add(-2 * time.Second),
			receiptTime: testEpoch.Add(-2 * time.Second),
			cancel:      func() {},
		}
		coordinator := pausedCoordinator(fixture, 2, receipted)
		arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
		if arm.Trigger != triggerEscalation || arm.Escalation.Stage != StageFirstToken {
			t.Fatalf("arm = %+v, want the first-token escalation", arm)
		}
		coordinator.events <- AttemptEvent{Kind: AttemptFirstToken, Nonce: 400, At: testEpoch}

		coordinator.expire(arm)

		if len(coordinator.attempts) != 1 {
			t.Fatalf("attempts = %d, want 1: a host that has started streaming is owed no successor", len(coordinator.attempts))
		}
		fixture.picker.mu.Lock()
		picks := len(fixture.picker.profiles)
		fixture.picker.mu.Unlock()
		if picks != 0 {
			t.Fatalf("picks = %d, want 0: an extra nonce was committed on a cleared deadline", picks)
		}
	})

	t.Run("a delivered context-length rejection withdraws the escalation it would have confirmed", func(t *testing.T) {
		policy := settledPolicy()
		policy.ReceiptTimeout = time.Second
		fixture := newRaceFixture(policy, 3)
		unreceipted := &liveAttempt{
			nonce:       430,
			participant: "host-0",
			sendTime:    testEpoch.Add(-2 * time.Second),
			cancel:      func() {},
		}
		rejecting := &liveAttempt{
			nonce:       431,
			participant: "host-1",
			sendTime:    testEpoch.Add(-2 * time.Second),
			receiptTime: testEpoch.Add(-2 * time.Second),
			cancel:      func() {},
		}
		coordinator := pausedCoordinator(fixture, 3, unreceipted, rejecting)
		arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
		if arm.Trigger != triggerEscalation || arm.Escalation.Stage != StageReceiptTimeout {
			t.Fatalf("arm = %+v, want the receipt-timeout escalation", arm)
		}
		coordinator.events <- AttemptEvent{Kind: AttemptDone, Nonce: 431, At: testEpoch, Outcome: &AttemptOutcome{
			Nonce:        431,
			Participant:  "host-1",
			Terminal:     TerminalCapabilityRefused,
			ErrorSource:  "error.BadRequestError",
			ErrorType:    "BadRequestError",
			ErrorMessage: vllmContextTotalMessage,
		}}

		coordinator.expire(arm)

		if coordinator.picking() {
			t.Fatal("a pick started after a trusted host rejected the prompt as past the model's context length")
		}
	})

	t.Run("a delivered chunk withdraws the stall it would have flagged", func(t *testing.T) {
		policy := settledPolicy()
		policy.InterChunkStall = time.Minute
		fixture := newRaceFixture(policy, 1)
		streaming := &liveAttempt{
			nonce:        410,
			participant:  "host-0",
			sendTime:     testEpoch.Add(-3 * time.Minute),
			receiptTime:  testEpoch.Add(-3 * time.Minute),
			firstToken:   testEpoch.Add(-3 * time.Minute),
			firstContent: testEpoch.Add(-2 * time.Minute),
			lastChunk:    testEpoch.Add(-2 * time.Minute),
			cancel:       func() {},
		}
		coordinator := pausedCoordinator(fixture, 1, streaming)
		arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
		if arm.Trigger != triggerStall {
			t.Fatalf("arm = %+v, want the stall", arm)
		}
		coordinator.events <- AttemptEvent{Kind: AttemptChunk, Nonce: 410, At: testEpoch}

		coordinator.expire(arm)

		if streaming.stalled {
			t.Fatal("a host whose chunk was already delivered was flagged as having gone silent")
		}
	})

	t.Run("a delivered completion releases the client instead of cancelling it", func(t *testing.T) {
		fixture := newRaceFixture(settledPolicy(), 1)
		fixture.host(420, 0, "host-0", &hostScript{finished: true})
		winner := &liveAttempt{
			nonce:       420,
			participant: "host-0",
			sendTime:    testEpoch,
			receiptTime: testEpoch,
			cancel:      func() {},
		}
		coordinator := pausedCoordinator(fixture, 1, winner)
		coordinator.winner = winner
		coordinator.events <- AttemptEvent{
			Kind:    AttemptDone,
			Nonce:   420,
			At:      testEpoch,
			Outcome: &AttemptOutcome{Nonce: 420, Terminal: TerminalLost, ContentChunks: 1},
		}

		if exit := coordinator.depart(); exit == exitClientGone {
			t.Fatal("a winner that had already completed was reported to its client as cancelled")
		}
	})
}

// Test flow:
//  1. Start a race with one host and a picker that parks the escalation pick the policy triggers immediately.
//  2. Let the host attempt arrive and its escalation pick reach the scheduler while parked.
//  3. Release the host to stream its first content chunk and assert the crowned content already reaches the client stream.
//  4. Let the host resume to finish, then advance the clock past the scheduler's pick timeout to expire the parked pick.
//  5. Assert the race ends with the host as winner and succeeded.
//  6. Assert the report carries only the one attempt the race actually started.
func TestAParkedEscalationPickBlocksNeitherTheWinnerNorTheRace(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 2)
	held, parked := make(chan struct{}), make(chan struct{}, 1)
	fixture.picker.hold, fixture.picker.parked = held, parked
	t.Cleanup(func() { close(held) })

	dispatched, streamed := make(chan uint64, 1), make(chan uint64, 1)
	release, resume := make(chan struct{}), make(chan struct{})
	fixture.host(500, 0, "host-0", &hostScript{
		arrive:    dispatched,
		release:   release,
		receipt:   true,
		streaming: streamed,
		resume:    resume,
		chunks:    []string{contentChunk(500), "data: tail\n\n"},
		confirmed: true,
		finished:  true,
	})

	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()

	<-dispatched
	waitForValue(t, parked, "the escalation reaching the scheduler")

	close(release)
	waitForValue(t, streamed, "the winner's first content chunk getting past the coordinator's crown answer")
	if forwarded := fixture.client.forwarded(); !bytes.Contains(forwarded, []byte(contentChunk(500))) {
		t.Fatalf("client stream %q is missing the crowned winner's bytes", forwarded)
	}
	close(resume)

	fixture.clock.waitArmed(t, schedulerPickTimeout)
	fixture.clock.advance(schedulerPickTimeout)

	if outcome := waitForValue(t, returned, "the race ending while its escalation pick was parked"); outcome.WinnerNonce != 500 || !outcome.Succeeded {
		t.Fatalf("outcome = winner %d succeeded %v, want 500/true", outcome.WinnerNonce, outcome.Succeeded)
	}
	if reported := <-fixture.reported; len(reported.Attempts) != 1 {
		t.Fatalf("reported attempts = %d, want only the one the race started", len(reported.Attempts))
	}
}

// Test flow:
//  1. Start a paused coordinator for an already-departed client with one failed attempt due for an escalation.
//  2. Compute the next deadline and assert it arms the failed attempt's escalation.
//  3. Call `expire` on that deadline.
//  4. Assert no pick was started and the picker received no profile, since the client had already left.
func TestAnEscalationDueAsTheClientLeavesStartsNoPick(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 2)
	failed := &liveAttempt{nonce: 440, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, done: true, cancel: func() {}}
	coordinator := pausedCoordinatorForClient(departedClient(), fixture, 2, failed)
	arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
	if arm.Trigger != triggerEscalation || arm.Escalation.Stage != StageAttemptFailed {
		t.Fatalf("arm = %+v, want the failed attempt's escalation", arm)
	}

	coordinator.expire(arm)

	if coordinator.picking() {
		t.Fatal("a pick started for a client that had already left")
	}
	fixture.picker.mu.Lock()
	picks := len(fixture.picker.profiles)
	fixture.picker.mu.Unlock()
	if picks != 0 {
		t.Fatalf("picks = %d, want 0: a nonce was requested for a client that had already left", picks)
	}
}

// Test flow:
//  1. Start a paused coordinator with one already-failed, escalated attempt and one attempt still awaiting its receipt.
//  2. Compute the next deadline and assert it arms the waiting attempt's receipt timeout, now that the failure freed a budget slot.
//  3. Call `expire` on that deadline.
//  4. Assert a pick was started for a replacement.
func TestAFailedAttemptFreesItsPlaceForAnotherPick(t *testing.T) {
	policy := refusalPolicy()
	policy.ReceiptTimeout = time.Second
	fixture := newRaceFixture(policy, 3)
	sentBeforeTheReceiptTimeout := fixture.deps.Now().Add(-time.Minute)
	failed := &liveAttempt{nonce: 450, participant: "host-0", sendTime: sentBeforeTheReceiptTimeout, receiptTime: sentBeforeTheReceiptTimeout, escalated: true, done: true, cancel: func() {}}
	waiting := &liveAttempt{nonce: 451, participant: "host-1", sendTime: sentBeforeTheReceiptTimeout, cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 2, failed, waiting)
	arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
	if arm.Trigger != triggerEscalation || arm.Escalation.Stage != StageReceiptTimeout {
		t.Fatalf("arm = %+v, want the waiting attempt's receipt timeout", arm)
	}

	coordinator.expire(arm)
	t.Cleanup(coordinator.stopPicking)

	if !coordinator.picking() {
		t.Fatal("no pick started, although the failed attempt left a place in the budget")
	}
}

// Test flow:
//  1. Start a paused coordinator with one silent attempt and a budget already spent.
//  2. Compute the next deadline and assert it is a missed first-token deadline, not an escalation.
//  3. Call `expire` on that deadline.
//  4. Assert the limiter records the host's window as having missed the first-token deadline and the attempt was not cancelled.
//  5. Assert the outcome flags the attempt's `FirstTokenDeadlineMissed`.
//  6. Assert computing the next deadline again does not re-arm the same missed deadline.
func TestAMissedFirstTokenDeadlineNarrowsTheHostWithoutStoppingItsAttempt(t *testing.T) {
	policy := settledPolicy()
	policy.FirstTokenFloor = time.Second
	fixture := newRaceFixture(policy, 2)
	cancelled := false
	silent := &liveAttempt{
		nonce:       460,
		participant: "host-0",
		sendTime:    testEpoch.Add(-2 * time.Second),
		receiptTime: testEpoch.Add(-2 * time.Second),
		cancel:      func() { cancelled = true },
	}
	coordinator := pausedCoordinator(fixture, 1, silent)
	arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
	if arm.Trigger != triggerMissedDeadline {
		t.Fatalf("arm = %+v, want the missed first-token deadline", arm)
	}

	coordinator.expire(arm)

	if move := waitForValue(t, fixture.limiter.verdicts, "the host's window being judged"); move != (windowMove{participant: "host-0", verdict: limits.MissedFirstTokenDeadline}) {
		t.Fatalf("window move = %+v, want host-0 to have missed the first-token deadline", move)
	}
	if cancelled {
		t.Fatal("the missed deadline cancelled an attempt that still owes its nonce")
	}
	if !coordinator.outcome().Attempts[0].FirstTokenDeadlineMissed {
		t.Fatal("the outcome does not record the first-token deadline the host missed")
	}
	if next := nextDeadline(coordinator.deps.Now(), coordinator.plan()); next.Trigger == triggerMissedDeadline {
		t.Fatalf("arm = %+v, want a deadline judged only once", next)
	}
}

// Test flow:
//  1. Start a paused coordinator inside a relaxed-mode proof-of-compute generation phase, with one attempt overdue for its receipt.
//  2. Compute the next deadline and assert it is a missed receipt deadline.
//  3. Call `expire` on that deadline.
//  4. Assert the limiter recorded no window move and the outcome does not flag the deadline as missed, since the PoC phase excuses it.
//  5. Assert computing the next deadline again does not re-arm the excused deadline.
func TestADeadlineMissedDuringProofOfComputeIsExcused(t *testing.T) {
	policy := settledPolicy()
	policy.ReceiptTimeout = time.Second
	fixture := newRaceFixture(policy, 1)
	fixture.deps.Snapshots = stubSnapshots{snapshot: chain.PhaseSnapshot{EpochPhase: chain.EpochPhasePoCGenerate}}
	fixture.deps.Modes = config.Modes{PoCMode: config.PoCModeRelaxed}
	unreceipted := &liveAttempt{nonce: 480, participant: "host-0", sendTime: testEpoch.Add(-2 * time.Second), cancel: func() {}}
	coordinator := pausedCoordinator(fixture, 1, unreceipted)
	arm := nextDeadline(coordinator.deps.Now(), coordinator.plan())
	if arm.Trigger != triggerMissedDeadline {
		t.Fatalf("arm = %+v, want the missed receipt deadline", arm)
	}

	coordinator.expire(arm)

	if moves := len(fixture.limiter.verdicts); moves != 0 {
		t.Fatalf("window moves = %d, want none inside a proof-of-compute phase", moves)
	}
	if coordinator.outcome().Attempts[0].ReceiptDeadlineMissed {
		t.Fatal("the outcome charges the host a deadline the phase excused")
	}
	if next := nextDeadline(coordinator.deps.Now(), coordinator.plan()); next.Trigger == triggerMissedDeadline {
		t.Fatalf("arm = %+v, want the excused deadline settled rather than armed again", next)
	}
}

// Test flow:
//  1. Start a race with a single host held after arrival, using a one-second receipt timeout.
//  2. Let the attempt arrive, wait for the receipt deadline to arm, then advance the clock past it.
//  3. Assert the limiter records the host's window as having missed the receipt deadline.
//  4. Release the host to finish with content.
//  5. Assert the finished race reports the attempt as `TerminalWon` with the receipt deadline flagged missed.
//  6. Assert the deadline was judged only once, with no further window moves.
func TestRunRaceNarrowsALateHostAtTheDeadlineAndLetsItFinish(t *testing.T) {
	policy := settledPolicy()
	policy.ReceiptTimeout = time.Second
	fixture := newRaceFixture(policy, 1)
	dispatched, release := make(chan uint64, 1), make(chan struct{})
	fixture.host(470, 0, "host-0", &hostScript{
		arrive:    dispatched,
		release:   release,
		receipt:   true,
		chunks:    []string{contentChunk(470)},
		confirmed: true,
		finished:  true,
	})
	returned := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		returned <- outcome
	}()
	<-dispatched
	fixture.clock.waitArmed(t, time.Second)

	fixture.clock.advance(time.Second)

	if move := waitForValue(t, fixture.limiter.verdicts, "the late host's window being judged"); move != (windowMove{participant: "host-0", verdict: limits.MissedReceiptDeadline}) {
		t.Fatalf("window move = %+v, want host-0 to have missed the receipt deadline", move)
	}
	close(release)
	waitForValue(t, returned, "the late attempt finishing its race")
	reported := waitForValue(t, fixture.reported, "the race's report")
	if attempt := reported.Attempts[0]; attempt.Terminal != TerminalWon || !attempt.ReceiptDeadlineMissed {
		t.Fatalf("attempt = %s with receipt deadline missed %t, want a won attempt that missed it", attempt.Terminal, attempt.ReceiptDeadlineMissed)
	}
	if moves := len(fixture.limiter.verdicts); moves != 0 {
		t.Fatalf("window moves after the first = %d, want the deadline judged once", moves)
	}
}

// Test flow:
//  1. For each case, start a paused coordinator with one attempt live whose race wants no further attempt: its client left and the race was handed off, or a winner was crowned.
//  2. Apply a pick answering with a committed nonce.
//  3. Assert the nonce reaches its host and its attempt runs, rather than being stranded for a timeout vote.
func TestACommittedPickIsSentOnceTheRaceNoLongerWantsIt(t *testing.T) {
	cases := []struct {
		name    string
		client  context.Context
		crowned bool
	}{
		{name: "the client left", client: departedClient()},
		{name: "a winner was crowned", client: context.Background(), crowned: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRaceFixture(refusalPolicy(), 3)
			arrived := make(chan uint64, 1)
			fixture.target.scripts[451] = &hostScript{receipt: true, finished: true, arrive: arrived}
			primary := &liveAttempt{nonce: 450, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
			coordinator := pausedCoordinatorForClient(testCase.client, fixture, 3, primary)
			coordinator.pickCancel = func() {}
			if testCase.crowned {
				coordinator.winner = primary
			} else {
				coordinator.detach()
			}

			coordinator.applyPick(pickedHost{assignment: scheduler.Assignment{
				Escrow: "escrow-1",
				Host:   "host-1",
				Nonce:  fakePrepared{nonce: 451, hostIdx: 1},
			}})

			if nonce := waitForValue(t, arrived, "the committed nonce reaching its host"); nonce != 451 {
				t.Fatalf("sent nonce = %d, want 451", nonce)
			}
			awaitTracedAttemptDone(t, coordinator.events)
			if picked := coordinator.attempts[len(coordinator.attempts)-1]; picked.nonce != 451 || picked.outcome != nil {
				t.Fatalf("picked attempt nonce = %d outcome = %+v, want 451 launched, not stranded", picked.nonce, picked.outcome)
			}
		})
	}
}

// Test flow:
//  1. Start a paused coordinator for an already-departed client with one attempt live and another immediate slot still owed.
//  2. Apply a pick answering that immediate slot's nonce, then wait for its attempt to finish.
//  3. Assert no further pick was started and the picker received no profile, since the client had already left.
func TestAnImmediateAttemptOwedAsTheClientLeavesStartsNoPick(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.target.scripts[451] = &hostScript{receipt: true, finished: true}
	primary := &liveAttempt{nonce: 450, participant: "host-0", sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
	coordinator := pausedCoordinatorForClient(departedClient(), fixture, 3, primary)
	coordinator.pickCancel, coordinator.moreImmediate = func() {}, 1

	coordinator.applyPick(pickedHost{assignment: scheduler.Assignment{
		Escrow: "escrow-1",
		Host:   "host-1",
		Nonce:  fakePrepared{nonce: 451, hostIdx: 1},
	}})
	awaitTracedAttemptDone(t, coordinator.events)

	if coordinator.picking() {
		t.Fatal("an immediate attempt's pick started for a client that had already left")
	}
	fixture.picker.mu.Lock()
	picks := len(fixture.picker.profiles)
	fixture.picker.mu.Unlock()
	if picks != 0 {
		t.Fatalf("picks = %d, want 0: a nonce was requested for a client that had already left", picks)
	}
}

// Test flow:
//  1. Start a race during a chain phase that blocks new requests, with two hosts queued behind a two-attempt budget.
//  2. Vary the gateway's PoC mode across the table's cases: strict mode, and relaxed mode serving through the block.
//  3. Run the race to completion.
//  4. Assert the number of attempts started matches each case's `want`: one attempt clamped in strict mode, both attempts allowed in relaxed mode.
func TestBudgetClampsOnlyWhenTheGatewayIsNotServingThroughTheBlock(t *testing.T) {
	blocked := chain.PhaseSnapshot{RequestsBlocked: true, EpochPhase: chain.EpochPhasePoCGenerate}
	cases := []struct {
		name  string
		modes config.Modes
		want  int
	}{
		{name: "strict mode leaves one nonce to spend", modes: config.Modes{}, want: 1},
		{name: "relaxed mode is serving through the block", modes: config.Modes{PoCMode: config.PoCModeRelaxed}, want: 2},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRaceFixture(racePolicy(2), 4)
			fixture.deps.Snapshots = stubSnapshots{snapshot: blocked}
			fixture.deps.Modes = testCase.modes
			fixture.host(440, 0, "host-0", &hostScript{receipt: true})
			fixture.host(441, 1, "host-1", &hostScript{receipt: true})

			if _, err := fixture.run(context.Background()); err != nil {
				t.Fatalf("runRace error = %v", err)
			}
			reported := <-fixture.reported

			if len(reported.Attempts) != testCase.want {
				t.Fatalf("attempts = %d, want %d", len(reported.Attempts), testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Start a coordinator with five attempts: one silent since its first content, one silent since a role chunk with no content, one with a recent chunk, one that never streamed, and one already done.
//  2. Set the clock two minutes past the attempts' timestamps and call `markStalls`.
//  3. Assert only the two silent, still-live attempts that streamed are flagged stalled: the stall runs from the first chunk, whether or not it carried content.
func TestMarkStallsFlagsSilentStreamingAttempts(t *testing.T) {
	base := testEpoch
	policy := settledPolicy()
	policy.InterChunkStall = time.Minute

	silent := &liveAttempt{nonce: 1, firstToken: base, firstContent: base, lastChunk: base}
	roleOnly := &liveAttempt{nonce: 5, firstToken: base, lastChunk: base}
	recent := &liveAttempt{nonce: 2, firstToken: base, firstContent: base, lastChunk: base.Add(time.Hour)}
	neverStreamed := &liveAttempt{nonce: 3}
	finished := &liveAttempt{nonce: 4, firstToken: base, firstContent: base, lastChunk: base, done: true}

	coordinator := stalledFixtureCoordinator(policy, silent, roleOnly, recent, neverStreamed, finished)
	coordinator.deps.Now = func() time.Time { return base.Add(2 * time.Minute) }
	coordinator.markStalls()

	for _, expectation := range []struct {
		attempt *liveAttempt
		want    bool
	}{{silent, true}, {roleOnly, true}, {recent, false}, {neverStreamed, false}, {finished, false}} {
		if expectation.attempt.stalled != expectation.want {
			t.Fatalf("attempt %d stalled = %v, want %v", expectation.attempt.nonce, expectation.attempt.stalled, expectation.want)
		}
	}
}

// Test flow:
//  1. Start a coordinator with three attempts: one stalled with content, one cancelled with no content, and one crowned as the winner.
//  2. Assemble the outcome.
//  3. Assert the stalled content-bearing attempt is rewritten to `TerminalStalled`, the empty cancelled attempt keeps `TerminalClientCancelled`, and the winner is rewritten to `TerminalWon`.
//  4. Assert the outcome reports nonce 3 as the winner, succeeded, with `NonceFinished` set.
func TestOutcomeRewritesTerminalsTheCoordinatorAloneKnows(t *testing.T) {
	base := testEpoch
	stalledOut := &liveAttempt{
		nonce:   1,
		stalled: true,
		done:    true,
		outcome: &AttemptOutcome{Nonce: 1, Terminal: TerminalClientCancelled, ContentChunks: 3, SendTime: base},
	}
	cancelledEmpty := &liveAttempt{
		nonce:   2,
		stalled: true,
		done:    true,
		outcome: &AttemptOutcome{Nonce: 2, Terminal: TerminalClientCancelled, SendTime: base},
	}
	crowned := &liveAttempt{
		nonce:         3,
		done:          true,
		nonceFinished: true,
		outcome:       &AttemptOutcome{Nonce: 3, Terminal: TerminalLost, ContentChunks: 2, SendTime: base},
	}

	coordinator := stalledFixtureCoordinator(settledPolicy(), stalledOut, cancelledEmpty, crowned)
	coordinator.winner = crowned
	outcome := coordinator.outcome()

	wanted := map[uint64]Terminal{1: TerminalStalled, 2: TerminalClientCancelled, 3: TerminalWon}
	for _, attempt := range outcome.Attempts {
		if attempt.Terminal != wanted[attempt.Nonce] {
			t.Fatalf("attempt %d terminal = %v, want %v", attempt.Nonce, attempt.Terminal, wanted[attempt.Nonce])
		}
	}
	if outcome.WinnerNonce != 3 || !outcome.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want 3/true", outcome.WinnerNonce, outcome.Succeeded)
	}
	if !outcome.Attempts[2].NonceFinished {
		t.Fatal("NonceFinished = false on the winner, want true")
	}
}

// Test flow:
//  1. Acquire another in-flight request for the same participant on a limiter with a cut-off threshold of one.
//  2. Start a coordinator with one attempt and complete it as unavailable while the other request is still open.
//  3. Release the other request, then report the outcome's verdicts to the limiter.
//  4. Assert the participant is not cut off, since the refusal landed while the host was still carrying other work of ours.
func TestARefusalFromAHostStillCarryingOurWorkLeavesItsCutoffCountAlone(t *testing.T) {
	limiter := limits.NewParticipantLimiter(limiterConfig(1), func() time.Time { return testEpoch })
	otherRequest, admission := limiter.Acquire(testParticipant, testModel, limits.TokenCost{Input: 1, Output: 1})
	if admission != limits.AdmissionOpen || otherRequest == nil {
		t.Fatalf("Acquire() for the host's other request = %s, want open", admission)
	}
	refused := refusedAttempt(100)
	coordinator := refusalCoordinator(limiter, refused)

	coordinator.complete(refused, unavailableDone(100))
	otherRequest()
	reportVerdicts(limiter, coordinator.outcome())

	if admission := limiter.Admits(testParticipant, testModel); admission == limits.AdmissionCutOff {
		t.Fatal("Admits() = cut_off, want the cut-off closed: the host refused while it still carried another request of ours, so it was busy rather than broken")
	}
}

// Test flow:
//  1. Start a coordinator with one attempt on a limiter with a cut-off threshold of one and nothing else in flight.
//  2. Complete the attempt as unavailable.
//  3. Assert the participant is cut off immediately, since the refusal landed with the host carrying nothing else of ours.
func TestARefusalFromAHostCarryingNothingElseCountsTheMomentItLands(t *testing.T) {
	limiter := limits.NewParticipantLimiter(limiterConfig(1), func() time.Time { return testEpoch })
	refused := refusedAttempt(100)
	coordinator := refusalCoordinator(limiter, refused)

	coordinator.complete(refused, unavailableDone(100))

	if admission := limiter.Admits(testParticipant, testModel); admission != limits.AdmissionCutOff {
		t.Fatalf("Admits() right after the refusal = %s, want cut_off: a host refusing while it carried nothing else of ours counts towards its cut-off when the refusal lands", admission)
	}
}

func refusedAttempt(nonce uint64) *liveAttempt {
	return &liveAttempt{nonce: nonce, participant: testParticipant, sendTime: testEpoch, receiptTime: testEpoch, cancel: func() {}}
}

func refusalCoordinator(limiter *limits.ParticipantLimiter, attempts ...*liveAttempt) *raceCoordinator {
	fixture := newRaceFixture(settledPolicy(), 2)
	fixture.deps.Limiter = limiter
	return pausedCoordinator(fixture, 2, attempts...)
}

func unavailableDone(nonce uint64) AttemptEvent {
	return AttemptEvent{Kind: AttemptDone, Nonce: nonce, At: testEpoch, Outcome: &AttemptOutcome{
		Nonce:       nonce,
		Participant: testParticipant,
		SendTime:    testEpoch,
		Completed:   testEpoch,
		Terminal:    TerminalUnavailable,
	}}
}

func reportVerdicts(limiter *limits.ParticipantLimiter, outcome RaceOutcome) {
	for _, attempt := range outcome.Attempts {
		if verdict, moves := outcome.Verdict(attempt); moves {
			limiter.OnResult(limits.Result{Participant: attempt.Participant, Model: outcome.Model, Verdict: verdict})
		}
	}
}

// refusalPolicy escalates on nothing but a failed attempt or a suspicious primary: no receipt or
// first-token clock starts an attempt.
func refusalPolicy() EscalationPolicy {
	policy := settledPolicy()
	policy.MaxAttemptsPerRequest = 2
	return policy
}

// Test flow:
//  1. Start a race with two hosts queued: one scripted to reject the request as past the model's context length, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert the outcome carries only the rejected attempt, terminalled `TerminalCapabilityRefused`.
//  4. Assert the perf tracker records the context limit for the rejecting host.
//  5. Assert only one pick was made, since a trusted rejection stops the search for another host.
func TestRunRaceStopsEscalatingOnceATrustedHostRejectsTheContextLength(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.host(100, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: too-long\n\n"}})
	fixture.host(101, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(101)},
		confirmed: true,
		finished:  true,
	})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported
	if len(outcome.Attempts) != 1 || outcome.Attempts[0].Terminal != TerminalCapabilityRefused {
		t.Fatalf("attempts = %+v, want only the refused one", outcome.Attempts)
	}

	fixture.perf.mu.Lock()
	contextLimits := fixture.perf.limits
	fixture.perf.mu.Unlock()
	if len(contextLimits) != 1 || contextLimits[0] != (contextLimitCall{participant: "host-0", maxTokens: 40960}) {
		t.Fatalf("recorded context limits = %+v, want one for host-0", contextLimits)
	}

	fixture.picker.mu.Lock()
	picks := len(fixture.picker.profiles)
	fixture.picker.mu.Unlock()
	if picks != 1 {
		t.Fatalf("picks = %d, want 1: no other host takes a prompt past the model's context length", picks)
	}
}

// Test flow:
//  1. Start a race for a model known to run 400 000 tokens, with two hosts queued: one scripted to reject the request as past its own 40 960-token limit, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert the race succeeds on the second host, after a second pick, since a longer host could still serve a request that fits the model.
func TestRunRaceKeepsSearchingWhenTheRejectingHostRunsShorterThanTheModel(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.deps.ModelContextLength = 400_000
	fixture.host(100, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: too-long\n\n"}})
	fixture.host(101, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(101)},
		confirmed: true,
		finished:  true,
	})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported
	if !outcome.Succeeded || outcome.WinnerNonce != 101 {
		t.Fatalf("outcome = %+v, want host-1 to win", outcome)
	}

	fixture.picker.mu.Lock()
	picks := len(fixture.picker.profiles)
	fixture.picker.mu.Unlock()
	if picks != 2 {
		t.Fatalf("picks = %d, want 2: a host running the whole model context can take the request", picks)
	}
}

// Test flow:
//  1. Start a race with two hosts: one held until released with content queued, one scripted to reject as past the model's context length immediately.
//  2. Wait for the rejection's context limit to reach the race, then release the held host.
//  3. Assert the race ends with the held host as winner and succeeded, since the rejection only cut short the search for another host, not the attempt already running.
func TestRunRaceLetsARunningAttemptFinishAfterATrustedContextLengthRejection(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 2)
	release := make(chan struct{})
	openRelease := sync.OnceFunc(func() { close(release) })
	t.Cleanup(openRelease)
	fixture.perf.contextLimitRecorded = make(chan struct{}, 1)
	fixture.host(100, 0, "host-0", &hostScript{
		release:   release,
		receipt:   true,
		chunks:    []string{contentChunk(100)},
		confirmed: true,
		finished:  true,
	})
	fixture.host(101, 1, "host-1", &hostScript{receipt: true, chunks: []string{"data: too-long\n\n"}})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, fixture.perf.contextLimitRecorded, "the escalated host's rejection reaching the race")
	openRelease()

	outcome := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	if outcome.WinnerNonce != 100 || !outcome.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want 100/true: the rejection cut short the attempt still running",
			outcome.WinnerNonce, outcome.Succeeded)
	}
}

// Test flow:
//  1. Start a race with a denied host and a picker that parks its rival pick, then queue the denied host to reject as past the model's context length.
//  2. Wait for the rival's pick to reach the scheduler and the rejection to reach the race.
//  3. Queue the rival host and release the parked pick.
//  4. Assert the race ends with the rival as winner and succeeded, since a suspicious host's rejection does not stop its rival.
func TestRunRaceKeepsTheRivalOfASuspiciousHostThatRejectsTheContextLength(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 2)
	fixture.crown.denied["host-0"] = true
	hold := make(chan struct{})
	openHold := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(openHold)
	fixture.picker.hold, fixture.picker.parked = hold, make(chan struct{}, 1)
	fixture.perf.contextLimitRecorded = make(chan struct{}, 1)
	fixture.host(100, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: too-long\n\n"}})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, fixture.picker.parked, "the rival's pick reaching the scheduler")
	waitForValue(t, fixture.perf.contextLimitRecorded, "the suspicious host's rejection reaching the race")
	fixture.host(101, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(101)},
		confirmed: true,
		finished:  true,
	})
	openHold()

	outcome := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	if outcome.WinnerNonce != 101 || !outcome.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want 101/true: the suspicious host's rejection stopped its rival",
			outcome.WinnerNonce, outcome.Succeeded)
	}
}

// Test flow:
//  1. Start a race with one host held until released and a picker that parks the escalation pick the policy triggers immediately.
//  2. Wait for the escalation pick to reach the scheduler, then release the host to send its context-length rejection.
//  3. Assert the race ends without waiting for its parked pick, reporting only the refused attempt.
func TestRunRaceGivesUpThePickInFlightWhenATrustedHostRejectsTheContextLength(t *testing.T) {
	fixture := newRaceFixture(racePolicy(2), 2)
	hold, release := make(chan struct{}), make(chan struct{})
	openRelease := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() { close(hold) })
	t.Cleanup(openRelease)
	fixture.picker.hold, fixture.picker.parked = hold, make(chan struct{}, 1)
	fixture.host(100, 0, "host-0", &hostScript{release: release, receipt: true, chunks: []string{"data: too-long\n\n"}})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, fixture.picker.parked, "the escalation's pick reaching the scheduler")
	openRelease()

	if outcome := waitForValue(t, outcomes, "the race ending without waiting out its pick"); len(outcome.Attempts) != 1 {
		t.Fatalf("attempts = %d, want only the refused one", len(outcome.Attempts))
	}
	waitForValue(t, fixture.reported, "the race's report")
}

// Test flow:
//  1. Start a race with two hosts queued: one scripted to reject the request body as malformed, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert the outcome carries only the rejected attempt, terminalled `TerminalErrorStream`.
//  4. Assert only one pick was made, since every host would reject the same request body.
func TestRunRaceStopsEscalatingOnceATrustedHostRejectsTheRequest(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.host(100, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: malformed-request\n\n"}})
	fixture.host(101, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(101)},
		confirmed: true,
		finished:  true,
	})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported
	if len(outcome.Attempts) != 1 || outcome.Attempts[0].Terminal != TerminalErrorStream {
		t.Fatalf("attempts = %+v, want only the rejected one", outcome.Attempts)
	}

	fixture.picker.mu.Lock()
	picks := len(fixture.picker.profiles)
	fixture.picker.mu.Unlock()
	if picks != 1 {
		t.Fatalf("picks = %d, want 1: every host would reject the same body", picks)
	}
}

// finishWatch reports each attempt the coordinator has begun to judge, which it finishes before it reads a pick's answer.
type finishWatch struct{ finished chan uint64 }

func (w finishWatch) RecordStep(step RaceStep) {
	if step.Kind == RaceStepAttemptFinished {
		w.finished <- step.Nonce
	}
}

func (finishWatch) HostDeniedCrown(string, string, int) {}

func (finishWatch) HostCrownedAgain(string, string) {}

// Test flow:
//  1. Start a race with a denied host and a picker that parks its rival pick, using a journal that reports each attempt's finish, then queue the denied host to reject the request as malformed.
//  2. Wait for the rival's pick to reach the scheduler and the journal to report the denied host's attempt finished.
//  3. Queue the rival host and release the parked pick.
//  4. Assert the race ends with the rival as winner and succeeded.
func TestRunRaceKeepsTheRivalOfASuspiciousHostThatRejectsTheRequest(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 2)
	fixture.crown.denied["host-0"] = true
	hold := make(chan struct{})
	openHold := sync.OnceFunc(func() { close(hold) })
	t.Cleanup(openHold)
	fixture.picker.hold, fixture.picker.parked = hold, make(chan struct{}, 1)
	watch := finishWatch{finished: make(chan uint64, 2)}
	fixture.deps.Journal = watch
	fixture.host(100, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: malformed-request\n\n"}})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := fixture.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	waitForValue(t, fixture.picker.parked, "the rival's pick reaching the scheduler")
	if finished := waitForValue(t, watch.finished, "the suspicious host's rejection reaching the race"); finished != 100 {
		t.Fatalf("finished attempt = %d, want the suspicious host's 100", finished)
	}
	fixture.host(101, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(101)},
		confirmed: true,
		finished:  true,
	})
	openHold()

	outcome := waitForValue(t, outcomes, "the race ending")
	waitForValue(t, fixture.reported, "the race's report")
	if outcome.WinnerNonce != 101 || !outcome.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want 101/true: the suspicious host's rejection stopped its rival",
			outcome.WinnerNonce, outcome.Succeeded)
	}
}

// Test flow:
//  1. Start a race with two hosts: one scripted to fail with a connection reset, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert the outcome carries both the failed attempt and its escalation, with the escalation as winner and the race succeeded.
//  4. Assert the escalation's start reason is the failed-attempt stage.
func TestRunRaceEscalatesAfterItsLastAttemptFailed(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.host(110, 0, "host-0", &hostScript{receipt: true, err: errors.New("connection reset")})
	fixture.host(111, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(111)},
		confirmed: true,
		finished:  true,
	})

	outcome, err := fixture.run(context.Background())
	if err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported
	if len(outcome.Attempts) != 2 {
		t.Fatalf("attempts = %d, want the failed one and its escalation", len(outcome.Attempts))
	}
	if outcome.WinnerNonce != 111 || !outcome.Succeeded {
		t.Fatalf("winner = %d succeeded = %v, want 111/true", outcome.WinnerNonce, outcome.Succeeded)
	}
	if outcome.Attempts[1].StartReason != StageAttemptFailed.Reason() {
		t.Fatalf("escalation start reason = %q, want %q", outcome.Attempts[1].StartReason, StageAttemptFailed.Reason())
	}
}

// Test flow:
//  1. Start a race with two hosts: one scripted to fail with a connection reset, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert two picks were made, the primary and its escalation.
//  4. Assert the escalation's pick excludes the host already dispatched to, even though its failure said nothing about its capabilities.
func TestRunRaceExcludesEveryHostItAlreadyDispatchedTo(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.host(120, 0, "host-0", &hostScript{receipt: true, err: errors.New("connection reset")})
	fixture.host(121, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(121)},
		confirmed: true,
		finished:  true,
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported

	fixture.picker.mu.Lock()
	profiles := fixture.picker.profiles
	fixture.picker.mu.Unlock()
	if len(profiles) != 2 {
		t.Fatalf("picks = %d, want the primary and its escalation", len(profiles))
	}
	if len(profiles[1].Exclude) != 1 || profiles[1].Exclude[0] != "host-0" {
		t.Fatalf("escalation exclusions = %v, want the host already dispatched to", profiles[1].Exclude)
	}
}

// Test flow:
//  1. Start a race with two hosts: one scripted to fail with a connection reset, one scripted to answer with content.
//  2. Run the race to completion.
//  3. Assert only the escalation's pick is marked an escalation, pinned to the escrow the primary was served from.
func TestOnlyARacesEscalationPickIsMarkedAnEscalation(t *testing.T) {
	fixture := newRaceFixture(refusalPolicy(), 3)
	fixture.host(122, 0, "host-0", &hostScript{receipt: true, err: errors.New("connection reset")})
	fixture.host(123, 1, "host-1", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(123)},
		confirmed: true,
		finished:  true,
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	<-fixture.reported

	fixture.picker.mu.Lock()
	profiles := fixture.picker.profiles
	fixture.picker.mu.Unlock()
	if len(profiles) != 2 {
		t.Fatalf("picks = %d, want the primary and its escalation", len(profiles))
	}
	if profiles[0].Escalation || !profiles[1].Escalation || profiles[1].Escrow != "escrow-1" {
		t.Fatalf("profiles = %+v, want only the second marked an escalation on escrow-1", profiles)
	}
}

// Test flow:
//  1. Start a race whose request is pinned to a specific escrow.
//  2. Run the race to completion.
//  3. Assert the first pick asked the scheduler for that pinned escrow.
func TestARacePinnedToAnEscrowAsksTheSchedulerForThatOne(t *testing.T) {
	fixture := newRaceFixture(racePolicy(1), 2)
	fixture.request.Escrow = "escrow-pinned"
	fixture.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentChunk(10)},
		confirmed: true,
		finished:  true,
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	fixture.picker.mu.Lock()
	defer fixture.picker.mu.Unlock()
	if len(fixture.picker.profiles) == 0 {
		t.Fatal("the race made no pick")
	}
	if got := fixture.picker.profiles[0].Escrow; got != "escrow-pinned" {
		t.Fatalf("first pick asked for escrow %q, want %q", got, "escrow-pinned")
	}
}

// Test flow:
//  1. Start a coordinator with one cancelled attempt and no winner, then mark the race cancelled.
//  2. Assemble the outcome.
//  3. Assert the attempt's terminal is rewritten to `TerminalStalled`, since nobody won and this host answered nothing.
func TestABackstopCancelWithNoWinnerIsNotACancelledLoser(t *testing.T) {
	base := time.Now()
	hung := &liveAttempt{
		nonce:   1,
		done:    true,
		outcome: &AttemptOutcome{Nonce: 1, Terminal: TerminalClientCancelled, SendTime: base},
	}

	coordinator := stalledFixtureCoordinator(settledPolicy(), hung)
	coordinator.cancelled = true
	outcome := coordinator.outcome()

	if got := outcome.Attempts[0].Terminal; got != TerminalStalled {
		t.Fatalf("terminal = %v, want %v: nobody won, so this host answered nothing", got, TerminalStalled)
	}
}

// Test flow:
//  1. Start a coordinator with a losing attempt and a crowned winner, then mark the race cancelled.
//  2. Assemble the outcome.
//  3. Assert the loser's terminal stays `TerminalClientCancelled`, since it lost the race rather than failing one.
func TestABackstopCancelAfterAWinnerLeavesLosersExempt(t *testing.T) {
	base := time.Now()
	loser := &liveAttempt{
		nonce:   1,
		done:    true,
		outcome: &AttemptOutcome{Nonce: 1, Terminal: TerminalClientCancelled, SendTime: base},
	}
	winner := &liveAttempt{
		nonce:         2,
		done:          true,
		nonceFinished: true,
		outcome:       &AttemptOutcome{Nonce: 2, Terminal: TerminalLost, ContentChunks: 2, SendTime: base},
	}

	coordinator := stalledFixtureCoordinator(settledPolicy(), loser, winner)
	coordinator.winner = winner
	coordinator.cancelled = true
	outcome := coordinator.outcome()

	if got := outcome.Attempts[0].Terminal; got != TerminalClientCancelled {
		t.Fatalf("terminal = %v, want %v: this host lost a race, it did not fail one", got, TerminalClientCancelled)
	}
}

// Test flow:
//  1. Start a coordinator with one attempt handed off after the client left, then mark the race cancelled.
//  2. Assemble the outcome.
//  3. Assert the attempt's terminal stays `TerminalClientCancelled`, since the client left rather than the host failing.
func TestABackstopCancelAfterTheClientLeftLeavesHostsExempt(t *testing.T) {
	base := time.Now()
	draining := &liveAttempt{
		nonce:   1,
		done:    true,
		outcome: &AttemptOutcome{Nonce: 1, Terminal: TerminalClientCancelled, SendTime: base},
	}

	coordinator := stalledFixtureCoordinator(settledPolicy(), draining)
	coordinator.handedOff = true
	coordinator.cancelled = true
	outcome := coordinator.outcome()

	if got := outcome.Attempts[0].Terminal; got != TerminalClientCancelled {
		t.Fatalf("terminal = %v, want %v: the client left, the hosts did not fail", got, TerminalClientCancelled)
	}
}

// Test flow:
//  1. Call `racedTerminal` on a stalled attempt with content chunks and a cancelled outcome.
//  2. Assert it is rewritten to `TerminalStalled`.
//  3. Call `racedTerminal` on a non-stalled attempt with a cancelled outcome carrying no content.
//  4. Assert the cancellation is left unchanged.
func TestRacedTerminalPromotesAStalledHostForEveryReader(t *testing.T) {
	t.Parallel()
	coordinator := &raceCoordinator{}
	attempt := &liveAttempt{stalled: true}
	hung := AttemptOutcome{Terminal: TerminalClientCancelled, ContentChunks: 3}

	if got := coordinator.racedTerminal(attempt, hung); got != TerminalStalled {
		t.Fatalf("racedTerminal = %v, want stalled -- the log must name what the ladders name", got)
	}

	cancelled := AttemptOutcome{Terminal: TerminalClientCancelled}
	if got := coordinator.racedTerminal(&liveAttempt{}, cancelled); got != TerminalClientCancelled {
		t.Fatalf("racedTerminal = %v, want the cancellation left alone", got)
	}
}

// Test flow:
//  1. Start a coordinator with one attempt whose goroutine never reported and one answered, crowned attempt.
//  2. Assemble the outcome.
//  3. Assert the unreported attempt is kept in the outcome, named and terminalled `TerminalUnclassified`.
//  4. Assert the timeout plan still posts a vote for that attempt's committed nonce.
func TestAnUnreportedAttemptStaysInTheOutcome(t *testing.T) {
	t.Parallel()
	base := raceStart
	silent := &liveAttempt{nonce: 557, participant: "host-silent", sendTime: base}
	answered := &liveAttempt{
		nonce:         558,
		done:          true,
		nonceFinished: true,
		outcome:       &AttemptOutcome{Nonce: 558, Terminal: TerminalLost, ContentChunks: 2, SendTime: base},
	}

	coordinator := stalledFixtureCoordinator(settledPolicy(), silent, answered)
	coordinator.winner = answered
	outcome := coordinator.outcome()

	if len(outcome.Attempts) != 2 {
		t.Fatalf("attempts = %d, want the silent one kept: its nonce was committed", len(outcome.Attempts))
	}
	var kept AttemptOutcome
	for _, attempt := range outcome.Attempts {
		if attempt.Nonce == 557 {
			kept = attempt
		}
	}
	if kept.Participant != "host-silent" || kept.Terminal != TerminalUnclassified {
		t.Fatalf("silent attempt = %+v, want it named and unclassified", kept)
	}
	if plan := outcome.TimeoutPlan(); len(plan) != 1 || plan[0].Nonce != 557 || !plan[0].Post {
		t.Fatalf("TimeoutPlan() = %+v, want a posted vote for 557", plan)
	}
}

// Test flow:
//  1. Detach a race context from cancellation and derive a pick context from it with the scheduler's pick timeout.
//  2. Assert the race context carries no deadline.
//  3. Assert the pick context carries a deadline no further out than the scheduler's pick timeout.
func TestEveryPickIsBounded(t *testing.T) {
	t.Parallel()
	raceCtx := context.WithoutCancel(t.Context())

	pickCtx, cancel := context.WithTimeout(raceCtx, schedulerPickTimeout)
	defer cancel()

	if _, hasDeadline := raceCtx.Deadline(); hasDeadline {
		t.Fatal("the race context gained a deadline: a departed client would stop nonces from settling")
	}
	deadline, hasDeadline := pickCtx.Deadline()
	if !hasDeadline {
		t.Fatal("the pick context carries no deadline, so a waiting scheduler would hang it forever")
	}
	if remaining := time.Until(deadline); remaining > schedulerPickTimeout {
		t.Fatalf("pick deadline is %v away, want at most %v", remaining, schedulerPickTimeout)
	}
}

// Test flow:
//  1. Start a race with one host held until released, then cancel the client context once the attempt has arrived.
//  2. Assert `runRace` returns `context.Canceled` to the departed client before releasing the host to answer.
//  3. Release the host so it still crowns a winner after the client left.
//  4. Assert the report records the client as gone.
//  5. Assert the winning attempt's visibility label is `VisibilityWinnerClientGone`, not the ordinary winner label.
func TestAWinnerCrownedAfterTheClientLeftIsNotLabelledUserVisible(t *testing.T) {
	fixture := newRaceFixture(racePolicy(1), 1)
	fixture.deps.DrainTimeout = time.Minute
	fixture.clock.step = time.Second
	arrive := make(chan uint64, 1)
	release := make(chan struct{})
	openRelease := sync.OnceFunc(func() { close(release) })
	t.Cleanup(openRelease)
	fixture.host(50, 0, "host-0", &hostScript{
		arrive:    arrive,
		release:   release,
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("answered anyway")},
		confirmed: true,
		finished:  true,
	})

	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() {
		_, err := fixture.run(ctx)
		returned <- err
	}()

	<-arrive
	cancel()
	if err := waitForValue(t, returned, "the race answering the departed client"); !errors.Is(err, context.Canceled) {
		t.Fatalf("runRace error = %v, want context.Canceled", err)
	}
	openRelease()

	reported := <-fixture.reported

	if !reported.Lifecycle.ClientGone {
		t.Fatal("the outcome does not record that the client left, so nothing downstream can know")
	}
	for _, attempt := range reported.Attempts {
		if !reported.IsWinner(attempt) {
			continue
		}
		if got := reported.Labels(attempt).Visibility; got != VisibilityWinnerClientGone {
			t.Fatalf("winner visibility = %q, want %q", got, VisibilityWinnerClientGone)
		}
	}
}

// Test flow:
//  1. Start a race with one host that answers with content while the client stays.
//  2. Run the race to completion.
//  3. Assert the report does not record the client as gone.
//  4. Assert the winning attempt's visibility label is the ordinary `VisibilityWinner`.
func TestAWinnerServedToAWaitingClientStaysUserVisible(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(60, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, contentEvent("delivered")},
		confirmed: true,
		finished:  true,
	})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported

	if reported.Lifecycle.ClientGone {
		t.Fatal("the client never left, so the outcome must not say it did")
	}
	for _, attempt := range reported.Attempts {
		if !reported.IsWinner(attempt) {
			continue
		}
		if got := reported.Labels(attempt).Visibility; got != VisibilityWinner {
			t.Fatalf("winner visibility = %q, want %q", got, VisibilityWinner)
		}
	}
}

// Test flow:
//  1. Start a race with a single host whose attempt fails with `ErrLocalRootUnavailable`, the gateway's own store failing it.
//  2. Run the race to completion.
//  3. Assert the attempt is flagged a local fault, not a divergence, and exempt from the host's sample.
//  4. Assert the host was neither marked diverged, nor blocked, nor rewound.
func TestRunRaceLeavesAHostAloneWhenTheLocalRootIsUnavailable(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(80, 0, "host-0", &hostScript{receipt: true, err: fmt.Errorf("apply: %w", ErrLocalRootUnavailable)})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported
	attempt := reported.Attempts[0]
	if !attempt.LocalFault || attempt.StateDivergent {
		t.Fatalf("LocalFault = %t, StateDivergent = %t, want true and false", attempt.LocalFault, attempt.StateDivergent)
	}
	if _, exemption := reported.Sample(attempt); exemption != ExemptLocalFault {
		t.Fatalf("Sample exemption = %v, want ExemptLocalFault", exemption)
	}
	fixture.picker.mu.Lock()
	blocked, diverged := fixture.picker.blocked, fixture.picker.diverged
	fixture.picker.mu.Unlock()
	fixture.target.mu.Lock()
	rewound := fixture.target.rewound
	fixture.target.mu.Unlock()
	if len(diverged) != 0 || len(blocked) != 0 || len(rewound) != 0 {
		t.Fatalf("diverged = %v, blocked = %v, rewound = %v, want none: the fault is the gateway's own", diverged, blocked, rewound)
	}
}

// Test flow:
//  1. Start a race with a single host whose send fails with `user.ErrHostAbsentFromChain`, the dead slot a session opens for a participant the chain did not list.
//  2. Run the race to completion.
//  3. Assert the attempt is flagged a local fault and moves neither the host's congestion verdict nor its sample.
//  4. Assert the picker was told the host is absent on this escrow, and the host was not marked diverged or blocked.
func TestRunRaceBlocksAnAbsentSlotOnItsEscrowWithoutJudgingTheHost(t *testing.T) {
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.host(80, 0, "host-0", &hostScript{err: fmt.Errorf("host host-0: %w", user.ErrHostAbsentFromChain)})

	if _, err := fixture.run(context.Background()); err != nil {
		t.Fatalf("runRace error = %v", err)
	}
	reported := <-fixture.reported
	attempt := reported.Attempts[0]
	if !attempt.LocalFault || !attempt.HostAbsent {
		t.Fatalf("LocalFault = %t, HostAbsent = %t, want true and true", attempt.LocalFault, attempt.HostAbsent)
	}
	if verdict, moves := reported.Verdict(attempt); moves {
		t.Fatalf("Verdict = %v, true, want no verdict: the host was never contacted", verdict)
	}
	if _, exemption := reported.Sample(attempt); exemption != ExemptLocalFault {
		t.Fatalf("Sample exemption = %v, want ExemptLocalFault", exemption)
	}
	fixture.picker.mu.Lock()
	absent, blocked, diverged := fixture.picker.absent, fixture.picker.blocked, fixture.picker.diverged
	fixture.picker.mu.Unlock()
	if len(absent) != 1 || absent[0] != [2]string{"escrow-1", "host-0"} {
		t.Fatalf("absent hosts = %v, want [[escrow-1 host-0]]", absent)
	}
	if len(diverged) != 0 || len(blocked) != 0 {
		t.Fatalf("diverged = %v, blocked = %v, want none", diverged, blocked)
	}
}

// Test flow:
//  1. Start a race with a single host that receipts and then ignores cancellation, the way a read from a dead host can, using a clock that jumps 30 minutes on every read.
//  2. Run the race and wait for its report, bounded in real time.
//  3. Assert the race reported once the cancelled attempt outlived its grace, with that attempt left unclassified so its open nonce still takes a timeout vote.
func TestARaceReportsWhenACancelledAttemptNeverReturns(t *testing.T) {
	deaf := make(chan struct{})
	t.Cleanup(func() { close(deaf) })
	fixture := newRaceFixture(settledPolicy(), 1)
	fixture.deps.CancelGrace = 10 * time.Millisecond
	fixture.host(60, 0, "host-0", &hostScript{receipt: true, deaf: deaf})
	fixture.clock.step = 30 * time.Minute

	go func() { _, _ = fixture.run(context.Background()) }()
	var reported RaceOutcome
	select {
	case reported = <-fixture.reported:
	case <-time.After(10 * time.Second):
		t.Fatal("runRace() reported = nothing after 10s, want a report once the cancel grace expires: an attempt that never returns would hold the race and its escrow forever")
	}

	if len(reported.Attempts) != 1 || reported.Attempts[0].Terminal != TerminalUnclassified {
		t.Fatalf("runRace() reported attempts = %+v, want one unclassified attempt", reported.Attempts)
	}
	if steps := reported.TimeoutPlan(); len(steps) != 1 || !steps[0].Post {
		t.Fatalf("TimeoutPlan() = %+v, want one posted vote for the abandoned nonce", steps)
	}
}
