package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/limits"
	"devshard/cmd/gateway/perf"
	"devshard/cmd/gateway/scheduler"
)

// virtualTime is a fake clock and timer in one, so a deadline fires only when a test advances it.
type virtualTime struct {
	mu       sync.Mutex
	step     time.Duration
	now      time.Time
	deadline time.Time
	armed    bool
	fired    chan time.Time
	arms     chan time.Duration
}

func newVirtualTime() *virtualTime {
	return &virtualTime{
		now:   testEpoch,
		fired: make(chan time.Time, 1),
		arms:  make(chan time.Duration, 256),
	}
}

func (v *virtualTime) Now() time.Time {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.now = v.now.Add(v.step)
	return v.now
}

func (v *virtualTime) Reset(delay time.Duration) {
	select {
	case v.arms <- delay:
	default:
	}
	v.mu.Lock()
	v.deadline, v.armed = v.now.Add(delay), true
	due := !v.deadline.After(v.now)
	v.mu.Unlock()
	v.discard()
	if due {
		v.ring()
	}
}

func (v *virtualTime) Stop() {
	v.mu.Lock()
	v.armed = false
	v.mu.Unlock()
	v.discard()
}

func (v *virtualTime) Fired() <-chan time.Time { return v.fired }

func (v *virtualTime) advance(step time.Duration) {
	v.mu.Lock()
	v.now = v.now.Add(step)
	due := v.armed && !v.deadline.After(v.now)
	v.mu.Unlock()
	if due {
		v.ring()
	}
}

// waitArmed blocks until a deadline no further out than limit is armed.
func (v *virtualTime) waitArmed(t *testing.T, limit time.Duration) {
	t.Helper()
	for {
		select {
		case delay := <-v.arms:
			if delay > 0 && delay <= limit {
				return
			}
		case <-time.After(10 * time.Second):
			t.Fatal("no deadline was armed within the expected range")
			return
		}
	}
}

func (v *virtualTime) ring() {
	select {
	case v.fired <- time.Time{}:
	default:
	}
}

func (v *virtualTime) discard() {
	select {
	case <-v.fired:
	default:
	}
}

// simSnapshots lets a test move the chain phase mid-attempt.
type simSnapshots struct {
	current atomic.Pointer[chain.PhaseSnapshot]
}

func newSimSnapshots(initial chain.PhaseSnapshot) *simSnapshots {
	snapshots := &simSnapshots{}
	snapshots.current.Store(&initial)
	return snapshots
}

func (s *simSnapshots) Snapshot() chain.PhaseSnapshot { return *s.current.Load() }

func (s *simSnapshots) set(next chain.PhaseSnapshot) { s.current.Store(&next) }

// simTracker records what the engine reported and, when health is set, answers ejection and degradation from a real perf.Tracker.
type simTracker struct {
	*stubPerf
	health  *perf.Tracker
	mu      sync.Mutex
	samples []perf.Sample
}

func (p *simTracker) RecordSample(sample perf.Sample) {
	p.mu.Lock()
	p.samples = append(p.samples, sample)
	p.mu.Unlock()
	if p.health != nil {
		p.health.RecordSample(sample)
	}
}

func (p *simTracker) Ejected(participant, model string) bool {
	if p.health != nil {
		return p.health.Ejected(participant, model)
	}
	return p.stubPerf.Ejected(participant, model)
}

func (p *simTracker) Degraded(participant, model string) bool {
	if p.health != nil {
		return p.health.Degraded(participant, model)
	}
	return p.stubPerf.Degraded(participant, model)
}

func (p *simTracker) recorded() []perf.Sample {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]perf.Sample(nil), p.samples...)
}

type windowMove struct {
	participant string
	verdict     limits.Verdict
}

type simWindows struct {
	*slotLedger
	mu    sync.Mutex
	moves []windowMove
}

func (w *simWindows) OnResult(result limits.Result) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.moves = append(w.moves, windowMove{participant: result.Participant, verdict: result.Verdict})
}

func (w *simWindows) recorded() []windowMove {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]windowMove(nil), w.moves...)
}

type simMetrics struct {
	races     chan RaceOutcome
	timeouts  chan TimeoutEvent
	mu        sync.Mutex
	overflows []string
}

func newSimMetrics() *simMetrics {
	return &simMetrics{
		races:    make(chan RaceOutcome, 4),
		timeouts: make(chan TimeoutEvent, 16),
	}
}

func (m *simMetrics) RecordRace(outcome RaceOutcome) { m.races <- outcome }

func (m *simMetrics) RecordAwaitedFinish(RaceOutcome, AttemptOutcome) {}

func (m *simMetrics) RecordTimeout(event TimeoutEvent) { m.timeouts <- event }

func (m *simMetrics) RecordClassifyOverflow(participant, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.overflows = append(m.overflows, participant)
}

func (m *simMetrics) classifyOverflows() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.overflows...)
}

type simLedger struct{ rows chan RaceOutcome }

func newSimLedger() *simLedger { return &simLedger{rows: make(chan RaceOutcome, 4)} }

func (l *simLedger) RecordRequest(outcome RaceOutcome) { l.rows <- outcome }

// simDispatchParams stands in for the concrete params the caller builds.
type simDispatchParams struct{ prompt string }

// simPoster stands in for the chain vote poster.
type simPoster struct {
	entered chan uint64
	release chan struct{}
	vote    string
	err     error

	mu       sync.Mutex
	posted   []uint64
	resolved []any
}

func (p *simPoster) resolvedWith(params any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.resolved = append(p.resolved, params)
}

func (p *simPoster) paramsSeen() []any {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]any(nil), p.resolved...)
}

func newSimPoster() *simPoster {
	return &simPoster{entered: make(chan uint64, 8), vote: TimeoutKindExecution}
}

func (p *simPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *simPoster) SettleTimeout(_ context.Context, step TimeoutStep) (TimeoutVote, error) {
	p.entered <- step.Nonce
	if p.release != nil {
		<-p.release
	}
	p.mu.Lock()
	p.posted = append(p.posted, step.Nonce)
	p.mu.Unlock()
	return TimeoutVote{Kind: p.vote}, p.err
}

func (p *simPoster) settled() []uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]uint64(nil), p.posted...)
}

// simulator wires an Engine to test doubles for each of its dependencies.
type simulator struct {
	engine                  *Engine
	picker                  *stubPicker
	target                  *simTargets
	perf                    *simTracker
	windows                 *simWindows
	metrics                 *simMetrics
	ledger                  *simLedger
	poster                  *simPoster
	snapshots               *simSnapshots
	clock                   *virtualTime
	client                  *recordingSink
	model                   string
	emptyAnswerCountsTokens bool
	pinned                  map[string]bool
}

func engineSettings(policy EscalationPolicy, modes config.Modes) *config.Config {
	return &config.Config{
		Modes: modes,
		Stream: config.Stream{
			DrainTimeoutSeconds:         2_400,
			ClassifyMaxAttemptBytes:     1 << 20,
			ClassifyMaxParticipantBytes: 10 << 20,
			ClassifyMaxGlobalBytes:      100 << 20,
		},
		Engine: config.Engine{
			ReceiptTimeoutMS:      policy.ReceiptTimeout.Milliseconds(),
			FirstTokenFloorMS:     policy.FirstTokenFloor.Milliseconds(),
			InterChunkStallMS:     policy.InterChunkStall.Milliseconds(),
			LoserGraceMS:          policy.LoserGrace.Milliseconds(),
			MaxAttemptsPerRequest: int64(policy.MaxAttemptsPerRequest),
		},
	}
}

func newSimulator(t *testing.T, policy EscalationPolicy, hosts int, model string) *simulator {
	t.Helper()
	return newSimulatorInPhase(t, policy, hosts, model, config.Modes{}, chain.PhaseSnapshot{})
}

func newSimulatorInPhase(
	t *testing.T,
	policy EscalationPolicy,
	hosts int,
	model string,
	modes config.Modes,
	phase chain.PhaseSnapshot,
) *simulator {
	t.Helper()
	sim := &simulator{
		picker:    &stubPicker{},
		target:    &simTargets{scriptedTarget: &scriptedTarget{scripts: map[uint64]*hostScript{}, hosts: hosts, labels: map[int]string{}}},
		perf:      &simTracker{stubPerf: &stubPerf{ejected: map[string]bool{}, degraded: map[string]bool{}}},
		windows:   &simWindows{slotLedger: newSlotLedger()},
		metrics:   newSimMetrics(),
		ledger:    newSimLedger(),
		poster:    newSimPoster(),
		snapshots: newSimSnapshots(phase),
		clock:     newVirtualTime(),
		client:    &recordingSink{},
		model:     model,
		pinned:    map[string]bool{},
	}
	engine, err := NewEngine(Deps{
		Picker:     sim.picker,
		Targets:    sim.target,
		Windows:    sim.windows,
		Perf:       sim.perf,
		Snapshots:  sim.snapshots,
		Config:     config.NewHolder(engineSettings(policy, modes)),
		Metrics:    sim.metrics,
		Ledger:     sim.ledger,
		Suspicious: func(participant string) bool { return sim.pinned[participant] },
		Timeouts: func(_ string, params any) (TimeoutPoster, bool) {
			sim.poster.resolvedWith(params)
			return sim.poster, true
		},
		Now:   sim.clock.Now,
		Timer: func() raceTimer { return sim.clock },
	})
	if err != nil {
		t.Fatalf("NewEngine() = %v, want a running engine", err)
	}
	sim.engine = engine
	t.Cleanup(sim.engine.Stop)
	return sim
}

func (s *simulator) host(nonce uint64, hostIdx int, participant string, script *hostScript) {
	s.target.mu.Lock()
	s.target.scripts[nonce] = script
	s.target.labels[hostIdx] = "label-" + participant
	s.target.mu.Unlock()
	s.picker.mu.Lock()
	s.picker.queue = append(s.picker.queue, scheduler.Assignment{
		Escrow:   "escrow-1",
		Host:     participant,
		Nonce:    fakePrepared{nonce: nonce, hostIdx: hostIdx},
		HostSlot: s.windows.hostSlot(participant),
	})
	s.picker.mu.Unlock()
}

func (s *simulator) profile() Request {
	return Request{
		RequestID:               "request-1",
		Model:                   s.model,
		EmptyAnswerCountsTokens: s.emptyAnswerCountsTokens,
		InputTokens:             1_000,
		InputTokensFromBytes:    1_000,
		OutputTokens:            256,
		Params:                  simDispatchParams{prompt: "hello"},
	}
}

// parkVotes holds every vote inside the poster and returns the gate that releases them.
func (s *simulator) parkVotes(t *testing.T) func() {
	t.Helper()
	s.poster.release = make(chan struct{})
	open := sync.OnceFunc(func() { close(s.poster.release) })
	t.Cleanup(open)
	return open
}

func (s *simulator) run(ctx context.Context) (RaceOutcome, error) {
	return s.engine.Run(ctx, s.profile(), s.client)
}

// reported is the race's single outcome, taken from the one point that records it.
func (s *simulator) reported(t *testing.T) RaceOutcome {
	t.Helper()
	select {
	case outcome := <-s.metrics.races:
		return outcome
	case <-time.After(10 * time.Second):
		t.Fatal("the race never reported an outcome")
		return RaceOutcome{}
	}
}

func (s *simulator) assertOneOutcome(t *testing.T) {
	t.Helper()
	if extra := len(s.metrics.races); extra != 0 {
		t.Fatalf("outcomes reported = %d, want 1", 1+extra)
	}
}

func (s *simulator) assertNoSlotLeaked(t *testing.T, attempts int) {
	t.Helper()
	for range attempts {
		select {
		case <-s.windows.releases:
		case <-time.After(10 * time.Second):
			t.Fatal("an attempt never released its host slot")
		}
	}
	acquired, released := s.perf.counts()
	if acquired != attempts || released != attempts {
		t.Fatalf("perf acquire/release = %d/%d, want %d/%d", acquired, released, attempts, attempts)
	}
}

// settleAll waits out the votes the race left owing.
func (s *simulator) settleAll() { s.engine.Stop() }

func (s *simulator) timeoutEvents() []TimeoutEvent {
	events := make([]TimeoutEvent, 0, len(s.metrics.timeouts))
	for range cap(s.metrics.timeouts) {
		select {
		case event := <-s.metrics.timeouts:
			events = append(events, event)
		default:
			return events
		}
	}
	return events
}

func attemptFor(t *testing.T, outcome RaceOutcome, nonce uint64) AttemptOutcome {
	t.Helper()
	for _, attempt := range outcome.Attempts {
		if attempt.Nonce == nonce {
			return attempt
		}
	}
	t.Fatalf("outcome has no attempt for nonce %d", nonce)
	return AttemptOutcome{}
}

// speculativePolicy escalates only when an attempt has finished.
func speculativePolicy(maxAttempts int) EscalationPolicy {
	policy := settledPolicy()
	policy.MaxAttemptsPerRequest = maxAttempts
	return policy
}

func contentEvent(text string) string {
	return fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", text)
}

const roleEvent = `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"

// Test flow:
//  1. Start a one-host simulator and script host-0 to stream the content_stream.sse fixture across two chunks with a receipt and a finished nonce.
//  2. Run the race and assert it returns no error.
//  3. Assert the outcome succeeded with host-0's nonce as the winner.
//  4. Assert the client received the fixture bytes unchanged.
//  5. Assert the winning attempt is TerminalWon with content source delta.content.
//  6. Assert one Success window move, one responsive perf sample, no classify overflows, no timeout events, no leaked slot, and exactly one reported outcome.
func TestSimulatorStreamsAHealthyRequestByteForByte(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.clock.step = time.Millisecond
	body := string(readFixture(t, "content_stream.sse"))
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{body[:40], body[40:]},
		confirmed: true,
		finished:  true,
	})

	outcome, err := sim.run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !outcome.Succeeded || outcome.WinnerNonce != 10 {
		t.Fatalf("outcome = winner %d succeeded %v, want winner 10 succeeded true", outcome.WinnerNonce, outcome.Succeeded)
	}
	if got := string(sim.client.forwarded()); got != body {
		t.Fatalf("client stream differs from the host stream:\n got %q\nwant %q", got, body)
	}
	reported := sim.reported(t)
	winner := attemptFor(t, reported, 10)
	if winner.Terminal != TerminalWon || winner.ContentSource != "delta.content" {
		t.Fatalf("winner = %v/%q, want TerminalWon/delta.content", winner.Terminal, winner.ContentSource)
	}
	if moves := sim.windows.recorded(); len(moves) != 1 || moves[0].verdict != limits.Success {
		t.Fatalf("window moves = %+v, want one Success", moves)
	}
	if samples := sim.perf.recorded(); len(samples) != 1 || !samples[0].Responsive {
		t.Fatalf("samples = %+v, want one responsive sample", samples)
	}
	if overflows := sim.metrics.classifyOverflows(); len(overflows) != 0 {
		t.Errorf("classify overflows = %v, want none for a stream inside every cap", overflows)
	}
	if events := sim.timeoutEvents(); len(events) != 0 {
		t.Fatalf("timeout events = %+v, want none for a settled nonce", events)
	}
	sim.assertNoSlotLeaked(t, 1)
	sim.assertOneOutcome(t)
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to stream two content chunks, flushing on each write.
//  2. Run the race, assert it returns no error, and read the reported outcome.
//  3. Assert the client's sink recorded one flush per streamed chunk.
func TestSimulatorFlushesEveryChunkThroughToTheClient(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("first"), contentEvent("second")},
		flush:     true,
		confirmed: true,
		finished:  true,
	})

	if _, err := sim.run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	sim.reported(t)

	if got := sim.client.flushed(); got != 2 {
		t.Fatalf("client flushes = %d, want one per streamed chunk", got)
	}
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to reply with a 400 "model does not exist" error event.
//  2. Run the race and assert the returned error is a HostApplicationError.
//  3. Assert the error's payload is the host's own refusal body and its HTTP status is 400.
//  4. Read the reported outcome.
func TestSimulatorForwardsTheHostsRefusalVerbatim(t *testing.T) {
	const refusal = `{"error":{"code":400,"message":"model does not exist","type":"BadRequestError"}}`
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:  true,
		chunks:   []string{"data: " + refusal + "\n\n"},
		finished: true,
	})

	_, err := sim.run(context.Background())

	var hostErr *HostApplicationError
	if !errors.As(err, &hostErr) {
		t.Fatalf("Run() error = %v, want a host application error", err)
	}
	if got := hostErr.Payload; got != refusal {
		t.Fatalf("payload = %s, want the host's own error event %s", got, refusal)
	}
	if hostErr.HTTPStatus() != 400 {
		t.Fatalf("status = %d, want the 400 the host reported", hostErr.HTTPStatus())
	}
	sim.reported(t)
}

// Test flow:
//  1. Start a two-host simulator racing an empty-answering host against a content-producing host, both held at arrive/release so they contend for the crown together.
//  2. Release both hosts at once and run the race to completion.
//  3. Assert the content-producing host's nonce wins and the empty host's attempt is TerminalEmptyStream.
//  4. Assert the client received only the content host's bytes, and exactly one outcome was reported.
func TestSimulatorContentProducerWinsOverAnEmptyStream(t *testing.T) {
	sim := newSimulator(t, racePolicy(2), 2, qwenModel)
	arrive, release := make(chan uint64, 2), make(chan struct{})
	sim.host(10, 0, "empty-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, confirmed: true, finished: true,
	})
	sim.host(11, 1, "content-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, chunks: []string{roleEvent, contentEvent("hello")},
		confirmed: true, finished: true,
	})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := sim.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	<-arrive
	<-arrive
	close(release)
	<-outcomes

	reported := sim.reported(t)
	if reported.WinnerNonce != 11 || !reported.Succeeded {
		t.Fatalf("winner = %d succeeded %v, want 11/true", reported.WinnerNonce, reported.Succeeded)
	}
	if empty := attemptFor(t, reported, 10); empty.Terminal != TerminalEmptyStream {
		t.Fatalf("empty host terminal = %v, want TerminalEmptyStream", empty.Terminal)
	}
	if got, want := string(sim.client.forwarded()), roleEvent+contentEvent("hello"); got != want {
		t.Fatalf("client stream = %q, want %q", got, want)
	}
	sim.assertOneOutcome(t)
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to emit a single SSE error event instead of content.
//  2. Run the race and assert the returned error carries the host's own error message.
//  3. Assert the outcome did not succeed and crowned no winner.
//  4. Assert the attempt is TerminalErrorStream, still counts the error event as a content chunk, and does not deny crowning.
//  5. Assert the client received nothing and the window moved once with ModelOutcome.
func TestSimulatorErrorStreamNeitherCrownsNorReadsAsEmpty(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:  true,
		chunks:   []string{`data: {"error":{"code":500,"message":"backend failed","type":"server_error"}}` + "\n\n"},
		finished: true,
	})

	outcome, err := sim.run(context.Background())

	var hostErr *HostApplicationError
	if !errors.As(err, &hostErr) || hostErr.Message != "backend failed" {
		t.Fatalf("Run() error = %v, want the host's own refusal", err)
	}
	if outcome.Succeeded || outcome.WinnerNonce != 0 {
		t.Fatalf("an error event crowned a winner: %+v", outcome)
	}
	reported := sim.reported(t)
	attempt := attemptFor(t, reported, 10)
	if attempt.Terminal != TerminalErrorStream {
		t.Fatalf("terminal = %v, want TerminalErrorStream", attempt.Terminal)
	}
	if attempt.ContentChunks != 1 {
		t.Errorf("ContentChunks = %d, want 1: an error event still counts as a chunk", attempt.ContentChunks)
	}
	if reported.DeniesCrowning(attempt) {
		t.Error("an error stream was charged as if the host had answered with nothing")
	}
	if len(sim.client.forwarded()) != 0 {
		t.Errorf("client received %q from an attempt that never earned the crown", sim.client.forwarded())
	}
	if moves := sim.windows.recorded(); len(moves) != 1 || moves[0].verdict != limits.ModelOutcome {
		t.Fatalf("window moves = %+v, want one ModelOutcome", moves)
	}
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to send two role-only chunks before its first content chunk.
//  2. Run the race and assert it returns no error.
//  3. Assert the client received the buffered role chunks flushed in order ahead of the content that won the crown.
//  4. Read the reported outcome.
func TestSimulatorHoldsPreContentChunksUntilTheCrownIsWon(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{roleEvent, roleEvent, contentEvent("hi")},
		confirmed: true,
		finished:  true,
	})

	if _, err := sim.run(context.Background()); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := roleEvent + roleEvent + contentEvent("hi")
	if got := string(sim.client.forwarded()); got != want {
		t.Fatalf("client stream = %q, want the buffered prefix flushed in order ahead of it: %q", got, want)
	}
	sim.reported(t)
}

// Test flow:
//  1. Start a two-host simulator where host-0 refuses with a tool-choice-unsupported error and host-1 can serve the tool call.
//  2. Run the race and assert it returns no error with host-1's nonce as the winner.
//  3. Assert host-0's attempt is TerminalCapabilityRefused.
//  4. Assert the scheduler was asked to pick twice, the second pick excluding "toolless-host", and the refusal was recorded against it.
func TestSimulatorToolRefusalDoesNotCrownAndRepicksElsewhere(t *testing.T) {
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	sim.host(10, 0, "toolless-host", &hostScript{
		receipt: true,
		chunks: []string{
			`data: {"error":{"code":400,"message":"` + filters.ToolChoiceUnsupportedMessage + `","type":"BadRequestError"}}` + "\n\n",
		},
	})
	sim.host(11, 1, "tool-host", &hostScript{
		receipt: true, chunks: []string{contentEvent("hi")},
		confirmed: true, finished: true,
	})

	outcome, err := sim.run(context.Background())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.WinnerNonce != 11 {
		t.Fatalf("winner = %d, want the host that could serve the tool call", outcome.WinnerNonce)
	}
	reported := sim.reported(t)
	if refused := attemptFor(t, reported, 10); refused.Terminal != TerminalCapabilityRefused {
		t.Fatalf("terminal = %v, want TerminalCapabilityRefused", refused.Terminal)
	}
	sim.picker.mu.Lock()
	profiles := append([]scheduler.RequestProfile(nil), sim.picker.profiles...)
	sim.picker.mu.Unlock()
	if len(profiles) != 2 {
		t.Fatalf("Pick calls = %d, want 2", len(profiles))
	}
	if len(profiles[1].Exclude) != 1 || profiles[1].Exclude[0] != "toolless-host" {
		t.Errorf("re-pick Exclude = %v, want [toolless-host]", profiles[1].Exclude)
	}
	if recorded := sim.perf.toolCalls; len(recorded) != 1 || recorded[0] != "toolless-host" {
		t.Errorf("recorded tool refusals = %v, want one for toolless-host", recorded)
	}
}

// Test flow:
//  1. Start a two-host simulator and script host-0 to reject the request for exceeding the model's context length while host-1 could otherwise answer.
//  2. Run the race and assert the returned error is the host's own rejection payload.
//  3. Assert the scheduler was picked only once, since every host would reject the same body, and that the rejection recorded a context limit of 40960 tokens for host-0.
//  4. Settle the race and assert the rejected nonce still got its vote posted.
func TestSimulatorContextLengthRejectionReachesTheClientWithoutAnotherAttempt(t *testing.T) {
	const rejection = `{"error":{"code":400,"message":"` + vllmContextTotalMessage + `","type":"BadRequestError"}}`
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: " + rejection + "\n\n"}})
	sim.host(11, 1, "host-1", &hostScript{
		receipt: true, chunks: []string{contentEvent("hi")},
		confirmed: true, finished: true,
	})

	_, err := sim.run(context.Background())

	var hostErr *HostApplicationError
	if !errors.As(err, &hostErr) || hostErr.Payload != rejection {
		t.Fatalf("Run() error = %v, want the host's own rejection %s", err, rejection)
	}
	sim.reported(t)
	sim.picker.mu.Lock()
	picks := len(sim.picker.profiles)
	sim.picker.mu.Unlock()
	if picks != 1 {
		t.Fatalf("Pick calls = %d, want 1", picks)
	}
	if recorded := sim.perf.limits; len(recorded) != 1 || recorded[0] != (contextLimitCall{participant: "host-0", maxTokens: 40_960}) {
		t.Errorf("recorded context limits = %+v, want one of 40960 for host-0", recorded)
	}
	sim.settleAll()
	if posted := sim.poster.settled(); len(posted) != 1 || posted[0] != 10 {
		t.Fatalf("votes posted = %v, want [10]", posted)
	}
}

// Test flow:
//  1. Start a two-host simulator and script host-0 to reject the request body with a malformed-tool-call error while host-1 could otherwise answer.
//  2. Run the race and assert the returned error is host-0's own rejection payload.
//  3. Assert the scheduler was picked only once, since every host would reject the same body.
//  4. Settle the race and assert the rejected nonce still got its vote posted.
func TestSimulatorRequestRejectionReachesTheClientWithoutAnotherAttempt(t *testing.T) {
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, chunks: []string{"data: " + malformedToolCallRejection + "\n\n"}})
	sim.host(11, 1, "host-1", &hostScript{
		receipt: true, chunks: []string{contentEvent("hi")},
		confirmed: true, finished: true,
	})

	_, err := sim.run(context.Background())

	var hostErr *HostApplicationError
	if !errors.As(err, &hostErr) || hostErr.Payload != malformedToolCallRejection {
		t.Fatalf("Run() error = %v, want the host's own rejection %s", err, malformedToolCallRejection)
	}
	sim.reported(t)
	sim.picker.mu.Lock()
	picks := len(sim.picker.profiles)
	sim.picker.mu.Unlock()
	if picks != 1 {
		t.Fatalf("Pick calls = %d, want 1: every host would reject the same body", picks)
	}
	sim.settleAll()
	if posted := sim.poster.settled(); len(posted) != 1 || posted[0] != 10 {
		t.Fatalf("votes posted = %v, want [10]", posted)
	}
}

// Test flow:
//  1. Split the content_stream.sse fixture into chunks of a given size (1, 3, 64, 1024, or 8192 bytes) and script host-0 to stream them.
//  2. Run the race and assert it returns no error.
//  3. Assert the client received the fixture bytes unchanged and the winning attempt's content source is delta.content, for every chunk size.
func TestSimulatorForwardsTheFixtureCorpusUnchangedAtEveryChunkSize(t *testing.T) {
	body := readFixture(t, "content_stream.sse")
	for _, chunkSize := range []int{1, 3, 64, 1024, 8192} {
		t.Run("chunk="+strconv.Itoa(chunkSize), func(t *testing.T) {
			sim := newSimulator(t, settledPolicy(), 1, qwenModel)
			chunks := make([]string, 0, len(body)/chunkSize+1)
			for start := 0; start < len(body); start += chunkSize {
				chunks = append(chunks, string(body[start:min(start+chunkSize, len(body))]))
			}
			sim.host(10, 0, "host-0", &hostScript{
				receipt: true, chunks: chunks, confirmed: true, finished: true,
			})

			if _, err := sim.run(context.Background()); err != nil {
				t.Fatalf("Run() error = %v", err)
			}

			if !bytes.Equal(sim.client.forwarded(), body) {
				t.Fatalf("client stream differs from the host stream at chunk size %d", chunkSize)
			}
			if winner := attemptFor(t, sim.reported(t), 10); winner.ContentSource != "delta.content" {
				t.Fatalf("ContentSource = %q, want delta.content", winner.ContentSource)
			}
		})
	}
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to stream the newlineless_final_content.sse fixture, whose last event never gets a trailing newline.
//  2. Run the race and assert it fails with ErrAllAttemptsFailed.
//  3. Assert the attempt is TerminalLost with content source delta.content from the flushed tail, and does not deny crowning.
func TestSimulatorFlushesTheFinalEventBeforeDecidingEmptiness(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt:  true,
		chunks:   []string{string(readFixture(t, "newlineless_final_content.sse"))},
		finished: true,
	})

	if _, err := sim.run(context.Background()); !errors.Is(err, ErrAllAttemptsFailed) {
		t.Fatalf("Run() error = %v, want ErrAllAttemptsFailed", err)
	}

	reported := sim.reported(t)
	attempt := attemptFor(t, reported, 10)
	if attempt.Terminal != TerminalLost {
		t.Fatalf("terminal = %v, want TerminalLost", attempt.Terminal)
	}
	if attempt.ContentSource != "delta.content" {
		t.Errorf("ContentSource = %q, want delta.content from the flushed tail", attempt.ContentSource)
	}
	if reported.DeniesCrowning(attempt) {
		t.Error("a flushed content event was still charged as an empty answer")
	}
}

// Test flow:
//  1. Start a simulator in PoC-relaxed mode and hold host-0's attempt at arrive before it sends any chunks.
//  2. Move the chain snapshot to EpochPhasePoCGenerate while the attempt is held, then release it and run the race to completion.
//  3. Assert the attempt is marked PhaseTransitionAborted with sample exemption ExemptPhaseAborted, and that no perf sample or window move was recorded.
//  4. Settle the race and assert the timeout event was skipped for TimeoutReasonPhaseAborted with no vote posted.
func TestSimulatorPhaseTransitionAbortSkipsTheSampleAndTheVote(t *testing.T) {
	sim := newSimulatorInPhase(t, settledPolicy(), 1, qwenModel,
		config.Modes{PoCMode: config.PoCModeRelaxed}, chain.PhaseSnapshot{})
	arrive, release := make(chan uint64, 1), make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{arrive: arrive, release: release, receipt: true})

	outcomes := make(chan error, 1)
	go func() {
		_, err := sim.run(context.Background())
		outcomes <- err
	}()
	<-arrive
	sim.snapshots.set(chain.PhaseSnapshot{EpochPhase: chain.EpochPhasePoCGenerate})
	close(release)
	<-outcomes

	reported := sim.reported(t)
	attempt := attemptFor(t, reported, 10)
	if !attempt.PhaseTransitionAborted {
		t.Fatal("an attempt the phase transition ended was blamed on its host")
	}
	if exemption := reported.sampleExemption(attempt); exemption != ExemptPhaseAborted {
		t.Errorf("sample exemption = %v, want ExemptPhaseAborted", exemption)
	}
	if samples := sim.perf.recorded(); len(samples) != 0 {
		t.Errorf("samples = %+v, want none", samples)
	}
	if moves := sim.windows.recorded(); len(moves) != 0 {
		t.Errorf("window moves = %+v, want none", moves)
	}
	sim.settleAll()
	events := sim.timeoutEvents()
	if len(events) != 1 || events[0].Action != TimeoutActionSkipped || events[0].Reason != TimeoutReasonPhaseAborted {
		t.Fatalf("timeout events = %+v, want one skipped/%s", events, TimeoutReasonPhaseAborted)
	}
	if posted := sim.poster.settled(); len(posted) != 0 {
		t.Errorf("votes posted = %v, want none", posted)
	}
}

// Test flow:
//  1. Start a one-host simulator, park the poster's vote by leaving release closed, and script host-0 to fail with "host refused".
//  2. Run the race, read the reported outcome, and wait until the failed nonce's vote has entered the poster.
//  3. Call Stop in a goroutine and assert it has not returned while the vote is still parked.
//  4. Release the vote, wait for Stop to return, and assert the nonce's vote was posted.
func TestSimulatorStopWaitsForTheVoteAFinishedRaceStillOwes(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.poster.release = make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{receipt: true, err: errors.New("host refused")})

	if _, err := sim.run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the failed attempt's error")
	}
	sim.reported(t)
	<-sim.poster.entered

	stopped := make(chan struct{})
	go func() {
		sim.engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a vote was still in flight")
	default:
	}
	close(sim.poster.release)
	<-stopped

	if posted := sim.poster.settled(); len(posted) != 1 || posted[0] != 10 {
		t.Fatalf("votes posted = %v, want [10]", posted)
	}
}

// Test flow:
//  1. Start a one-host simulator, park the poster's votes, and script host-0 to fail with "host refused".
//  2. Run the race, read the reported outcome, and wait until the failed nonce's vote has entered the poster.
//  3. Assert the escrow's hold was taken and not yet given back while the vote is still parked.
//  4. Release the parked vote, stop the engine, and assert the escrow's hold was given back.
func TestSimulatorTheEscrowStaysHeldUntilTheVoteIsPosted(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	postVote := sim.parkVotes(t)
	sim.host(10, 0, "host-0", &hostScript{receipt: true, err: errors.New("host refused")})

	if _, err := sim.run(context.Background()); err == nil {
		t.Fatal("Run() error = nil, want the failed attempt's error")
	}
	sim.reported(t)
	<-sim.poster.entered

	holds, releases := sim.target.held()
	if holds != 1 {
		t.Fatalf("escrow holds taken = %d, want 1", holds)
	}
	if releases != 0 {
		t.Fatalf("escrow holds given back while the vote is still in flight = %d, want 0", releases)
	}

	postVote()
	sim.engine.Stop()

	if _, releases := sim.target.held(); releases != 1 {
		t.Errorf("escrow holds given back after the vote = %d, want 1", releases)
	}
}

// Test flow:
//  1. Start a one-host simulator and force its dispatch target to panic on every send.
//  2. Run the race inside a recover and assert the panic re-raises out of Run instead of being swallowed.
//  3. Assert the escrow's hold was taken and given back despite the panic.
func TestSimulatorAPanickingRaceGivesTheEscrowHoldBack(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{receipt: true})
	sim.target.panicking.Store(true)

	func() {
		defer func() {
			if panicked := recover(); panicked == nil {
				t.Error("Run() recovered the panic instead of re-raising it")
			}
		}()
		_, _ = sim.run(context.Background())
	}()

	holds, releases := sim.target.held()
	if holds != 1 || releases != 1 {
		t.Fatalf("escrow holds taken/given back = %d/%d, want 1/1", holds, releases)
	}
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to pause mid-stream at streaming before its remaining chunks.
//  2. Run the race in the background and wait for it to reach that pause.
//  3. Call Stop in a goroutine and assert it does not return within 250ms while the race is still running.
//  4. Resume the host, wait for the race and Stop to finish, and assert the nonce's vote was posted before Stop returned.
func TestSimulatorStopWaitsForARaceThatIsStillRunning(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	streaming, resume := make(chan uint64, 1), make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hi")},
		streaming: streaming,
		resume:    resume,
	})

	outcomes := make(chan error, 1)
	go func() {
		_, err := sim.run(context.Background())
		outcomes <- err
	}()
	<-streaming

	stopped := make(chan struct{})
	go func() {
		sim.engine.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("Stop returned while a race was still running")
	case <-time.After(250 * time.Millisecond):
	}

	close(resume)
	<-outcomes
	<-stopped

	if posted := sim.poster.settled(); len(posted) != 1 || posted[0] != 10 {
		t.Fatalf("votes posted = %v, want [10] before Stop returned", posted)
	}
	sim.reported(t)
}

// Test flow:
//  1. Start a one-host simulator with a 500ms inter-chunk-stall policy and script host-0 to send one content chunk, pause at streaming, then hold the request open past its deadlines.
//  2. Run the race, wait for the pause, wait for the inter-chunk-stall deadline to arm, then advance the virtual clock past the streaming hard timeout backstop.
//  3. Resume the host and let the race finish.
//  4. Assert the attempt is TerminalHardTimeout, neither finished its nonce nor was confirmed, and the host's window moved.
//  5. Settle the race and assert the timeout vote ran to completion rather than being skipped, and no slot leaked.
func TestSimulatorAHostCutAtTheBackstopAnswersForIt(t *testing.T) {
	policy := settledPolicy()
	policy.InterChunkStall = 500 * time.Millisecond
	sim := newSimulator(t, policy, 1, qwenModel)
	streaming, resume := make(chan uint64, 1), make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hi")},
		streaming: streaming,
		resume:    resume,
		hold:      true,
	})

	outcomes := make(chan error, 1)
	go func() {
		_, err := sim.run(context.Background())
		outcomes <- err
	}()
	<-streaming
	sim.clock.waitArmed(t, policy.InterChunkStall)
	sim.clock.advance(streamingHardTimeout + time.Second)
	close(resume)
	<-outcomes

	reported := sim.reported(t)
	attempt := attemptFor(t, reported, 10)
	if attempt.Terminal != TerminalHardTimeout {
		t.Fatalf("terminal = %v, want TerminalHardTimeout", attempt.Terminal)
	}
	if attempt.NonceFinished || attempt.Confirmed {
		t.Fatal("a host cut at the backstop was credited with the nonce it never finished")
	}
	if moves := sim.windows.recorded(); len(moves) == 0 {
		t.Error("window did not move for a host that held the request past the backstop")
	}
	sim.settleAll()
	events := sim.timeoutEvents()
	voted := false
	for _, event := range events {
		if event.Action == TimeoutActionSkipped {
			t.Fatalf("timeout events = %+v, want the vote to run rather than be skipped", events)
		}
		voted = voted || event.Action == TimeoutActionCompleted
	}
	if !voted {
		t.Fatalf("timeout events = %+v, want the vote carried through to a miss", events)
	}
	sim.assertNoSlotLeaked(t, 1)
}

// Test flow:
//  1. Script host-0 to answer with a completion-tokens-only chunk that carries no content, for a model that varies across the case table.
//  2. Run the race and assert it fails with ErrEmptyStream.
//  3. Assert the attempt's terminal, crowning denial, and window verdict match the request's thinking-budget flag, never the model name: a flagged request reads as TerminalBurnEmpty/ModelOutcome without denying crowning, while an unflagged one, Kimi included, reads as TerminalEmptyStream/EmptyAnswer and denies crowning.
func TestSimulatorBurnEmptyIsAModelOutcomeAndPlainEmptyIsNot(t *testing.T) {
	burnStream := `data: {"choices":[{"delta":{}}],"usage":{"completion_tokens":90}}` + "\n\n"
	cases := []struct {
		name                    string
		model                   string
		emptyAnswerCountsTokens bool
		wantTerminal            Terminal
		wantDenied              bool
		wantVerdict             limits.Verdict
	}{
		{
			name:                    "thinking_budget_route_burned_the_tokens",
			model:                   kimiModel,
			emptyAnswerCountsTokens: true,
			wantTerminal:            TerminalBurnEmpty,
			wantVerdict:             limits.ModelOutcome,
		},
		{
			name:         "any_other_route_answered_with_nothing",
			model:        qwenModel,
			wantTerminal: TerminalEmptyStream,
			wantDenied:   true,
			wantVerdict:  limits.EmptyAnswer,
		},
		{
			name:         "a_thinking_model_without_the_request_flag_answered_with_nothing",
			model:        kimiModel,
			wantTerminal: TerminalEmptyStream,
			wantDenied:   true,
			wantVerdict:  limits.EmptyAnswer,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			sim := newSimulator(t, settledPolicy(), 1, testCase.model)
			sim.emptyAnswerCountsTokens = testCase.emptyAnswerCountsTokens
			sim.host(10, 0, "host-0", &hostScript{receipt: true, chunks: []string{burnStream}, finished: true})

			if _, err := sim.run(context.Background()); !errors.Is(err, ErrEmptyStream) {
				t.Fatalf("Run() error = %v, want ErrEmptyStream", err)
			}

			reported := sim.reported(t)
			attempt := attemptFor(t, reported, 10)
			if attempt.Terminal != testCase.wantTerminal {
				t.Fatalf("terminal = %v, want %v", attempt.Terminal, testCase.wantTerminal)
			}
			if reported.DeniesCrowning(attempt) != testCase.wantDenied {
				t.Errorf("DeniesCrowning = %v, want %v", reported.DeniesCrowning(attempt), testCase.wantDenied)
			}
			if moves := sim.windows.recorded(); len(moves) != 1 || moves[0].verdict != testCase.wantVerdict {
				t.Fatalf("window moves = %+v, want one %v", moves, testCase.wantVerdict)
			}
		})
	}
}

// Test flow:
//  1. Start a two-host simulator where host-0 finishes its nonce with no content and host-1 answers.
//  2. Run the race and assert it returns no error with host-1's nonce as the winner.
//  3. Assert the second attempt started because the first one failed (EscalationReasonAttemptFailed).
//  4. Assert the empty host's window moved first with EmptyAnswer, followed by the winner's move.
func TestSimulatorAnEmptyAnswerHandsTheRequestToAnotherHost(t *testing.T) {
	sim := newSimulator(t, speculativePolicy(2), 2, qwenModel)
	sim.host(10, 0, "empty-host", &hostScript{receipt: true, confirmed: true, finished: true})
	sim.host(11, 1, "content-host", &hostScript{
		receipt: true, chunks: []string{contentEvent("hi")},
		confirmed: true, finished: true,
	})

	outcome, err := sim.run(context.Background())

	if err != nil {
		t.Fatalf("Run() error = %v, want the second host's answer", err)
	}
	if outcome.WinnerNonce != 11 {
		t.Fatalf("winner = %d, want the host that answered", outcome.WinnerNonce)
	}
	reported := sim.reported(t)
	if escalated := attemptFor(t, reported, 11); escalated.StartReason != EscalationReasonAttemptFailed {
		t.Errorf("second attempt start reason = %q, want %q", escalated.StartReason, EscalationReasonAttemptFailed)
	}
	if moves := sim.windows.recorded(); len(moves) != 2 || moves[0] != (windowMove{participant: "empty-host", verdict: limits.EmptyAnswer}) {
		t.Fatalf("window moves = %+v, want the empty host's window halved, then the winner's", moves)
	}
}

// Test flow:
//  1. Start a two-host simulator racing a slow role-only host against a fast content-producing host, both held at arrive/release.
//  2. Release both hosts at once and run the race to completion.
//  3. Assert the fast host's nonce wins and the client received only its bytes.
//  4. Assert exactly one of the two attempts is crowned TerminalWon, no slot leaked, and exactly one outcome was reported.
func TestSimulatorSpeculativeSecondaryWinsAndItsLosersAreDrained(t *testing.T) {
	sim := newSimulator(t, racePolicy(2), 2, qwenModel)
	arrive, release := make(chan uint64, 2), make(chan struct{})
	sim.host(10, 0, "slow-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, chunks: []string{roleEvent}, confirmed: true, finished: true,
	})
	sim.host(11, 1, "fast-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, chunks: []string{contentEvent("hi")}, confirmed: true, finished: true,
	})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := sim.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	<-arrive
	<-arrive
	close(release)
	<-outcomes

	reported := sim.reported(t)
	if reported.WinnerNonce != 11 {
		t.Fatalf("winner = %d, want the only content producer", reported.WinnerNonce)
	}
	if got, want := string(sim.client.forwarded()), contentEvent("hi"); got != want {
		t.Fatalf("client stream = %q, want only the winner's bytes %q", got, want)
	}
	won := 0
	for _, attempt := range reported.Attempts {
		if attempt.Terminal == TerminalWon {
			won++
		}
	}
	if won != 1 || len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d with %d crowned, want 2 with 1", len(reported.Attempts), won)
	}
	sim.assertNoSlotLeaked(t, 2)
	sim.assertOneOutcome(t)
}

// Test flow:
//  1. Register a goroutine-leak check with goleak, start a one-host simulator, and script host-0 to pause mid-stream at streaming before a second content chunk.
//  2. Run the race with a cancellable client context, wait for the pause, then cancel the context and assert Run returns context.Canceled.
//  3. Resume the host so it finishes streaming after the client disconnected.
//  4. Assert the attempt finished its nonce and is TerminalWon.
//  5. Settle the race and assert no vote was posted for the already-finished nonce, no slot leaked, and exactly one outcome reported.
func TestSimulatorClientDisconnectStillSettlesTheNonce(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	streaming, resume := make(chan uint64, 1), make(chan struct{})
	sim.host(10, 0, "host-0", &hostScript{
		receipt:   true,
		chunks:    []string{contentEvent("hi"), contentEvent("after the client left")},
		streaming: streaming,
		resume:    resume,
		confirmed: true,
		finished:  true,
	})

	clientCtx, disconnect := context.WithCancel(context.Background())
	outcomes := make(chan error, 1)
	go func() {
		_, err := sim.run(clientCtx)
		outcomes <- err
	}()
	<-streaming
	disconnect()

	if err := <-outcomes; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	close(resume)

	reported := sim.reported(t)
	attempt := attemptFor(t, reported, 10)
	if !attempt.NonceFinished {
		t.Fatal("the race abandoned its nonce when the client left")
	}
	if attempt.Terminal != TerminalWon {
		t.Fatalf("terminal = %v, want TerminalWon: the host ran to completion", attempt.Terminal)
	}
	sim.settleAll()
	if posted := sim.poster.settled(); len(posted) != 0 {
		t.Errorf("votes posted = %v, want none for a finished nonce", posted)
	}
	sim.assertNoSlotLeaked(t, 1)
	sim.assertOneOutcome(t)
}

// Test flow:
//  1. Start a one-host simulator and script host-0 to emit unparsable garbage bytes followed by [DONE].
//  2. Run the race and assert it fails with ErrEmptyStream.
//  3. Assert no nonce was crowned and the client received nothing.
//  4. Assert the attempt is TerminalEmptyStream.
func TestSimulatorGarbageStreamNeverCrowns(t *testing.T) {
	sim := newSimulator(t, settledPolicy(), 1, qwenModel)
	sim.host(10, 0, "host-0", &hostScript{
		receipt: true,
		chunks: []string{
			"data: {\x00\x00\x00\n\n",
			`data: {"choices":[{"delta"` + "\n",
			"data: [DONE]\n\n",
		},
		finished: true,
	})

	outcome, err := sim.run(context.Background())

	if !errors.Is(err, ErrEmptyStream) {
		t.Fatalf("Run() error = %v, want ErrEmptyStream", err)
	}
	if outcome.WinnerNonce != 0 {
		t.Fatalf("garbage crowned nonce %d", outcome.WinnerNonce)
	}
	if len(sim.client.forwarded()) != 0 {
		t.Errorf("client received %q from a stream that produced no content", sim.client.forwarded())
	}
	if attempt := attemptFor(t, sim.reported(t), 10); attempt.Terminal != TerminalEmptyStream {
		t.Fatalf("terminal = %v, want TerminalEmptyStream", attempt.Terminal)
	}
}

// Test flow:
//  1. Start a two-host simulator, pin "pinned-host" as suspicious, and script it alongside a trusted host, both held at arrive/release.
//  2. Run the race and wait for both attempts to arrive before releasing either, proving the second attempt started immediately rather than after the first failed or timed out.
//  3. Release both hosts and run the race to completion.
//  4. Assert the decision was StartPrimarySuspicious with two attempts started, the pinned host's attempt marked suspicious and the trusted host's not.
//  5. Assert no slot leaked and exactly one outcome was reported.
func TestSimulatorPinnedPrimaryStartsASecondAttemptImmediately(t *testing.T) {
	sim := newSimulator(t, racePolicy(2), 2, qwenModel)
	sim.pinned["pinned-host"] = true
	arrive, release := make(chan uint64, 2), make(chan struct{})
	sim.host(10, 0, "pinned-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, chunks: []string{roleEvent}, confirmed: true, finished: true,
	})
	sim.host(11, 1, "trusted-host", &hostScript{
		arrive: arrive, release: release,
		receipt: true, chunks: []string{contentEvent("hi")}, confirmed: true, finished: true,
	})

	outcomes := make(chan RaceOutcome, 1)
	go func() {
		outcome, err := sim.run(context.Background())
		if err != nil {
			t.Error(err)
		}
		outcomes <- outcome
	}()
	<-arrive
	<-arrive
	close(release)
	<-outcomes

	reported := sim.reported(t)
	if reported.Decision != StartPrimarySuspicious {
		t.Fatalf("decision = %q, want %q", reported.Decision, StartPrimarySuspicious)
	}
	if len(reported.Attempts) != 2 {
		t.Fatalf("attempts = %d, want 2 started immediately", len(reported.Attempts))
	}
	if !attemptFor(t, reported, 10).Suspicious {
		t.Error("the pinned host's attempt is not marked suspicious")
	}
	if attemptFor(t, reported, 11).Suspicious {
		t.Error("the unpinned host's attempt is marked suspicious")
	}
	sim.assertNoSlotLeaked(t, 2)
	sim.assertOneOutcome(t)
}

type panickingPicker struct{}

func (p *panickingPicker) Pick(context.Context, scheduler.RequestProfile) (scheduler.Assignment, error) {
	panic("picker exploded")
}

func (p *panickingPicker) HostDiverged(string, string, time.Time) bool { return false }

func (p *panickingPicker) HostServed(string, string, time.Time) {}

func (p *panickingPicker) HostAbsent(string, string) {}

// Test flow:
//  1. Build an engine directly, not through the simulator (whose cleanup is itself a Stop that would hang here), with a picker that panics on every Pick.
//  2. Run the race in a goroutine wrapped in a recover, and assert the panic reaches the caller rather than being swallowed.
//  3. Call Stop in a goroutine and assert it returns, proving the panicking race released its registration.
func TestPanickingRaceReleasesTheStopBarrierAndRepanics(t *testing.T) {
	races, err := NewEngine(Deps{
		Picker:    &panickingPicker{},
		Targets:   &simTargets{scriptedTarget: &scriptedTarget{scripts: map[uint64]*hostScript{}, labels: map[int]string{}}},
		Windows:   &simWindows{},
		Perf:      &simTracker{stubPerf: &stubPerf{ejected: map[string]bool{}, degraded: map[string]bool{}}},
		Config:    config.NewHolder(engineSettings(settledPolicy(), config.Modes{})),
		Snapshots: newSimSnapshots(chain.PhaseSnapshot{}),
		Now:       func() time.Time { return testEpoch },
	})
	if err != nil {
		t.Fatalf("NewEngine() = %v, want a running engine", err)
	}

	recovered := make(chan any, 1)
	go func() {
		defer func() { recovered <- recover() }()
		_, _ = races.Run(context.Background(), Request{RequestID: "panicking", Model: qwenModel}, &bytes.Buffer{})
	}()

	select {
	case panicked := <-recovered:
		if panicked == nil {
			t.Fatal("the panic was swallowed; the client would be answered with an outcome no race produced")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() neither returned nor panicked")
	}

	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		races.Stop()
	}()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop() never returned: the panicking race still holds its registration")
	}
}
