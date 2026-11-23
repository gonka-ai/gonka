package shadow

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/scheduler"
	"devshard/cmd/gateway/warmup"
	"devshard/host"
	"devshard/user"
)

const (
	probeEscrow      = "escrow-1"
	probeParticipant = "gonka1shadowhost"
	probeModel       = "model-a"
	awaitBudget      = 5 * time.Second
	probeHardTimeout = time.Hour
)

type stubEscrows struct {
	session  registry.EscrowSession
	held     bool
	released atomic.Int64
	settling atomic.Int64
	settled  atomic.Int64
}

func (s *stubEscrows) HoldForVote(string) (registry.EscrowSession, func(), bool) {
	s.settling.Add(1)
	return s.session, func() { s.settled.Add(1) }, s.held
}

func (s *stubEscrows) AcquireCommitted(string) (registry.EscrowSession, func(), bool) {
	return s.session, func() { s.released.Add(1) }, s.held
}

type stubSession struct {
	registry.EscrowSession
	armed atomic.Int64
}

func (s *stubSession) ArmQuietSettle() { s.armed.Add(1) }

type spyOutcomes struct{ results chan Result }

func (s spyOutcomes) ShadowProbed(result Result) { s.results <- result }

type spyProbes struct {
	mu       sync.Mutex
	attempts []accounting.Attempt
}

func (s *spyProbes) ProbeRecorded(_ string, attempt accounting.Attempt) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attempts = append(s.attempts, attempt)
}

type stubPoster struct{ calls atomic.Int64 }

func (p *stubPoster) VoteDeadline(uint64, time.Time) time.Time { return time.Time{} }

func (p *stubPoster) Voteable(uint64) bool { return true }

func (p *stubPoster) SettleTimeout(context.Context, engine.TimeoutStep) (engine.TimeoutVote, error) {
	p.calls.Add(1)
	return engine.TimeoutVote{Kind: engine.TimeoutKindRefused}, nil
}

type spyTimeouts struct {
	mu     sync.Mutex
	events []engine.TimeoutEvent
}

func (s *spyTimeouts) RecordTimeout(event engine.TimeoutEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

type probeFixture struct {
	runner   *Runner
	escrows  *stubEscrows
	session  *stubSession
	outcomes spyOutcomes
	probes   *spyProbes
	poster   *stubPoster
	timeouts *spyTimeouts
	held     atomic.Int64
	hardTime time.Duration
}

func steppedClock() func() time.Time {
	var mu sync.Mutex
	current := time.Unix(1_700_000_000, 0)
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		current = current.Add(100 * time.Millisecond)
		return current
	}
}

func newFixture(t *testing.T, send sendFunc) *probeFixture {
	t.Helper()
	return newFixtureWithin(t, t.Context(), send)
}

func newFixtureWithin(t *testing.T, lifetime context.Context, send sendFunc) *probeFixture {
	t.Helper()
	session := &stubSession{}
	fixture := &probeFixture{
		runner:   New(steppedClock(), roomyBounds),
		escrows:  &stubEscrows{session: session, held: true},
		session:  session,
		outcomes: spyOutcomes{results: make(chan Result, 1)},
		probes:   &spyProbes{},
		poster:   &stubPoster{},
		timeouts: &spyTimeouts{},
		hardTime: probeHardTimeout,
	}
	fixture.runner.send = send
	fixture.runner.Bind(Binding{
		Escrows:  fixture.escrows,
		Lifetime: lifetime,
		Voter: warmup.Voter{
			Posters:  func(string, any) (engine.TimeoutPoster, bool) { return fixture.poster, true },
			Timeouts: fixture.timeouts,
		},
		Probes:      fixture.probes,
		Outcomes:    fixture.outcomes,
		HardTimeout: func() time.Duration { return fixture.hardTime },
	})
	return fixture
}

func (f *probeFixture) request() scheduler.ProbeRequest {
	params, skip := f.runner.Params(queuedProfile(clientRequest(clientBody)), 1_700_000_000)
	if skip != scheduler.ProbeSkipNone {
		panic("the client body must give a probe")
	}
	return scheduler.ProbeRequest{
		Assignment: scheduler.Assignment{
			Escrow:     probeEscrow,
			Host:       probeParticipant,
			Nonce:      &user.PreparedInference{},
			EscrowHold: func() { f.held.Add(1) },
		},
		Model:  probeModel,
		Params: params,
	}
}

func (f *probeFixture) await(t *testing.T) Result {
	t.Helper()
	result := f.awaitReported(t)
	if result.Outcome == OutcomeFailed {
		f.awaitVote(t)
	}
	return result
}

func (f *probeFixture) awaitReported(t *testing.T) Result {
	t.Helper()
	select {
	case result := <-f.outcomes.results:
		return result
	case <-time.After(awaitBudget):
		t.Fatal("timed out waiting for the shadow probe's result to be reported")
		return Result{}
	}
}

func (f *probeFixture) awaitVote(t *testing.T) {
	t.Helper()
	deadline := time.After(awaitBudget)
	for f.escrows.settled.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for the failed probe's vote hold to be released")
		case <-time.After(time.Millisecond):
		}
	}
}

func servedAnswer(chunks ...string) sendFunc {
	return func(_ context.Context, _ registry.EscrowSession, _ *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
		onReceipt()
		for _, chunk := range chunks {
			if _, err := answer.Write([]byte(chunk)); err != nil {
				return nil, err
			}
		}
		return &host.HostResponse{Receipt: []byte("receipt")}, nil
	}
}

// Test flow:
//  1. Bind a runner whose send streams two SSE events, the last carrying a usage of 7 completion tokens.
//  2. Probe a committed nonce bound to a shadow participant and await the reported result.
//  3. Assert the result is served, streamed, names the participant, escrow and nonce, and carries the 7 output tokens and a positive total.
//  4. Assert the assignment's escrow hold was given back once the runner held its own, and the runner released its own when it finished.
//  5. Assert the ledger got one attempt under `TerminalShadowProbe`, sent and acknowledged, and no timeout vote was cast.
//  6. Assert the session's quiet settle was armed and the runner's own hold was released before the result was reported.
func TestServedProbeIsReportedRecordedAndSettledWithoutAVote(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	fixture := newFixture(t, servedAnswer(
		"data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n",
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":7}}\n\ndata: [DONE]\n\n",
	))

	if !fixture.runner.Probe(fixture.request()) {
		t.Fatal("Probe = false, want the probe started")
	}
	result := fixture.await(t)

	if result.Outcome != OutcomeServed || !result.Streamed || result.OutputTokens != 7 || result.Total <= 0 || result.Err != nil {
		t.Fatalf("result = %+v, want a served streamed probe of 7 output tokens", result)
	}
	if result.Participant != probeParticipant || result.Escrow != probeEscrow || result.Model != probeModel {
		t.Errorf("result names %s/%s/%s, want %s/%s/%s", result.Participant, result.Escrow, result.Model, probeParticipant, probeEscrow, probeModel)
	}
	if fixture.held.Load() != 1 {
		t.Errorf("assignment holds released = %d, want 1", fixture.held.Load())
	}
	if len(fixture.probes.attempts) != 1 || fixture.probes.attempts[0].Terminal != accounting.TerminalShadowProbe || !fixture.probes.attempts[0].Sent || !fixture.probes.attempts[0].Acknowledged {
		t.Errorf("ledger attempts = %+v, want one sent, acknowledged %s attempt", fixture.probes.attempts, accounting.TerminalShadowProbe)
	}
	if fixture.poster.calls.Load() != 0 || len(fixture.timeouts.events) != 0 {
		t.Errorf("votes = %d, events = %+v, want none for a served probe", fixture.poster.calls.Load(), fixture.timeouts.events)
	}
	if fixture.session.armed.Load() != 1 {
		t.Errorf("quiet settle armed %d times, want 1", fixture.session.armed.Load())
	}
	if fixture.escrows.released.Load() != 1 {
		t.Errorf("runner holds released = %d, want 1", fixture.escrows.released.Load())
	}
}

// Test flow:
//  1. Bind a runner whose send fails before any receipt arrives.
//  2. Probe a committed nonce and await the reported result.
//  3. Assert the result is failed and carries the send error.
//  4. Assert one refused timeout vote was posted and recorded as started then completed, so no pending nonce is left behind.
//  5. Assert the ledger attempt is not acknowledged, the quiet settle was armed before and again after the vote and the runner's hold was released.
func TestFailedProbeVotesItsNonceRefused(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	failure := errors.New("connection refused")
	fixture := newFixture(t, func(context.Context, registry.EscrowSession, *user.PreparedInference, io.Writer, func()) (*host.HostResponse, error) {
		return nil, failure
	})

	fixture.runner.Probe(fixture.request())
	result := fixture.await(t)

	if result.Outcome != OutcomeFailed || !errors.Is(result.Err, failure) {
		t.Fatalf("result = %+v, want a failed probe carrying the send error", result)
	}
	if fixture.poster.calls.Load() != 1 {
		t.Errorf("votes posted = %d, want 1", fixture.poster.calls.Load())
	}
	if len(fixture.timeouts.events) != 2 || fixture.timeouts.events[0].Kind != engine.TimeoutKindRefused || fixture.timeouts.events[0].Action != engine.TimeoutActionStarted {
		t.Errorf("timeout events = %+v, want a started then a completed refused vote", fixture.timeouts.events)
	}
	if len(fixture.probes.attempts) != 1 || fixture.probes.attempts[0].Acknowledged {
		t.Errorf("ledger attempts = %+v, want one unacknowledged attempt", fixture.probes.attempts)
	}
	if fixture.session.armed.Load() != 2 || fixture.escrows.released.Load() != 1 {
		t.Errorf("quiet settle armed %d times, holds released %d, want 2 and 1", fixture.session.armed.Load(), fixture.escrows.released.Load())
	}
}

// Test flow:
//  1. Bind a runner whose send receives a receipt and then fails.
//  2. Probe a committed nonce and await the reported result.
//  3. Assert the vote posted is an execution timeout, as the host had already acknowledged the nonce.
func TestProbeFailingAfterTheReceiptVotesAnExecutionTimeout(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	fixture := newFixture(t, func(_ context.Context, _ registry.EscrowSession, _ *user.PreparedInference, _ io.Writer, onReceipt func()) (*host.HostResponse, error) {
		onReceipt()
		return nil, errors.New("stream cut")
	})

	fixture.runner.Probe(fixture.request())
	fixture.await(t)

	if len(fixture.timeouts.events) == 0 || fixture.timeouts.events[0].Kind != engine.TimeoutKindExecution {
		t.Fatalf("timeout events = %+v, want an execution vote", fixture.timeouts.events)
	}
}

// Test flow:
//  1. Bind a runner whose escrow can no longer be acquired.
//  2. Probe a committed nonce.
//  3. Assert Probe reports false and leaves the assignment's hold to the caller.
//  4. Assert a runner never bound, and a request carrying params of the wrong type, are refused the same way.
func TestProbeIsRefusedWhenItCannotBeSent(t *testing.T) {
	fixture := newFixture(t, servedAnswer())
	fixture.escrows.held = false

	if fixture.runner.Probe(fixture.request()) {
		t.Fatal("Probe = true for an escrow nobody holds, want false")
	}
	if fixture.held.Load() != 0 {
		t.Errorf("assignment holds released = %d, want the caller to keep it", fixture.held.Load())
	}

	if New(steppedClock(), roomyBounds).Probe(fixture.request()) {
		t.Error("Probe = true on a runner never bound, want false")
	}
	fixture.escrows.held = true
	wrong := fixture.request()
	wrong.Params = "not params"
	if fixture.runner.Probe(wrong) {
		t.Error("Probe = true for params of the wrong type, want false")
	}
}

// Test flow:
//  1. Bind a runner whose send never returns until its context ends, with a lifetime that is cancelled.
//  2. Probe a committed nonce, then cancel the lifetime.
//  3. Assert the probe is reported failed within the budget, so a stopping gateway does not leave the goroutine behind.
func TestProbeEndsWithTheGateway(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	lifetime, stop := context.WithCancel(t.Context())
	fixture := newFixtureWithin(t, lifetime, func(ctx context.Context, _ registry.EscrowSession, _ *user.PreparedInference, _ io.Writer, _ func()) (*host.HostResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	fixture.runner.Probe(fixture.request())
	stop()
	result := fixture.await(t)

	if result.Outcome != OutcomeFailed || !errors.Is(result.Err, context.Canceled) {
		t.Fatalf("result = %+v, want a failed probe ended by the gateway's stop", result)
	}
}

// Test flow:
//  1. Build probe params from a client request and keep a copy of the client's body.
//  2. Bind a runner whose send streams a usage of 700 completion tokens, probe the nonce and await the result.
//  3. Assert the ledger holds exactly one attempt, the probe's own, filed under `TerminalShadowProbe`, sent and counted for the usage the host streamed.
//  4. Assert the client's params and body are as they were, so the probe left the client's charge inputs alone, and the probe's body is a body of its own.
func TestProbeIsTheOnlyLedgerLineAndLeavesTheClientRequestAlone(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	fixture := newFixture(t, servedAnswer("data: {\"choices\":[],\"usage\":{\"completion_tokens\":700}}\n\ndata: [DONE]\n\n"))
	client := clientRequest(clientBody)
	before := bytes.Clone(client.Prompt)
	params, skip := fixture.runner.Params(queuedProfile(client), 1_700_000_000)
	request := fixture.request()
	request.Params = params

	if skip != scheduler.ProbeSkipNone || !fixture.runner.Probe(request) {
		t.Fatalf("skip = %v, want a probe that started", skip)
	}
	result := fixture.await(t)

	if len(fixture.probes.attempts) != 1 {
		t.Fatalf("ledger attempts = %+v, want exactly the probe's", fixture.probes.attempts)
	}
	attempt := fixture.probes.attempts[0]
	if attempt.Terminal != accounting.TerminalShadowProbe || attempt.Nonce != result.Nonce || attempt.OutputTokens != 700 || !attempt.Sent {
		t.Errorf("attempt = %+v, want the probe's nonce %d under %s with 700 output tokens", attempt, result.Nonce, accounting.TerminalShadowProbe)
	}
	if !bytes.Equal(client.Prompt, before) || client.MaxTokens != 50 || client.InputLength != uint64(len(before)) {
		t.Errorf("client params = %+v, want the request as it was queued", client)
	}
	if probe := params.(user.InferenceParams); bytes.Equal(probe.Prompt, client.Prompt) || &probe.Prompt[0] == &client.Prompt[0] {
		t.Errorf("probe body = %s, want a body of its own", probe.Prompt)
	}
}

type manualClock struct {
	mu      sync.Mutex
	current time.Time
}

func newManualClock() *manualClock {
	return &manualClock{current: time.Unix(1_700_000_000, 0)}
}

func (c *manualClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *manualClock) advance(step time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.current = c.current.Add(step)
}

const (
	contentEvent = "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"
	usageEvent10 = "data: {\"choices\":[],\"usage\":{\"completion_tokens\":10}}\n\n"
	doneEvent    = "data: [DONE]\n\n"
)

func timedAnswer(clock *manualClock, receiptAfter, gap time.Duration, confirmedAfter time.Duration) sendFunc {
	return func(_ context.Context, _ registry.EscrowSession, _ *user.PreparedInference, answer io.Writer, onReceipt func()) (*host.HostResponse, error) {
		sent := clock.now()
		clock.advance(receiptAfter)
		onReceipt()
		_, _ = answer.Write([]byte(contentEvent))
		clock.advance(gap)
		_, _ = answer.Write([]byte(usageEvent10))
		_, _ = answer.Write([]byte(doneEvent))
		return &host.HostResponse{Receipt: []byte("receipt"), ConfirmedAt: sent.Add(confirmedAfter).Unix()}, nil
	}
}

// Test flow:
//  1. Bind a runner on a clock the send controls, whose send receipts after 3s, streams one content event, stalls 6s, then streams a usage of 10 tokens, with a host stamp a minute ahead.
//  2. Probe a committed nonce and await the result.
//  3. Assert the ledger attempt carries the slow receipt, slow chunk, slow decode and clock drift flags a client line would carry for the same stream.
func TestProbeLineCarriesTheTimingFlagsAClientLineCarries(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	clock := newManualClock()
	fixture := newFixture(t, timedAnswer(clock, 3*time.Second, 6*time.Second, time.Minute))
	fixture.runner.now = clock.now

	fixture.runner.Probe(fixture.request())
	fixture.await(t)

	attempt := fixture.probes.attempts[0]
	if !attempt.SlowReceipt || !attempt.SlowChunk || !attempt.SlowDecode || !attempt.ClockDrifted {
		t.Errorf("attempt = %+v, want slow receipt, slow chunk, slow decode and clock drift set", attempt)
	}
}

// Test flow:
//  1. Bind a runner on a clock the send controls, whose send receipts at once, streams without a stall, with a host stamp inside the round trip.
//  2. Probe a committed nonce and await the result.
//  3. Assert the ledger attempt carries none of the timing flags.
func TestProbeLineOfAPromptAnswerCarriesNoTimingFlags(t *testing.T) {
	defer leakcheck.VerifyNoneStarted(t)()
	clock := newManualClock()
	fixture := newFixture(t, timedAnswer(clock, 0, 0, 0))
	fixture.runner.now = clock.now

	fixture.runner.Probe(fixture.request())
	fixture.await(t)

	attempt := fixture.probes.attempts[0]
	if attempt.SlowReceipt || attempt.SlowChunk || attempt.SlowDecode || attempt.ClockDrifted {
		t.Errorf("attempt = %+v, want no timing flag set", attempt)
	}
}
