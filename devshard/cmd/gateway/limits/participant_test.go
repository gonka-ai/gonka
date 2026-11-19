package limits

import (
	"math"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
)

var testEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func fixedNow(instant time.Time) func() time.Time {
	return func() time.Time { return instant }
}

// movingClock lets a test advance time between calls to cross a cutoff's openUntil deterministically.
type movingClock struct {
	mu sync.Mutex
	t  time.Time
}

func newMovingClock(start time.Time) *movingClock {
	return &movingClock{t: start}
}

func (c *movingClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *movingClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func zeroJitter(time.Duration) time.Duration { return 0 }

// oneToken prices a request at a single token on each dimension.
var oneToken = TokenCost{Input: 1, Output: 1}

func testConfig() ParticipantConfig {
	return ParticipantConfig{
		Pricing: WindowPricing{
			Input:                 RequestBounds{Min: 1, Initial: 4},
			Output:                RequestBounds{Min: 1, Initial: 4},
			FallbackContextTokens: 1,
			FallbackOutputTokens:  1,
		},
		Factors:       CongestionFactors{Soft: 0.85, Hard: 0.70, Severe: 0.50, Cross: 0.90},
		Slack:         0.30,
		AfterFailures: 3,
		BaseOpen:      1 * time.Second,
		MaxOpen:       10 * time.Second,
	}
}

// newTestLimiter builds a limiter with deterministic (zero) jitter so backoff assertions are exact.
func newTestLimiter(cfg ParticipantConfig, now func() time.Time) *ParticipantLimiter {
	l := NewParticipantLimiter(cfg, now)
	l.jitter = zeroJitter
	return l
}

func (l *ParticipantLimiter) admitOne(participant, model string) (func(), bool) {
	release, admission := l.Acquire(participant, model, oneToken)
	return release, admission == AdmissionOpen
}

func (l *ParticipantLimiter) acquired(t *testing.T, participant, model string) func() {
	t.Helper()
	release, admitted := l.admitOne(participant, model)
	if !admitted || release == nil {
		t.Fatalf("Acquire(%q, %q) refused, want a lease: the test needs this request in flight", participant, model)
	}
	return release
}

func (l *ParticipantLimiter) acquiredAndReleased(t *testing.T, participant, model string) {
	t.Helper()
	l.acquired(t, participant, model)()
}

func (l *ParticipantLimiter) admits(participant, model string) bool {
	_, admitted := l.admitOne(participant, model)
	return admitted
}

// answered posts one verdict for a request that carried a single token on each dimension.
func (l *ParticipantLimiter) answered(participant, model string, verdict Verdict) {
	l.OnResult(Result{Participant: participant, Model: model, Verdict: verdict, Carried: oneToken})
}

func (l *ParticipantLimiter) windowsOf(t *testing.T, participant, model string) (input, output float64) {
	t.Helper()
	state, tracked := l.states[key{participant: participant, model: model}]
	if !tracked {
		t.Fatalf("participant %q is not tracked for model %q", participant, model)
	}
	return state.input.tokens, state.output.tokens
}

func withinTolerance(got, want time.Duration) bool {
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff <= time.Millisecond
}

// Test flow:
//  1. Build a limiter from the default test config, whose window holds four one-token requests.
//  2. Acquire four one-token requests for the same participant and model.
//  3. Assert every one of the four is admitted.
//  4. Assert a fifth request is refused with the window full.
func TestAcquireAdmitsUpToWindowThenBlocks(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	for i := range 4 {
		if !l.admits("p", "m") {
			t.Fatalf("Acquire() call %d = false, want true (a four-token window takes four one-token requests)", i+1)
		}
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() with the window full = true, want false")
	}
}

// Test flow:
//  1. Build a limiter from the default test config.
//  2. Acquire a request whose token cost is far larger than the host's whole window while the host has nothing in flight.
//  3. Assert it is admitted anyway, since an idle host must not refuse a prompt no window fits.
//  4. Assert a further request behind it is refused, since the host is over its window until the oversized request ends.
func TestAHostWithNothingInFlightAdmitsARequestLargerThanItsWholeWindow(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	if _, admission := l.Acquire("p", "m", TokenCost{Input: 1_000_000, Output: 1_000_000}); admission != AdmissionOpen {
		t.Fatalf("Acquire() of a request larger than the window on an idle host = %s, want open: a prompt no window fits must not become one no host can ever serve", admission)
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() behind the oversized request = true, want false: the host is over its window until that one ends")
	}
}

// Test flow:
//  1. Acquire the window's four one-token requests, keeping the release for the first.
//  2. Release that first lease.
//  3. Assert another one-token request is now admitted.
func TestReleaseAllowsAnotherAcquire(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	release, _ := l.admitOne("p", "m")
	for range 3 {
		l.admitOne("p", "m")
	}

	release()

	if !l.admits("p", "m") {
		t.Fatal("Acquire() after a lease gave its tokens back = false, want true")
	}
}

// Test flow:
//  1. Acquire the window's four one-token requests, keeping the release for the first.
//  2. Call that release twice.
//  3. Assert exactly one further request is admitted.
//  4. Assert a second further request is refused, since a lease released twice must not hand back tokens it never took.
func TestReleasingALeaseTwiceGivesBackOnlyWhatItTook(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	release, _ := l.admitOne("p", "m")
	for range 3 {
		l.admitOne("p", "m")
	}

	release()
	release()

	if !l.admits("p", "m") {
		t.Fatal("Acquire() after the released lease = false, want true")
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() beyond the window = true, want false: a lease released twice must not hand back tokens it never took")
	}
}

// Test flow:
//  1. Acquire a single one-token request, leaving the host's peak in-flight count (1) below half the window (2), the growth gate.
//  2. Post a Success verdict carrying a full window's worth of tokens.
//  3. Assert both windows stay unchanged at their initial value, since an idle host must not accumulate an imaginary window.
func TestSuccessBelowUtilizationGateLeavesWindowUnchanged(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")

	l.OnResult(Result{Participant: "p", Model: "m", Verdict: Success, Carried: TokenCost{Input: 4, Output: 4}})

	input, output := l.windowsOf(t, "p", "m")
	if input != 4 || output != 4 {
		t.Fatalf("windows after a sub-gate Success = (%v, %v), want both unchanged at 4: an idle host must not accumulate an imaginary window", input, output)
	}
}

// Test flow:
//  1. Acquire two one-token requests, meeting the growth gate of half the window (peak 2 against window/2 = 2).
//  2. Post a Success verdict carrying a full window's worth of tokens.
//  3. Assert both windows grow by one step (from 4 to 5).
func TestAnAnswerCarryingAWindowsWorthOfTokensEarnsOneStep(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.admitOne("p", "m")

	l.OnResult(Result{Participant: "p", Model: "m", Verdict: Success, Carried: TokenCost{Input: 4, Output: 4}})

	input, output := l.windowsOf(t, "p", "m")
	if input != 5 || output != 5 {
		t.Fatalf("windows after an answer carrying a whole window = (%v, %v), want both 5: growth is one step per window's worth of tokens", input, output)
	}
}

// Test flow:
//  1. Acquire one request for a participant and model.
//  2. Post an Overload verdict for it.
//  3. Assert both windows narrow to the soft congestion factor (4 × 0.85 = 3.4), since an Overload blames neither dimension and no cross factor applies.
func TestOverloadNarrowsBothWindowsSoftly(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")

	l.answered("p", "m", Overload)

	input, output := l.windowsOf(t, "p", "m")
	if input != 3.4 || output != 3.4 {
		t.Fatalf("windows after Overload = (%v, %v), want both 3.4 (4 × 0.85): a host saying slow down blames neither dimension, so no cross factor applies", input, output)
	}
}

// Test flow:
//  1. Configure the minimum window pricing at 1 for both dimensions.
//  2. Acquire one request and post an Overload verdict for it.
//  3. Assert both windows land at the floor of 1.
func TestOverloadFloorsWindowAtTheMinimum(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.Pricing.Input.Initial, cfg.Pricing.Output.Initial = 1, 1
	l := newTestLimiter(cfg, fixedNow(testEpoch))
	l.admitOne("p", "m")

	l.answered("p", "m", Overload)

	input, output := l.windowsOf(t, "p", "m")
	if input != 1 || output != 1 {
		t.Fatalf("windows after Overload at the floor = (%v, %v), want both the minimum 1", input, output)
	}
}

// Test flow:
//  1. Repeat ten times: acquire and release a request, then post an Overload verdict for it once the host is idle.
//  2. Assert the cutoff never opens, since `OnResult` leaves counting a race-end overload to `CountRefusalIfIdle`.
func TestAnOverloadReportedAtRaceEndNeverTripsCutoff(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	for range 10 {
		l.acquiredAndReleased(t, "p", "m")
		l.answered("p", "m", Overload)
	}

	if admission := l.Admits("p", "m"); admission == AdmissionCutOff {
		t.Fatal("Admits() after repeated Overload results on an idle host = cut_off, want the cut-off closed: OnResult leaves the count to CountRefusalIfIdle")
	}
}

// Test flow:
//  1. Repeat three times: acquire and release a request, then call `CountRefusalIfIdle` while the host carries nothing else.
//  2. Assert the cutoff opens, since a host refusing while idle for this model is broken, not busy.
func TestRefusalsFromAnIdleHostOpenTheCutoff(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	for range 3 {
		l.acquiredAndReleased(t, "p", "m")
		l.CountRefusalIfIdle("p", "m")
	}

	if admission := l.Admits("p", "m"); admission != AdmissionCutOff {
		t.Fatalf("Admits() after three refusals with nothing else in flight = %s, want cut_off: a host refusing while it carried nothing of ours for this model is broken, not busy", admission)
	}
}

// Test flow:
//  1. Acquire one request and keep it in flight.
//  2. Repeat three times: acquire and release another request, then call `CountRefusalIfIdle`.
//  3. Assert the cutoff stays closed, since a host busy with other work is busy, not broken.
func TestRefusalsWhileTheHostCarriesOtherWorkLeaveTheCutoffClosed(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.acquired(t, "p", "m")
	for range 3 {
		l.acquiredAndReleased(t, "p", "m")
		l.CountRefusalIfIdle("p", "m")
	}

	if admission := l.Admits("p", "m"); admission == AdmissionCutOff {
		t.Fatal("Admits() after three refusals while another request was in flight = cut_off, want the cut-off closed: a host busy with our work is busy, not broken")
	}
}

// Test flow:
//  1. Post a TransportFault, an Overload, then two more TransportFault verdicts in sequence.
//  2. Assert the cutoff is open, since a refusing host must not reset its own fault streak by answering an Overload in between.
func TestAnOverloadDoesNotClearTheTransportFaultStreak(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	l.answered("p", "m", TransportFault)
	l.answered("p", "m", Overload)
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	if admission := l.Admits("p", "m"); admission != AdmissionCutOff {
		t.Fatalf("Admits() after fault, overload, fault, fault = %s, want cut_off: a refusing host must not reset its own streak", admission)
	}
}

// Test flow:
//  1. Mark the pair half-open and acquire/release its one probe.
//  2. Call `CountRefusalIfIdle` for that probe.
//  3. Assert the cutoff reopens, since a half-open probe gets exactly one try.
func TestARefusalOnAHalfOpenProbeReopensTheCutoff(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.markHalfOpen("p", "m")
	l.acquiredAndReleased(t, "p", "m")

	l.CountRefusalIfIdle("p", "m")

	if admission := l.Admits("p", "m"); admission != AdmissionCutOff {
		t.Fatalf("Admits() after the half-open probe was refused = %s, want cut_off: a probe gets exactly one try", admission)
	}
}

// Test flow:
//  1. Call `CountRefusalIfIdle` three times for a participant that was never acquired.
//  2. Assert the cutoff opens for it, since a pair swept between release and refusal must still be tracked as idle.
func TestARefusalFromAHostNeverSeenCreatesItsState(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	for range 3 {
		l.CountRefusalIfIdle("never-acquired", "m")
	}

	if admission := l.Admits("never-acquired", "m"); admission != AdmissionCutOff {
		t.Fatalf("Admits() after three refusals from an untracked host = %s, want cut_off: a pair swept between release and refusal was idle", admission)
	}
}

// Test flow:
//  1. Build a config with the default AfterFailures of 3, acquire one request.
//  2. Post alternating TransportFault and UpstreamFault verdicts for AfterFailures-1 rounds, since a host answering 5xx between resets must not clear the fault streak.
//  3. Assert both windows narrowed twice at the hard congestion factor.
//  4. Post one more TransportFault (the threshold-th).
//  5. Assert the cutoff is now open.
func TestUpstreamFaultNarrowsBothWindowsAndKeepsTheBreakersCount(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	l := newTestLimiter(cfg, fixedNow(testEpoch))
	l.admitOne("p", "m")

	for range cfg.AfterFailures - 1 {
		l.answered("p", "m", TransportFault)
		l.answered("p", "m", UpstreamFault)
	}
	hard := cfg.Factors.Hard
	floor := float64(cfg.Pricing.Input.Min)
	wantNarrowed := narrowedTo(narrowedTo(4, hard, floor), hard, floor)
	input, output := l.windowsOf(t, "p", "m")
	if input != wantNarrowed || output != wantNarrowed {
		t.Fatalf("windows after two 5xx answers = (%v, %v), want both %v: the host answered, so each one narrows at the hard factor", input, output, wantNarrowed)
	}

	l.answered("p", "m", TransportFault)
	if l.admits("p", "m") {
		t.Fatal("Acquire() after the threshold-th transport fault = true, want false (a 5xx must not clear the count that opens the cutoff)")
	}
}

// Test flow:
//  1. Acquire one request and post two TransportFault verdicts.
//  2. Post a MissedReceiptDeadline verdict.
//  3. Assert both windows narrow at the severe factor (4 × 0.5 = 2), since a receipt arrives before any prefill and lateness there blames the host, not a dimension.
//  4. Post one more TransportFault and assert the cutoff opens, since a missed deadline must not clear the fault streak.
func TestAMissedReceiptDeadlineNarrowsBothWindowsAndKeepsTheBreakersCount(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	l.answered("p", "m", MissedReceiptDeadline)

	input, output := l.windowsOf(t, "p", "m")
	if input != 2 || output != 2 {
		t.Fatalf("windows after a missed receipt deadline = (%v, %v), want both 2 (4 × 0.5): a receipt arrives before any prefill, so lateness blames the host, not a dimension", input, output)
	}
	l.answered("p", "m", TransportFault)
	if l.admits("p", "m") {
		t.Fatal("Acquire() after the threshold-th transport fault = true, want false (a missed deadline must not clear the breaker's count)")
	}
}

// Test flow:
//  1. Acquire one request.
//  2. Post a MissedFirstTokenDeadline verdict.
//  3. Assert the input window narrows at the hard factor (4 × 0.5 = 2), since the receipt arrived but the first content did not, which is prefill.
//  4. Assert the output window only takes the cross factor (4 × 0.9 = 3.6), since prefill and decode share one device.
func TestAMissedFirstTokenDeadlineBlamesPrefillAndOnlyGrazesDecode(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")

	l.answered("p", "m", MissedFirstTokenDeadline)

	input, output := l.windowsOf(t, "p", "m")
	if input != 2 {
		t.Fatalf("input window after a missed first token = %v, want 2 (4 × 0.5): the receipt arrived and the first content did not, which is prefill", input)
	}
	if output != 3.6 {
		t.Fatalf("output window after a missed first token = %v, want 3.6 (4 × 0.9): prefill and decode share one device, so the window that was not blamed still gives ground", output)
	}
}

// Test flow:
//  1. Acquire one request and post two TransportFault verdicts.
//  2. Post a DecodeStalled verdict.
//  3. Assert the output window narrows at the severe factor (4 × 0.5 = 2), since a stream gone silent between chunks is decode starved.
//  4. Assert the input window only takes the cross factor (4 × 0.9 = 3.6).
//  5. Assert a further request is still admitted, since a host that answered slowly is not one the cutoff is for.
func TestAStalledStreamBlamesDecodeAndLeavesTheCutoffAlone(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	l.answered("p", "m", DecodeStalled)

	input, output := l.windowsOf(t, "p", "m")
	if output != 2 {
		t.Fatalf("output window after a stall = %v, want 2 (4 × 0.5): a stream that went silent between chunks is decode starved", output)
	}
	if input != 3.6 {
		t.Fatalf("input window after a stall = %v, want 3.6 (4 × 0.9): the window that was not blamed takes the cross factor", input)
	}
	if !l.admits("p", "m") {
		t.Fatal("Acquire() after a stall = false, want true: a host that answered slowly is not one the cutoff is for, and the outlier detector already answers a host that stalls chronically")
	}
}

// Test flow:
//  1. Acquire two requests, meeting the growth gate that a timely Success would otherwise widen, then post two TransportFault verdicts.
//  2. Post a LateSuccess verdict carrying a full window's worth of tokens.
//  3. Assert both windows stay unchanged, since widening would undo the narrowing the lateness already earned.
//  4. Post one more TransportFault and assert a further request is still admitted, since the LateSuccess must have cleared the fault streak.
func TestALateSuccessKeepsTheWindowAndClearsTheBreakersCount(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	l.OnResult(Result{Participant: "p", Model: "m", Verdict: LateSuccess, Carried: TokenCost{Input: 4, Output: 4}})

	input, output := l.windowsOf(t, "p", "m")
	if input != 4 || output != 4 {
		t.Fatalf("windows after a LateSuccess at the utilisation gate = (%v, %v), want both unchanged at 4: widening would undo the narrowing the lateness earned", input, output)
	}
	l.answered("p", "m", TransportFault)
	if !l.admits("p", "m") {
		t.Fatal("Acquire() after one fault following a LateSuccess = false, want true (the answer must clear the breaker's count)")
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts, advance the clock past BaseOpen, and acquire the half-open probe.
//  2. Post a LateSuccess verdict for that probe.
//  3. Assert the snapshot reports the cutoff closed.
func TestALateSuccessLiftsAHalfOpenCutoff(t *testing.T) {
	t.Parallel()
	clock := newMovingClock(testEpoch)
	cfg := testConfig()
	l := newTestLimiter(cfg, clock.now)
	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	clock.advance(cfg.BaseOpen)
	l.admitOne("p", "m")

	l.answered("p", "m", LateSuccess)

	if got := l.Snapshot()[0].Cutoff; got != CutoffClosed {
		t.Fatalf("cutoff after a late answer to the probe = %q, want %q", got, CutoffClosed)
	}
}

// Test flow:
//  1. Acquire one request and post two TransportFault verdicts.
//  2. Post an EmptyAnswer verdict.
//  3. Assert both windows narrow to 2.8 (4 × 0.7).
//  4. Post one more TransportFault and assert the cutoff opens, since an empty answer must not clear the fault streak.
func TestAnEmptyAnswerNarrowsBothWindowsAndKeepsTheBreakersCount(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	l.answered("p", "m", EmptyAnswer)

	input, output := l.windowsOf(t, "p", "m")
	if input != 2.8 || output != 2.8 {
		t.Fatalf("windows after an empty answer = (%v, %v), want both 2.8 (4 × 0.7)", input, output)
	}
	l.answered("p", "m", TransportFault)
	if l.admits("p", "m") {
		t.Fatal("Acquire() after the threshold-th transport fault = true, want false (an empty answer must not clear the breaker's count)")
	}
}

// Test flow:
//  1. Acquire one request and post two TransportFault verdicts.
//  2. Post an EmptyAnswerLeftOpen verdict.
//  3. Assert both windows narrow to the severe factor (4 × 0.5 = 2).
//  4. Assert a further request is refused, since an empty answer that left its nonce open counts towards the cutoff.
func TestAnEmptyAnswerLeftOpenNarrowsSeverelyAndCountsTowardsTheCutoff(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	l.answered("p", "m", TransportFault)

	l.answered("p", "m", EmptyAnswerLeftOpen)

	input, output := l.windowsOf(t, "p", "m")
	if input != 2 || output != 2 {
		t.Fatalf("windows after an empty answer that left its nonce open = (%v, %v), want both 2 (4 × 0.5)", input, output)
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() after the threshold-th fault = true, want false (an empty answer left open counts towards the cutoff)")
	}
}

// Test flow:
//  1. For each case, set both window minimums to 3 and acquire one request.
//  2. Post the case's verdict twenty times in a row; the table varies the verdict across every congestion and fault outcome that narrows a window.
//  3. Assert both windows land at the minimum of 3, not below it.
func TestNoNarrowingGoesBelowTheMinimumWindow(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		verdict Verdict
	}{
		{"overload", Overload},
		{"upstream fault", UpstreamFault},
		{"missed receipt deadline", MissedReceiptDeadline},
		{"missed first token deadline", MissedFirstTokenDeadline},
		{"decode stalled", DecodeStalled},
		{"empty answer", EmptyAnswer},
		{"empty answer left open", EmptyAnswerLeftOpen},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			cfg := testConfig()
			cfg.Pricing.Input.Min, cfg.Pricing.Output.Min = 3, 3
			l := newTestLimiter(cfg, fixedNow(testEpoch))
			l.admitOne("p", "m")

			for range 20 {
				l.answered("p", "m", testCase.verdict)
			}

			input, output := l.windowsOf(t, "p", "m")
			if input != 3 || output != 3 {
				t.Fatalf("windows after twenty narrowings = (%v, %v), want both the minimum 3: a host a run of bad answers narrowed still takes enough work to earn its window back", input, output)
			}
		})
	}
}

// Test flow:
//  1. Acquire two requests, meeting the growth gate that a healthy answer would otherwise widen.
//  2. Post a Success verdict carrying a full window's worth of tokens but reporting input pressure of 1.5 against a baseline of 1.
//  3. Assert the input window narrows at the hard factor (4 × 0.85 = 3.4), since a host slower than its own best is congested before it has failed anything.
//  4. Assert the output window takes the cross factor (4 × 0.9 = 3.6).
func TestADelayedButSuccessfulAnswerNarrowsInsteadOfWidening(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.admitOne("p", "m")

	l.OnResult(Result{
		Participant: "p", Model: "m", Verdict: Success,
		Carried:  TokenCost{Input: 4, Output: 4},
		Pressure: Pressure{Input: 1.5, Output: 1},
	})

	input, output := l.windowsOf(t, "p", "m")
	if input != 3.4 {
		t.Fatalf("input window after a slow but successful answer = %v, want 3.4 (4 × 0.85): a host slower than its own best is congested before it has failed anything", input)
	}
	if output != 3.6 {
		t.Fatalf("output window after a slow but successful answer = %v, want 3.6 (4 × 0.9): the window that was not blamed takes the cross factor", output)
	}
}

// Test flow:
//  1. Build a limiter whose config allows 0.30 of slack, and acquire two requests to meet the growth gate.
//  2. Post a Success verdict whose pressure (1.29 on both dimensions) sits inside that slack.
//  3. Assert both windows still widen to 5, since ordinary jitter is not congestion.
func TestLatencyInsideTheSlackStillWidens(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.admitOne("p", "m")

	l.OnResult(Result{
		Participant: "p", Model: "m", Verdict: Success,
		Carried:  TokenCost{Input: 4, Output: 4},
		Pressure: Pressure{Input: 1.29, Output: 1.29},
	})

	input, output := l.windowsOf(t, "p", "m")
	if input != 5 || output != 5 {
		t.Fatalf("windows after an answer inside the slack = (%v, %v), want both 5: ordinary jitter is not congestion", input, output)
	}
}

// Test flow:
//  1. Build a config with the default AfterFailures of 3 and post TransportFault verdicts for AfterFailures-1 rounds.
//  2. Acquire and release one request, asserting it is still admitted since the cutoff has not yet opened.
//  3. Post the AfterFailures-th consecutive TransportFault verdict.
//  4. Assert a further request is refused, since the cutoff must now be open.
func TestTransportFaultTripsAtExactThreshold(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	l := newTestLimiter(cfg, fixedNow(testEpoch))

	for range cfg.AfterFailures - 1 {
		l.answered("p", "m", TransportFault)
	}
	release, admitted := l.admitOne("p", "m")
	if !admitted {
		t.Fatal("Acquire() before AfterFailures reached = false, want true (cutoff not yet open)")
	}
	release()

	l.answered("p", "m", TransportFault)

	if l.admits("p", "m") {
		t.Fatal("Acquire() at AfterFailures = true, want false (cutoff must open)")
	}
}

// Test flow:
//  1. Build a config with AfterFailures 1, BaseOpen 1s, and MaxOpen 3s.
//  2. Trip the cutoff four times, each after the previous one expired and left a half-open probe that failed, following the backoff ladder base × 1.6^n (1s, 1.6s, 2.56s, then capped at the 3s MaxOpen).
//  3. After each trip, assert the cutoff's openUntil matches the expected ladder value within tolerance.
func TestTransportFaultBackoffLadderGrowsThenCaps(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.AfterFailures, cfg.BaseOpen, cfg.MaxOpen = 1, 1*time.Second, 3*time.Second
	l := newTestLimiter(cfg, fixedNow(testEpoch))

	want := []time.Duration{
		1 * time.Second,
		time.Duration(1.6 * float64(time.Second)),
		time.Duration(2.56 * float64(time.Second)),
		3 * time.Second,
	}
	for i, wantBackoff := range want {
		if i > 0 {
			l.markHalfOpen("p", "m")
		}
		l.answered("p", "m", TransportFault)
		got := l.states[key{participant: "p", model: "m"}].openUntil.Sub(testEpoch)
		if !withinTolerance(got, wantBackoff) {
			t.Fatalf("trip %d: openUntil-now = %v, want %v", i+1, got, wantBackoff)
		}
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts and assert a request is refused immediately after.
//  2. Advance the clock past the trip's cooldown.
//  3. Assert one request is admitted as the half-open probe.
//  4. Assert a second concurrent request is refused, since exactly one probe is allowed.
func TestHalfOpenAllowsExactlyOneProbeAfterCooldown(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() immediately after trip = true, want false (cutoff open)")
	}

	state := l.states[key{participant: "p", model: "m"}]
	clock.advance(state.openUntil.Sub(clock.now()) + time.Millisecond)

	if !l.admits("p", "m") {
		t.Fatal("Acquire() after cooldown elapsed = false, want true (half-open probe)")
	}
	if l.admits("p", "m") {
		t.Fatal("second concurrent Acquire() during half-open = true, want false (exactly one probe allowed)")
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts and record the backoff count.
//  2. Advance the clock past the cooldown and acquire the half-open probe.
//  3. Post a Success verdict for that probe.
//  4. Assert the cutoff is no longer half-open, its openUntil is zero, and its backoff count decayed by one.
//  5. Release the probe and assert a further request is admitted.
func TestHalfOpenSuccessClosesCutoffAndDecaysBackoff(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	state := l.states[key{participant: "p", model: "m"}]
	backoffCountAfterTrip := state.backoffCount
	clock.advance(state.openUntil.Sub(clock.now()) + time.Millisecond)
	release, _ := l.admitOne("p", "m")

	l.answered("p", "m", Success)

	if state.halfOpen {
		t.Fatal("halfOpen after probe Success = true, want false (cutoff closed)")
	}
	if !state.openUntil.IsZero() {
		t.Fatalf("openUntil after probe Success = %v, want zero (cutoff fully closed)", state.openUntil)
	}
	if state.backoffCount != backoffCountAfterTrip-1 {
		t.Fatalf("backoffCount after probe Success = %d, want %d (decayed by one)", state.backoffCount, backoffCountAfterTrip-1)
	}
	release()
	if !l.admits("p", "m") {
		t.Fatal("Acquire() after cutoff closed = false, want true")
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts, record the first cooldown, and advance the clock past it.
//  2. Acquire the half-open probe.
//  3. Post a single TransportFault verdict for that probe, not AfterFailures-many.
//  4. Assert the cutoff fully reopens (no longer half-open) with a second cooldown longer than the first.
//  5. Assert a request right after reopening is refused.
func TestHalfOpenTransportFaultReopensWithLongerCooldown(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	state := l.states[key{participant: "p", model: "m"}]
	firstCooldown := state.openUntil.Sub(clock.now())
	clock.advance(firstCooldown + time.Millisecond)
	l.admitOne("p", "m")
	probeTime := clock.now()

	l.answered("p", "m", TransportFault)

	if state.halfOpen {
		t.Fatal("halfOpen after failed probe = true, want false (fully reopened)")
	}
	secondCooldown := state.openUntil.Sub(probeTime)
	if secondCooldown <= firstCooldown {
		t.Fatalf("second cooldown = %v, want longer than first cooldown %v", secondCooldown, firstCooldown)
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() right after re-opening = true, want false")
	}
}

// Test flow:
//  1. Acquire one request and post twenty ModelOutcome verdicts for it.
//  2. Assert both windows stay unchanged at 4.
//  3. Assert the cutoff state (openUntil, half-open flag, fault streak, backoff count) is entirely untouched.
func TestModelOutcomeNeverPenalizes(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")

	for range 20 {
		l.answered("p", "m", ModelOutcome)
	}

	state, exists := l.states[key{participant: "p", model: "m"}]
	if !exists {
		t.Fatal("state missing after Acquire")
	}
	if state.input.tokens != 4 || state.output.tokens != 4 {
		t.Fatalf("windows after ModelOutcome verdicts = (%v, %v), want both unchanged at 4", state.input.tokens, state.output.tokens)
	}
	if !state.openUntil.IsZero() || state.halfOpen || state.consecutiveCutoffFaults != 0 || state.backoffCount != 0 {
		t.Fatal("cutoff state moved after ModelOutcome verdicts, want fully unchanged")
	}
}

// Test flow:
//  1. Post a ModelOutcome verdict for a participant that was never acquired.
//  2. Assert no state was created for that participant and model, a true no-op.
func TestModelOutcomeWithoutPriorAcquireCreatesNoState(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	l.answered("never-acquired", "m", ModelOutcome)

	if _, exists := l.states[key{participant: "never-acquired", model: "m"}]; exists {
		t.Fatal("ModelOutcome created state for a host never Acquired, want a true no-op")
	}
}

// Test flow:
//  1. Start many goroutines, each with its own random source, that repeatedly pick a random participant, model, and verdict.
//  2. Each goroutine acquires a request and, if admitted, posts a random verdict and releases it.
//  3. Wait for every goroutine, relying on `go test -race` to catch unsynchronized access.
func TestParticipantLimiterConcurrentAccessIsRaceFree(t *testing.T) {
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	const goroutines = 50
	const iterationsPerGoroutine = 200
	participants := []string{"p1", "p2", "p3"}
	models := []string{"m1", "m2"}
	verdicts := []Verdict{Success, Overload, TransportFault, ModelOutcome}

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			source := rand.New(rand.NewSource(seed))
			for range iterationsPerGoroutine {
				participant := participants[source.Intn(len(participants))]
				model := models[source.Intn(len(models))]
				if release, admitted := l.admitOne(participant, model); admitted {
					l.answered(participant, model, verdicts[source.Intn(len(verdicts))])
					release()
				}
			}
		}(int64(g))
	}
	wg.Wait()
}

// Test flow:
//  1. Read the default cutoff MaxOpen and the default perf ejection horizon from `config.Defaults()`.
//  2. Assert the cutoff's MaxOpen stays strictly below the perf ejection horizon, so perf remains the pool authority, not the cutoff.
func TestCutoffMaxOpenDefaultStaysBelowPerfEjectionHorizon(t *testing.T) {
	t.Parallel()
	defaults := config.Defaults()
	cutoffMaxOpen := time.Duration(defaults.Limits.HostCutoff.MaxMS) * time.Millisecond
	perfEjectionMax := time.Duration(defaults.Perf.EjectionMaxSeconds) * time.Second

	if cutoffMaxOpen >= perfEjectionMax {
		t.Fatalf("cutoff MaxMS default %v must stay below perf's ejection horizon %v", cutoffMaxOpen, perfEjectionMax)
	}
}

// Test flow:
//  1. Call `Available` on a participant and model that were never acquired.
//  2. Assert it reports true, since a fresh window is available.
//  3. Assert the call created no state for that pair.
func TestAvailableTrueOnFreshParticipant(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	if !l.Available("p", "m") {
		t.Fatal("Available() on a never-seen participant = false, want true (fresh full window)")
	}
	if _, exists := l.states[key{participant: "p", model: "m"}]; exists {
		t.Fatal("Available() on a never-seen participant created state, want no-op")
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts.
//  2. Assert `Available` reports false while the cutoff is open.
//  3. Advance the clock past the cooldown.
//  4. Assert `Available` reports true (a half-open probe is available), and assert acquiring right after still succeeds, since the peek must not consume the probe.
func TestAvailableFalseWhileCutoffOpenThenTrueAfterCooldown(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	if l.Available("p", "m") {
		t.Fatal("Available() while cutoff Open = true, want false")
	}

	state := l.states[key{participant: "p", model: "m"}]
	clock.advance(state.openUntil.Sub(clock.now()) + time.Millisecond)

	if !l.Available("p", "m") {
		t.Fatal("Available() after cooldown elapsed = false, want true (half-open probe available)")
	}
	if !l.admits("p", "m") {
		t.Fatal("Acquire() after Available() reported half-open = false, want true (peek must not consume the probe)")
	}
}

// Test flow:
//  1. Trip the cutoff with AfterFailures TransportFault verdicts and advance the clock past the cooldown.
//  2. Acquire the half-open probe, keeping it in flight.
//  3. Assert `Available` reports false while that probe is still outstanding.
func TestAvailableFalseDuringHalfOpenProbeInFlight(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	clock := newMovingClock(testEpoch)
	l := newTestLimiter(cfg, clock.now)

	for range cfg.AfterFailures {
		l.answered("p", "m", TransportFault)
	}
	state := l.states[key{participant: "p", model: "m"}]
	clock.advance(state.openUntil.Sub(clock.now()) + time.Millisecond)
	if !l.admits("p", "m") {
		t.Fatal("Acquire() for the half-open probe = false, want true")
	}

	if l.Available("p", "m") {
		t.Fatal("Available() with the half-open probe in flight = true, want false")
	}
}

// Test flow:
//  1. Acquire the window's four one-token requests, keeping the release for the first.
//  2. Assert `Available` reports false with the window full.
//  3. Release the first lease.
//  4. Assert `Available` reports true again.
func TestAvailableFalseAtWindowThenTrueAfterRelease(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	release, _ := l.admitOne("p", "m")
	for range 3 {
		l.admitOne("p", "m")
	}
	if l.Available("p", "m") {
		t.Fatal("Available() with the window full = true, want false")
	}

	release()

	if !l.Available("p", "m") {
		t.Fatal("Available() after a lease gave its tokens back = false, want true")
	}
}

// Test flow:
//  1. Call `Available` ten times on a never-acquired pair and assert it created no state.
//  2. Acquire four one-token requests and assert every one is admitted.
//  3. Assert a fifth is refused, showing the earlier `Available` peeks never consumed tokens.
func TestAvailableDoesNotConsumeWindowTokens(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	for range 10 {
		l.Available("p", "m")
	}
	if _, exists := l.states[key{participant: "p", model: "m"}]; exists {
		t.Fatal("Available() created participant state as a side effect, want no-op until first Acquire")
	}

	for i := range 4 {
		if !l.admits("p", "m") {
			t.Fatalf("Acquire() call %d after repeated Available() peeks = false, want true (peeks must not consume tokens)", i+1)
		}
	}
	if l.admits("p", "m") {
		t.Fatal("Acquire() beyond the window after repeated Available() peeks = true, want false")
	}
}

// Test flow:
//  1. Acquire one request and post a TransportFault verdict, then snapshot the resulting state.
//  2. Call `Available` ten times.
//  3. Assert the state afterwards is byte-for-byte identical to the snapshot taken before.
func TestAvailableLeavesExistingStateUnchanged(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.answered("p", "m", TransportFault)
	before := *l.states[key{participant: "p", model: "m"}]

	for range 10 {
		l.Available("p", "m")
	}

	after := *l.states[key{participant: "p", model: "m"}]
	if before != after {
		t.Fatalf("Available() mutated existing state: before=%+v after=%+v", before, after)
	}
}

// Test flow:
//  1. Start many goroutines, each with its own random source, that repeatedly pick a random participant, model, and verdict.
//  2. Each goroutine calls `Available`, then acquires a request and, if admitted, posts a random verdict and releases it.
//  3. Wait for every goroutine, relying on `go test -race` to catch unsynchronized access.
func TestAvailableConcurrentWithAcquireIsRaceFree(t *testing.T) {
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))

	const goroutines = 50
	const iterationsPerGoroutine = 200
	participants := []string{"p1", "p2", "p3"}
	models := []string{"m1", "m2"}
	verdicts := []Verdict{Success, Overload, TransportFault, ModelOutcome}

	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Add(1)
		go func(seed int64) {
			defer wg.Done()
			source := rand.New(rand.NewSource(seed))
			for range iterationsPerGoroutine {
				participant := participants[source.Intn(len(participants))]
				model := models[source.Intn(len(models))]
				l.Available(participant, model)
				if release, admitted := l.admitOne(participant, model); admitted {
					l.answered(participant, model, verdicts[source.Intn(len(verdicts))])
					release()
				}
			}
		}(int64(g))
	}
	wg.Wait()
}

// Test flow:
//  1. Build a config where a single TransportFault trips a cutoff for a full minute.
//  2. Trip host-a on model-a and model-b, and host-b on model-a.
//  3. Assert host-a/model-a is unavailable before the quarantine is cleared.
//  4. Call `ClearQuarantine` on host-a and assert it reports true.
//  5. Assert host-a is available again on both models, while host-b/model-a stays unavailable.
//  6. Assert `ClearQuarantine` on a never-seen host reports false.
func TestClearQuarantineReopensEveryModelsCutoffForOneParticipant(t *testing.T) {
	now := time.Now()
	cfg := testConfig()
	cfg.AfterFailures, cfg.BaseOpen, cfg.MaxOpen = 1, time.Minute, time.Minute
	limiter := newTestLimiter(cfg, func() time.Time { return now })

	limiter.answered("host-a", "model-a", TransportFault)
	limiter.answered("host-a", "model-b", TransportFault)
	limiter.answered("host-b", "model-a", TransportFault)
	if limiter.Available("host-a", "model-a") {
		t.Fatal("host-a/model-a is available before the quarantine is cleared")
	}

	if !limiter.ClearQuarantine("host-a") {
		t.Fatal("ClearQuarantine(host-a) = false, want true for a tracked participant")
	}
	if !limiter.Available("host-a", "model-a") || !limiter.Available("host-a", "model-b") {
		t.Fatal("host-a is still unavailable after its quarantine was cleared")
	}
	if limiter.Available("host-b", "model-a") {
		t.Fatal("host-b was reopened by clearing host-a's quarantine")
	}
	if limiter.ClearQuarantine("host-never-seen") {
		t.Fatal("ClearQuarantine(unknown) = true, want false")
	}
}

// Test flow:
//  1. Define a `grow` helper that acquires two requests, then either releases the first lease before posting a Success `OnResult` or posts the result before releasing, and reads back the resulting input window from the snapshot.
//  2. Run `grow` once with the release first and once with the result first.
//  3. Assert both orders report the same window, since the engine's release-in-a-defer pattern must not change the growth decision.
//  4. Assert that window actually grew past the initial 4.
func TestWindowGrowthDoesNotDependOnReleaseOrder(t *testing.T) {
	base := time.Unix(0, 0)
	grow := func(releaseFirst bool) float64 {
		limiter := newTestLimiter(testConfig(), func() time.Time { return base })
		var first func()
		for range 2 {
			release, admitted := limiter.admitOne("host-a", "model-a")
			if !admitted {
				t.Fatal("Acquire refused inside the initial window")
			}
			if first == nil {
				first = release
			}
		}
		answer := Result{
			Participant: "host-a", Model: "model-a", Verdict: Success,
			Carried: TokenCost{Input: 4, Output: 4},
		}
		if releaseFirst {
			first()
			limiter.OnResult(answer)
		} else {
			limiter.OnResult(answer)
			first()
		}
		for _, host := range limiter.Snapshot() {
			if host.Participant == "host-a" && host.Model == "model-a" {
				return host.InputWindowTokens
			}
		}
		t.Fatal("the limiter reported no window for the host it just admitted")
		return 0
	}

	released, reported := grow(true), grow(false)
	if released != reported {
		t.Fatalf("window = %v when the lease was released first and %v when OnResult ran first; the order must not matter", released, reported)
	}
	if released <= 4 {
		t.Fatalf("window = %v, want growth past the initial 4: half a four-token window is the growth threshold", released)
	}
}

// windowOf reads one participant's current input window out of the snapshot the collector also reads.
func windowOf(t *testing.T, limiter *ParticipantLimiter, participant string) float64 {
	t.Helper()
	for _, window := range limiter.Snapshot() {
		if window.Participant == participant {
			return window.InputWindowTokens
		}
	}
	t.Fatalf("participant %q absent from the snapshot", participant)
	return 0
}

// Test flow:
//  1. Acquire one request and post an Overload verdict, shrinking the window below the initial 4.
//  2. Reconfigure the limiter with a new initial window of 32.
//  3. Assert the tracked participant's window is lifted to the new initial 32.
func TestReconfigureLiftsAWindowThatCollapsedBelowTheNewInitial(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	limiter.admitOne("host-a", "model-a")
	limiter.answered("host-a", "model-a", Overload)
	if collapsed := windowOf(t, limiter, "host-a"); collapsed >= 4 {
		t.Fatalf("window = %v, want it shrunk below the initial 4 by the overload", collapsed)
	}

	lifted := testConfig()
	lifted.Pricing.Input.Initial, lifted.Pricing.Output.Initial = 32, 32
	limiter.Reconfigure(lifted)

	if got := windowOf(t, limiter, "host-a"); got != 32 {
		t.Fatalf("window = %v, want the new initial 32", got)
	}
}

// Test flow:
//  1. Acquire one request and post an Overload verdict, shrinking the window below the initial 4.
//  2. Reconfigure the limiter with the very same window bounds, as an unrelated admin write does.
//  3. Assert the window stays where the overload left it: only a raised initial lifts a window, so an unrelated write cannot reopen a host the gateway just backed off from.
func TestReconfigureWithUnchangedBoundsKeepsANarrowedWindow(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	limiter.admitOne("host-a", "model-a")
	limiter.answered("host-a", "model-a", Overload)
	narrowed := windowOf(t, limiter, "host-a")

	limiter.Reconfigure(testConfig())

	if got := windowOf(t, limiter, "host-a"); got != narrowed {
		t.Fatalf("window = %v, want the narrowed %v kept", got, narrowed)
	}
}

// Test flow:
//  1. Acquire and answer forty Success results, growing the window well past 8.
//  2. Reconfigure the limiter with a new initial window of 1.
//  3. Assert the tracked participant's window is left at the value it had already grown to, since a lowered initial is a floor, not a ceiling.
func TestReconfigureLeavesAWindowThatAlreadyGrewPastTheNewInitial(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	for range 40 {
		limiter.admitOne("host-a", "model-a")
		limiter.OnResult(Result{
			Participant: "host-a", Model: "model-a", Verdict: Success,
			Carried: TokenCost{Input: 64, Output: 64},
		})
	}
	grown := windowOf(t, limiter, "host-a")
	if grown <= 8 {
		t.Fatalf("window = %v, want it grown well past 8 by the successes", grown)
	}

	narrowed := testConfig()
	narrowed.Pricing.Input.Initial, narrowed.Pricing.Output.Initial = 1, 1
	limiter.Reconfigure(narrowed)

	if got := windowOf(t, limiter, "host-a"); got != grown {
		t.Fatalf("window = %v, want the %v the host had earned: a lowered initial is a floor, not a ceiling", got, grown)
	}
}

// Test flow:
//  1. Build a config with AfterFailures 1, BaseOpen 1s, MaxOpen 3s, and a fixed jitter function of one fifth of the backoff.
//  2. Trip the cutoff four times, each after the previous one expired and its half-open probe failed, so the ladder saturates well before the fourth trip.
//  3. Assert the resulting openUntil equals MaxOpen plus its jitter, showing jitter survives clamping at the ceiling.
func TestSaturatedBackoffStillCarriesJitter(t *testing.T) {
	t.Parallel()
	cfg := testConfig()
	cfg.AfterFailures, cfg.BaseOpen, cfg.MaxOpen = 1, 1*time.Second, 3*time.Second
	l := NewParticipantLimiter(cfg, fixedNow(testEpoch))
	l.jitter = func(backoff time.Duration) time.Duration { return backoff / 5 }

	for trip := range 4 {
		if trip > 0 {
			l.markHalfOpen("p", "m")
		}
		l.answered("p", "m", TransportFault)
	}

	got := l.states[key{participant: "p", model: "m"}].openUntil.Sub(testEpoch)
	if !withinTolerance(got, cfg.MaxOpen+cfg.MaxOpen/5) {
		t.Fatalf("saturated backoff = %v, want %v (MaxOpen plus its jitter)", got, cfg.MaxOpen+cfg.MaxOpen/5)
	}
}

func idleEvictionConfig() ParticipantConfig {
	settings := testConfig()
	settings.IdleEviction = time.Hour
	return settings
}

func openWindow(participant string, inflight int64) HostWindow {
	return HostWindow{
		Participant: participant, Model: "model-a",
		InputWindowTokens: 4, OutputWindowTokens: 4,
		InflightInputTokens: inflight, InflightOutputTokens: inflight,
		Cutoff: CutoffClosed, Available: true,
	}
}

// Test flow:
//  1. Configure a one-hour idle eviction window.
//  2. Acquire and release a request for host-released, and acquire (without releasing) one for host-busy.
//  3. Advance the clock past the eviction window.
//  4. Acquire a request for host-new.
//  5. Assert the snapshot now contains only host-busy and host-new: the idle, fully released host-released pair was forgotten.
func TestAcquireForgetsAPairIdlePastTheEvictionWindow(t *testing.T) {
	t.Parallel()
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(idleEvictionConfig(), clock.now)
	release, admitted := limiter.admitOne("host-released", "model-a")
	require.True(t, admitted)
	release()
	require.True(t, limiter.admits("host-busy", "model-a"))

	clock.advance(time.Hour + time.Minute)
	require.True(t, limiter.admits("host-new", "model-a"))

	require.Equal(t, []HostWindow{
		openWindow("host-busy", 1),
		openWindow("host-new", 1),
	}, limiter.Snapshot())
}

// Test flow:
//  1. Configure a one-hour idle eviction window and a cutoff whose cooldown runs from 2 to 4 hours.
//  2. Trip host-probing's cutoff, advance the clock past its cooldown, and acquire/release its half-open probe.
//  3. Trip host-cut-off's cutoff too, without letting its cooldown expire.
//  4. Advance the clock past the idle eviction window and acquire a request for host-trigger.
//  5. Assert the snapshot keeps host-cut-off with its cutoff still open, while the idle, half-open host-probing pair was forgotten.
func TestAcquireForgetsAnIdleHalfOpenPairButKeepsARunningCutoff(t *testing.T) {
	t.Parallel()
	settings := idleEvictionConfig()
	settings.AfterFailures, settings.BaseOpen, settings.MaxOpen = 1, 2*time.Hour, 4*time.Hour
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(settings, clock.now)
	limiter.answered("host-probing", "model-a", TransportFault)
	clock.advance(2*time.Hour + time.Minute)
	release, admitted := limiter.admitOne("host-probing", "model-a")
	require.True(t, admitted, "an expired cut-off admits one probe")
	release()
	limiter.answered("host-cut-off", "model-a", TransportFault)

	clock.advance(time.Hour + time.Minute)
	require.True(t, limiter.admits("host-trigger", "model-a"))

	require.Equal(t, []HostWindow{
		{
			Participant: "host-cut-off", Model: "model-a",
			InputWindowTokens: 4, OutputWindowTokens: 4,
			Cutoff: CutoffOpen, BackoffCount: 1,
		},
		openWindow("host-trigger", 1),
	}, limiter.Snapshot())
}

// Test flow:
//  1. Configure a one-hour idle eviction window and acquire a request for host-pending.
//  2. Advance the clock past the eviction window, then release that lease.
//  3. Acquire a request for host-trigger, which runs the idle scan.
//  4. Assert the snapshot still holds both pairs, since a lease released just now stamps lastUsed and its verdict may still be pending.
func TestAcquireKeepsAPairJustReleasedEvenPastTheEvictionWindow(t *testing.T) {
	t.Parallel()
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(idleEvictionConfig(), clock.now)
	release, admitted := limiter.admitOne("host-pending", "model-a")
	require.True(t, admitted)

	clock.advance(time.Hour + time.Minute)
	release()
	require.True(t, limiter.admits("host-trigger", "model-a"))

	require.Len(t, limiter.Snapshot(), 2, "host-pending was released just now; its verdict may still be reported")
}

// Test flow:
//  1. Configure a one-hour idle eviction window, acquire and release a request for host-late-result.
//  2. Advance the clock 55 minutes and post a Success verdict for that pair.
//  3. Advance the clock 10 more minutes and acquire a request for host-trigger.
//  4. Assert the snapshot still holds both pairs, since `OnResult` refreshed lastUsed only 10 minutes ago, not the full 1h5m since release.
func TestAcquireKeepsAPairWhoseVerdictArrivedAfterItWasReleased(t *testing.T) {
	t.Parallel()
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(idleEvictionConfig(), clock.now)
	release, admitted := limiter.admitOne("host-late-result", "model-a")
	require.True(t, admitted)
	release()

	clock.advance(55 * time.Minute)
	limiter.answered("host-late-result", "model-a", Success)

	clock.advance(10 * time.Minute)
	require.True(t, limiter.admits("host-trigger", "model-a"))

	require.Len(t, limiter.Snapshot(), 2, "the verdict landed 10m ago, not 1h5m, because OnResult refreshed lastUsed")
}

// Test flow:
//  1. Configure a one-hour idle eviction window, acquire and release a request for host-idle.
//  2. Advance the clock 58 minutes and acquire for host-trigger, running the idle scan.
//  3. Advance the clock 3 more minutes and acquire for host-trigger again; assert host-idle is still present, since three minutes after the last scan is too soon for the next one.
//  4. Advance the clock 3 more minutes and acquire for host-trigger a third time; assert host-idle is now gone, since a full tenth of the window has elapsed since the last scan.
func TestAcquireScansForIdlePairsAtMostOncePerTenthOfTheWindow(t *testing.T) {
	t.Parallel()
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(idleEvictionConfig(), clock.now)
	release, admitted := limiter.admitOne("host-idle", "model-a")
	require.True(t, admitted)
	release()
	clock.advance(58 * time.Minute)
	require.True(t, limiter.admits("host-trigger", "model-a"))

	clock.advance(3 * time.Minute)
	require.True(t, limiter.admits("host-trigger", "model-a"))
	require.Len(t, limiter.Snapshot(), 2, "three minutes after the last scan, host-idle may not be forgotten yet")

	clock.advance(3 * time.Minute)
	require.True(t, limiter.admits("host-trigger", "model-a"))
	require.Len(t, limiter.Snapshot(), 1)
}

// Test flow:
//  1. Configure a one-minute idle eviction window shared by three participants.
//  2. Start one goroutine per participant that repeatedly advances a shared clock, acquires a request, and, if admitted, posts a Success verdict and releases it.
//  3. Wait for every goroutine, relying on `go test -race` to catch unsynchronized access between admission and idle eviction.
//  4. Assert every window in the final snapshot reports zero in-flight input and output tokens.
func TestIdleEvictionIsRaceFreeUnderConcurrentAdmission(t *testing.T) {
	settings := idleEvictionConfig()
	settings.IdleEviction = time.Minute
	clock := newMovingClock(testEpoch)
	limiter := newTestLimiter(settings, clock.now)
	participants := []string{"p1", "p2", "p3"}

	var workers sync.WaitGroup
	for _, participant := range participants {
		workers.Go(func() {
			for range 200 {
				clock.advance(time.Second)
				if release, admitted := limiter.admitOne(participant, "m"); admitted {
					limiter.answered(participant, "m", Success)
					release()
				}
			}
		})
	}
	workers.Wait()

	for _, window := range limiter.Snapshot() {
		require.Zero(t, window.InflightInputTokens, window.Participant)
		require.Zero(t, window.InflightOutputTokens, window.Participant)
	}
}

// pricedConfig prices windows in tokens the way production does.
func pricedConfig() ParticipantConfig {
	settings := testConfig()
	settings.Pricing.Input = RequestBounds{Min: 1, Initial: 4}
	settings.Pricing.Output = RequestBounds{Min: 1, Initial: 4}
	settings.Pricing.FallbackContextTokens = 1_000
	settings.Pricing.FallbackOutputTokens = 100
	return settings
}

// Test flow:
//  1. Build a limiter with the priced test config and observe a context length of 400,000 for "long-context".
//  2. Acquire one request each for "long-context" and an unnamed model governance never reported.
//  3. Assert the long-context input window is priced at the model's own context length (4 × 400,000) while its output window uses the fallback output tokens (4 × 100).
//  4. Assert the unnamed model's input window falls back to the configured default context length (4 × 1,000).
func TestAFreshHostIsPricedAtTheContextLengthGovernanceReports(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(pricedConfig(), fixedNow(testEpoch))
	l.ObserveModels(map[string]int64{"long-context": 400_000})

	l.Acquire("p", "long-context", oneToken)
	l.Acquire("p", "unnamed-model", oneToken)

	input, output := l.windowsOf(t, "p", "long-context")
	if input != 4*400_000 {
		t.Errorf("input window = %v, want %v: prefill is counted in the tokens of the model's own context length", input, 4*400_000)
	}
	if output != 4*100 {
		t.Errorf("output window = %v, want 400: decode is counted in the model's output cap, which governance does not set", output)
	}
	if unnamed, _ := l.windowsOf(t, "p", "unnamed-model"); unnamed != 4*1_000 {
		t.Errorf("input window of a model governance did not name = %v, want the default %v", unnamed, 4*1_000)
	}
}

// Test flow:
//  1. Configure an operator pin of 8,000 context tokens for "pinned".
//  2. Observe governance reporting a context length of 400,000 for the same model.
//  3. Acquire one request for "pinned".
//  4. Assert its input window is priced at governance's 400,000 tokens, not the pinned 8,000.
func TestWhatGovernanceReportsOutranksAnOperatorsPin(t *testing.T) {
	t.Parallel()
	settings := pricedConfig()
	settings.Pricing.ContextTokensByModel = map[string]int64{"pinned": 8_000}
	limiter := newTestLimiter(settings, fixedNow(testEpoch))
	limiter.ObserveModels(map[string]int64{"pinned": 400_000})

	limiter.Acquire("p", "pinned", oneToken)

	if input, _ := limiter.windowsOf(t, "p", "pinned"); input != 4*400_000 {
		t.Fatalf("input window = %v, want %v: governance moves before an operator can react, so its length wins", input, 4*400_000)
	}
}

// Test flow:
//  1. Configure an operator pin of 8,000 context tokens for "pinned" and observe nothing for it.
//  2. Acquire one request for "pinned".
//  3. Assert its input window is priced at the pinned 8,000 tokens, not the fallback.
func TestAnOperatorsPinPricesAModelGovernanceDoesNotName(t *testing.T) {
	t.Parallel()
	settings := pricedConfig()
	settings.Pricing.ContextTokensByModel = map[string]int64{"pinned": 8_000}
	limiter := newTestLimiter(settings, fixedNow(testEpoch))

	limiter.Acquire("p", "pinned", oneToken)

	if input, _ := limiter.windowsOf(t, "p", "pinned"); input != 4*8_000 {
		t.Fatalf("input window = %v, want %v: the pin stands in when governance names no length", input, 4*8_000)
	}
}

// Test flow:
//  1. Observe governance reporting 400,000 context tokens for "dropped", then a reply that no longer names it.
//  2. Reconfigure with an operator pin of 8,000 tokens for "dropped" and acquire one request.
//  3. Assert the input window is priced at the pinned 8,000 tokens, not the 400,000 governance stopped naming.
func TestAnOperatorsPinOutranksALengthGovernanceStoppedNaming(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(pricedConfig(), fixedNow(testEpoch))
	limiter.ObserveModels(map[string]int64{"dropped": 400_000})
	limiter.ObserveModels(map[string]int64{})
	pinned := pricedConfig()
	pinned.Pricing.ContextTokensByModel = map[string]int64{"dropped": 8_000}
	limiter.Reconfigure(pinned)

	limiter.Acquire("p", "dropped", oneToken)

	if input, _ := limiter.windowsOf(t, "p", "dropped"); input != 4*8_000 {
		t.Fatalf("input window = %v, want %v: a length governance no longer names must not outrank the operator's pin", input, 4*8_000)
	}
}

// Test flow:
//  1. Observe governance reporting 400,000 context tokens for "dropped", then a reply that no longer names it.
//  2. Acquire one request for "dropped" with no operator pin.
//  3. Assert the input window keeps the 400,000 tokens the model was last priced at, not the fallback.
func TestALengthGovernanceStoppedNamingIsKeptWithoutAPin(t *testing.T) {
	t.Parallel()
	limiter := newTestLimiter(pricedConfig(), fixedNow(testEpoch))
	limiter.ObserveModels(map[string]int64{"dropped": 400_000})
	limiter.ObserveModels(map[string]int64{})

	limiter.Acquire("p", "dropped", oneToken)

	if input, _ := limiter.windowsOf(t, "p", "dropped"); input != 4*400_000 {
		t.Fatalf("input window = %v, want %v: without a pin a model keeps the length it was last priced at", input, 4*400_000)
	}
}

// Test flow:
//  1. Observe a context length for "model-a", acquire a request, and post an EmptyAnswerLeftOpen verdict that narrows its window.
//  2. Observe the same context length for "model-a" again.
//  3. Assert the window stays at the narrowed value, since observing a model again is not an operator raising the initial window.
func TestObservingModelsKeepsAWindowAHostHasAlreadyEarned(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(pricedConfig(), fixedNow(testEpoch))
	l.ObserveModels(map[string]int64{"model-a": 1_000})
	l.Acquire("p", "model-a", oneToken)
	l.answered("p", "model-a", EmptyAnswerLeftOpen)
	narrowed, _ := l.windowsOf(t, "p", "model-a")

	l.ObserveModels(map[string]int64{"model-a": 1_000})

	if input, _ := l.windowsOf(t, "p", "model-a"); input != narrowed {
		t.Fatalf("input window = %v, want the %v the host had earned: observing a model is not an operator raising the initial window", input, narrowed)
	}
}

// Test flow:
//  1. Observe a context length of 1,000 for "model-a" and acquire a request, recording the opened window.
//  2. Observe a smaller context length of 100 for "model-a".
//  3. Assert the window stays at the value it already had, since renaming a context is not a reason to take a window away.
//  4. Assert the tracked bounds now use a floor and step of 100, following the new context unit.
func TestObservingASmallerContextMovesTheFloorAndLeavesTheWindowAlone(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(pricedConfig(), fixedNow(testEpoch))
	l.ObserveModels(map[string]int64{"model-a": 1_000})
	l.Acquire("p", "model-a", oneToken)
	opened, _ := l.windowsOf(t, "p", "model-a")

	l.ObserveModels(map[string]int64{"model-a": 100})

	input, _ := l.windowsOf(t, "p", "model-a")
	if input != opened {
		t.Fatalf("input window = %v, want the %v it already had: governance renaming a context is not a reason to take a window away", input, opened)
	}
	if got := l.states[key{participant: "p", model: "model-a"}].bounds.Input; got.Min != 100 || got.Step != 100 {
		t.Fatalf("bounds = %+v, want a floor and a step of 100: the unit a window is judged in follows the model", got)
	}
}

// Test flow:
//  1. Acquire two requests to meet the growth gate and post a Success verdict carrying a full window's worth of tokens.
//  2. Assert both windows grow by one step to 5, since one answer is worth one request of room.
//  3. Post an Overload verdict, narrowing the window, and record the narrowed input value.
//  4. Acquire two more requests and post the same whole-window Success verdict again.
//  5. Assert the window grows by exactly one step above the narrowed value, since growth climbs back at the same rate whether or not it was ever narrowed.
func TestAWindowEarnsOneStepPerAnswerWhetherOrNotItWasEverNarrowed(t *testing.T) {
	t.Parallel()
	l := newTestLimiter(testConfig(), fixedNow(testEpoch))
	l.admitOne("p", "m")
	l.admitOne("p", "m")
	wholeWindow := Result{Participant: "p", Model: "m", Verdict: Success, Carried: TokenCost{Input: 4, Output: 4}}

	l.OnResult(wholeWindow)

	input, output := l.windowsOf(t, "p", "m")
	if input != 5 || output != 5 {
		t.Fatalf("windows after one answer = (%v, %v), want both 5: one answer is worth one request of room", input, output)
	}

	l.answered("p", "m", Overload)
	narrowedInput, _ := l.windowsOf(t, "p", "m")
	l.admitOne("p", "m")
	l.admitOne("p", "m")
	l.OnResult(wholeWindow)

	if grown, _ := l.windowsOf(t, "p", "m"); grown != narrowedInput+1 {
		t.Fatalf("window = %v after congestion, want %v: the rung is the same one on the way back up", grown, narrowedInput+1)
	}
}

// randomLimiterConfig draws every knob freshState reads, including zero and negative windows.
func randomLimiterConfig(source *rand.Rand) ParticipantConfig {
	limiterConfig := testConfig()
	limiterConfig.Pricing = WindowPricing{
		Input:                     RequestBounds{Min: source.Int63n(5) - 1, Initial: source.Int63n(9) - 1},
		Output:                    RequestBounds{Min: source.Int63n(5) - 1, Initial: source.Int63n(9) - 1},
		ConcurrencyPer10000Weight: float64(source.Intn(3)) * source.Float64() * 4,
		FallbackContextTokens:     source.Int63n(8193) - 1,
		FallbackOutputTokens:      source.Int63n(2049) - 1,
		ContextTokensByModel:      map[string]int64{"pinned": source.Int63n(65536)},
		OutputTokensByModel:       map[string]int64{"pinned": source.Int63n(4096)},
	}
	limiterConfig.AfterFailures = source.Int63n(4)
	limiterConfig.IdleEviction = time.Duration(source.Intn(3)) * time.Minute
	return limiterConfig
}

// Test flow:
//  1. For many seeded random configs, build a limiter and feed it random observed weights and context lengths.
//  2. For hosts and models it never tracked, call `Admits` and compare it with `admissionLocked` on a `freshState`.
//  3. Assert both answers match and `Admits` left the host untracked.
func TestAdmitsAnswersAnUntrackedHostLikeAFreshState(t *testing.T) {
	t.Parallel()
	models := []string{"m", "pinned", "observed"}
	for seed := range int64(200) {
		source := rand.New(rand.NewSource(seed))
		limiter := newTestLimiter(randomLimiterConfig(source), fixedNow(testEpoch))
		limiter.ObserveModels(map[string]int64{"observed": source.Int63n(32769)})
		limiter.ObserveWeights(map[string]map[string]float64{
			"m":        {"heavy": source.Float64() * 100000},
			"observed": {"heavy": source.Float64() * 100},
		})

		for _, participant := range []string{"heavy", "light"} {
			for _, model := range models {
				want := admissionLocked(limiter.freshState(participant, model), smallestRequest, testEpoch)
				if got := limiter.Admits(participant, model); got != want {
					t.Fatalf("seed %d: Admits(%q, %q) = %s, want %s: an untracked host must answer as a fresh state would", seed, participant, model, got, want)
				}
				if _, tracked := limiter.states[key{participant: participant, model: model}]; tracked {
					t.Fatalf("seed %d: Admits(%q, %q) started tracking the host, want it left untracked", seed, participant, model)
				}
			}
		}
	}
}

// Test flow:
//  1. Start goroutines that call `Admits` beside goroutines that acquire, release, post faults, count idle refusals and clear quarantines on the same hosts.
//  2. Wait for every goroutine to finish, then clear every host's quarantine.
//  3. Assert every host is idle and admits again, since every lease was given back.
func TestAdmitsReadsWhileAttemptsAndCutOffsWrite(t *testing.T) {
	t.Parallel()
	hosts := []string{"first", "second", "third", "fourth"}
	limiter := newTestLimiter(testConfig(), fixedNow(testEpoch))
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for round := range 500 {
				host := hosts[(worker+round)%len(hosts)]
				for _, candidate := range hosts {
					limiter.Admits(candidate, "m")
				}
				if release, admission := limiter.Acquire(host, "m", oneToken); admission == AdmissionOpen {
					release()
				}
				switch round % 50 {
				case 7:
					limiter.answered(host, "m", UpstreamFault)
				case 19:
					limiter.CountRefusalIfIdle(host, "m")
				case 41:
					limiter.ClearQuarantine(host)
				}
			}
		})
	}
	workers.Wait()

	for _, host := range hosts {
		limiter.ClearQuarantine(host)
		if admission := limiter.Admits(host, "m"); admission != AdmissionOpen {
			t.Fatalf("Admits(%q, %q) after every lease was released and the quarantine cleared = %s, want open", host, "m", admission)
		}
		if state := limiter.states[key{participant: host, model: "m"}]; state != nil && !state.idle() {
			t.Fatalf("host %q in flight = (%d, %d), want both 0: every lease was released", host, state.input.inflight, state.output.inflight)
		}
	}
}

// Test flow:
//  1. Price a host whose weight times the configured share overflows an int64.
//  2. Assert the earned concurrency saturates at the largest int64: a float out of an int64's range converts differently on each architecture, so it is never converted.
func TestEarnedConcurrencySaturatesAWeightPastTheInt64Range(t *testing.T) {
	if earned := earnedConcurrency(math.MaxFloat64, math.MaxFloat64); earned != math.MaxInt64 {
		t.Fatalf("earnedConcurrency() = %d, want %d", earned, int64(math.MaxInt64))
	}
}
