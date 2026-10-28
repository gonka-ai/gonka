package journal

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/leakcheck"
	"devshard/cmd/gateway/internal/logcapture"
	"devshard/cmd/gateway/scheduler"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}

// ledgerSpy records what reached the ledger, in the order it arrived, and refuses every probe with probeRefusal.
type ledgerSpy struct {
	mu           sync.Mutex
	arrivals     []string
	probeRefusal error
}

func (s *ledgerSpy) note(arrival string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.arrivals = append(s.arrivals, arrival)
}

func (s *ledgerSpy) RecordRace(outcome engine.RaceOutcome) { s.note("race " + outcome.RequestID) }

func (s *ledgerSpy) RecordGhost(escrowID string, nonce uint64, reason string) {
	s.note(fmt.Sprintf("ghost %s %d %s", escrowID, nonce, reason))
}

func (s *ledgerSpy) RecordTimeout(vote engine.TimeoutEvent) {
	s.note(fmt.Sprintf("timeout %s %d %s", vote.EscrowID, vote.Nonce, vote.Action))
}

func (s *ledgerSpy) RecordDiffFacts(escrowID string, facts []DiffFact) {
	for _, fact := range facts {
		s.note(fmt.Sprintf("diff %s %d %d %d", escrowID, fact.Kind, fact.Nonce, fact.ValidatorSlot))
	}
}

func (s *ledgerSpy) RecordProbe(escrowID string, attempt accounting.Attempt) error {
	s.note(fmt.Sprintf("probe %s %d %s", escrowID, attempt.Nonce, attempt.Terminal))
	return s.probeRefusal
}

func (s *ledgerSpy) arrived() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.arrivals...)
}

// heldLedger parks the consumer inside its first race until released, so a test can fill the queue behind it.
type heldLedger struct {
	ledgerSpy
	entered chan struct{}
	release chan struct{}
	holding sync.Once
}

func newHeldLedger() *heldLedger {
	return &heldLedger{entered: make(chan struct{}), release: make(chan struct{})}
}

func (l *heldLedger) RecordRace(outcome engine.RaceOutcome) {
	l.holding.Do(func() {
		close(l.entered)
		<-l.release
	})
	l.ledgerSpy.RecordRace(outcome)
}

// heldVoteLedger parks the consumer inside its first race and again inside its first timeout vote.
type heldVoteLedger struct {
	heldLedger
	voteEntered chan struct{}
	voteRelease chan struct{}
	voteHolding sync.Once
}

func newHeldVoteLedger() *heldVoteLedger {
	return &heldVoteLedger{
		heldLedger:  heldLedger{entered: make(chan struct{}), release: make(chan struct{})},
		voteEntered: make(chan struct{}),
		voteRelease: make(chan struct{}),
	}
}

func (l *heldVoteLedger) RecordTimeout(vote engine.TimeoutEvent) {
	l.voteHolding.Do(func() {
		close(l.voteEntered)
		<-l.voteRelease
	})
	l.heldLedger.RecordTimeout(vote)
}

func newJournal(t *testing.T, settings Settings) *Journal {
	t.Helper()
	created := New(settings)
	t.Cleanup(func() { _ = created.Close() })
	return created
}

// holdConsumer parks the consumer and returns its release; cleanup releases it before the journal closes.
func holdConsumer(t *testing.T, events *Journal, ledger *heldLedger) func() {
	t.Helper()
	release := sync.OnceFunc(func() { close(ledger.release) })
	t.Cleanup(release)
	events.RecordRace(engine.RaceOutcome{RequestID: "holding"})
	select {
	case <-ledger.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer never took the first race")
	}
	return release
}

// dropWhileTheQueueIsEmpty parks the consumer on a batch holding the only progress slot, drops two progress lines behind it, and returns the release.
func dropWhileTheQueueIsEmpty(t *testing.T, events *Journal, ledger *heldVoteLedger) func() {
	t.Helper()
	releaseRace := holdConsumer(t, events, &ledger.heldLedger)
	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1", Nonce: 4})
	releaseVote := sync.OnceFunc(func() { close(ledger.voteRelease) })
	t.Cleanup(releaseVote)
	releaseRace()
	select {
	case <-ledger.voteEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the consumer never took the vote queued behind the progress line")
	}
	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	return releaseVote
}

// awaitClosed returns once Close has marked the journal closed; call it only while the consumer is parked in a sink.
func awaitClosed(t *testing.T, events *Journal) {
	t.Helper()
	marked := make(chan struct{})
	go func() {
		defer close(marked)
		events.mu.Lock()
		defer events.mu.Unlock()
		for !events.closed {
			events.arrived.Wait()
		}
	}()
	select {
	case <-marked:
	case <-time.After(5 * time.Second):
		t.Fatal("Close never marked the journal closed")
	}
}

// Test flow:
//  1. Build a journal with a `ledgerSpy`.
//  2. Record a race, a ghost burn, and a timeout vote, from different producers.
//  3. Flush the journal.
//  4. Assert the ledger spy received the facts in the order they were recorded.
func TestTheLedgerReceivesFactsInTheOrderTheyWereRecorded(t *testing.T) {
	ledger := &ledgerSpy{}
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: ledger})

	events.RecordRace(engine.RaceOutcome{RequestID: "request-1"})
	events.GhostBurned("escrow-1", scheduler.Burn{Nonce: 5, Reason: "participant_window_full_no_send"})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1", Nonce: 4, Action: engine.TimeoutActionCompleted})
	events.Flush()

	require.Equal(t, []string{
		"race request-1",
		"ghost escrow-1 5 participant_window_full_no_send",
		"timeout escrow-1 4 completed",
	}, ledger.arrived())
}

// Test flow:
//  1. Build a journal with no `Ledger` configured.
//  2. Record a race, a ghost burn, and a timeout vote.
//  3. Flush the journal.
//  4. Assert `Close` returns no error.
func TestAJournalWithoutALedgerAcceptsEveryFact(t *testing.T) {
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}})

	events.RecordRace(engine.RaceOutcome{RequestID: "request-1"})
	events.GhostBurned("escrow-1", scheduler.Burn{Nonce: 5})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1"})
	events.Flush()

	require.NoError(t, events.Close())
}

// Test flow:
//  1. Build a journal with a `heldLedger` and a `MoneyCeiling` of 3, and park the consumer via `holdConsumer`.
//  2. Record three race outcomes while the consumer is parked, filling the ceiling (the held request counts too).
//  3. Release the consumer and flush.
//  4. Assert only the held request and the first two races reached the ledger, one refusal is counted, and `Close` reports it.
func TestAMoneyFactPastTheCeilingIsRefusedCountedAndReported(t *testing.T) {
	ledger := newHeldLedger()
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: ledger, MoneyCeiling: 3})
	release := holdConsumer(t, events, ledger)

	events.RecordRace(engine.RaceOutcome{RequestID: "request-1"})
	events.RecordRace(engine.RaceOutcome{RequestID: "request-2"})
	events.RecordRace(engine.RaceOutcome{RequestID: "request-3"})
	release()
	events.Flush()

	require.Equal(t, []string{"race holding", "race request-1", "race request-2"}, ledger.arrived())
	moneyRefused, _, _ := events.Counts()
	require.Equal(t, uint64(1), moneyRefused)
	err := events.Close()
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "refused 1 money-lane events"), "Close() = %v", err)
}

// Test flow:
//  1. Build a journal with a `heldLedger` and `ProgressBacklog` of 1, and park the consumer via `holdConsumer`.
//  2. Record three progress steps while the consumer is parked, past the backlog limit.
//  3. Release the consumer and flush.
//  4. Assert the drop count is 2 and the "journal skipped progress lines" line reports it.
func TestAProgressLinePastTheBacklogIsDroppedAndTheNextBatchSaysHowMany(t *testing.T) {
	lines := &logcapture.Recorder{}
	ledger := newHeldLedger()
	events := newJournal(t, Settings{Lines: lines, Ledger: ledger, ProgressBacklog: 1})
	release := holdConsumer(t, events, ledger)

	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	release()
	events.Flush()

	_, progressDropped, _ := events.Counts()
	require.Equal(t, uint64(2), progressDropped)
	lines.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "journal skipped progress lines", Fields: []any{
		"skipped_lines", uint64(2),
	}})
}

// Test flow:
//  1. Build a journal with a `heldVoteLedger` and `ProgressBacklog` of 1, then set up a drop with nothing accepted after it via `dropWhileTheQueueIsEmpty`.
//  2. Release the consumer and flush.
//  3. Assert the "journal skipped progress lines" line is already written when `Flush` returns.
func TestADropNothingFollowsIsWrittenBeforeFlushReturns(t *testing.T) {
	lines := &logcapture.Recorder{}
	ledger := newHeldVoteLedger()
	events := newJournal(t, Settings{Lines: lines, Ledger: ledger, ProgressBacklog: 1})
	release := dropWhileTheQueueIsEmpty(t, events, ledger)

	release()
	events.Flush()

	lines.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "journal skipped progress lines", Fields: []any{
		"skipped_lines", uint64(2),
	}})
}

// Test flow:
//  1. Build a journal with a `heldVoteLedger` and `ProgressBacklog` of 1, then set up a drop with nothing accepted after it via `dropWhileTheQueueIsEmpty`.
//  2. Release the consumer and close the journal.
//  3. Assert `Close` writes the "journal skipped progress lines" line it still owed.
func TestCloseWritesTheWarningOwedForADropNothingFollowed(t *testing.T) {
	lines := &logcapture.Recorder{}
	ledger := newHeldVoteLedger()
	events := newJournal(t, Settings{Lines: lines, Ledger: ledger, ProgressBacklog: 1})
	release := dropWhileTheQueueIsEmpty(t, events, ledger)

	release()
	require.NoError(t, events.Close())

	lines.RequireLine(t, logcapture.Entry{Level: "warn", Msg: "journal skipped progress lines", Fields: []any{
		"skipped_lines", uint64(2),
	}})
}

// Test flow:
//  1. Build a journal with a `heldLedger`, park the consumer via `holdConsumer`, and record one more race behind it.
//  2. Call `Close` in a goroutine and wait, via `awaitClosed`, until it has marked the journal closed.
//  3. Release the consumer.
//  4. Assert `Close` returns without error and the ledger received both the held and the queued race.
func TestCloseDrainsWhatWasAcceptedBeforeIt(t *testing.T) {
	ledger := newHeldLedger()
	events := newJournal(t, Settings{Lines: &logcapture.Recorder{}, Ledger: ledger})
	release := holdConsumer(t, events, ledger)
	events.RecordRace(engine.RaceOutcome{RequestID: "request-1"})

	closeResult := make(chan error, 1)
	go func() { closeResult <- events.Close() }()
	awaitClosed(t, events)
	release()

	select {
	case err := <-closeResult:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the consumer was released")
	}
	require.Equal(t, []string{"race holding", "race request-1"}, ledger.arrived())
}

// Test flow:
//  1. Build and immediately close a journal.
//  2. Record two timeout votes and one ghost burn after close.
//  3. Assert nothing reached the ledger, the late-event count is 3, and only the first event of each kind logged an "after it closed" error line.
func TestAnEventAfterCloseIsCountedAndItsKindWrittenOnce(t *testing.T) {
	lines := &logcapture.Recorder{}
	ledger := &ledgerSpy{}
	events := newJournal(t, Settings{Lines: lines, Ledger: ledger})
	require.NoError(t, events.Close())

	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1", Nonce: 4})
	events.RecordTimeout(engine.TimeoutEvent{EscrowID: "escrow-1", Nonce: 5})
	events.GhostBurned("escrow-1", scheduler.Burn{Nonce: 6})

	require.Empty(t, ledger.arrived())
	_, _, lateEvents := events.Counts()
	require.Equal(t, uint64(3), lateEvents)
	require.Equal(t, []logcapture.Entry{
		{Level: "error", Msg: "journal received an event after it closed", Fields: []any{"kind", "timeout_vote"}},
		{Level: "error", Msg: "journal received an event after it closed", Fields: []any{"kind", "nonce_burned"}},
	}, lines.All())
}

// Test flow:
//  1. Build a journal with a `heldLedger` and `ProgressBacklog` of 1, and park the consumer via `holdConsumer`.
//  2. Record a progress step to fill the backlog, then a finished request and a throttled request.
//  3. Release the consumer and flush.
//  4. Assert the finished-request line was logged (it rides the money lane) and the drop count reflects the dropped throttled line.
func TestARequestFinishedLineSurvivesAFullProgressBacklogThatDropsARequestThrottledLine(t *testing.T) {
	lines := &logcapture.Recorder{}
	ledger := newHeldLedger()
	events := newJournal(t, Settings{Lines: lines, Ledger: ledger, ProgressBacklog: 1})
	release := holdConsumer(t, events, ledger)

	events.RecordStep(engine.RaceStep{Kind: engine.RaceStepNonceCommitted})
	events.RequestFinished(RequestLine{RequestID: "request-1", Model: "qwen", Verdict: "served"})
	events.RequestThrottled(RequestLine{RequestID: "request-2", Model: "qwen", LimiterReason: "too_many_requests"})
	release()
	events.Flush()

	lines.RequireLine(t, logcapture.Entry{Level: "info", Msg: "request finished", Fields: []any{
		"request", "request-1", "model", "qwen", "stream", false,
		"input_tokens", uint64(0), "output_tokens", int64(0),
		"outcome", "served", "bytes", int64(0), "terminated", false, "duration_ms", int64(0),
	}})
	_, progressDropped, _ := events.Counts()
	require.Equal(t, uint64(1), progressDropped)
}

// Test flow:
//  1. Build the expected money-lane membership for every `Kind`.
//  2. Iterate every `Kind` from `KindRaceReported` to `kindCount`.
//  3. Assert each kind has a non-"unknown" name and sits on the lane the expected map assigns.
func TestEveryKindHasANameAndTheLaneTheSpecAssigns(t *testing.T) {
	moneyLane := map[Kind]bool{
		KindRaceReported: true, KindTimeoutVote: true, KindNonceBurned: true, KindBurnBudgetExhausted: true,
		KindDiffComposed: true, KindWarmupProbe: true, KindNonceStranded: true, KindHostDiverged: true,
		KindReplyNotCached: true, KindRequestFinished: true, KindHostTransition: true, KindExcludedHostServed: true,
		KindForcedSend: true, KindEscrowTransition: true, KindChainTransition: true, KindFundingTransition: true,
	}
	for kind := KindRaceReported; kind < kindCount; kind++ {
		require.NotEqual(t, "unknown", kind.String(), "kind %d has no name", kind)
		require.Equal(t, moneyLane[kind], kind.onMoneyLane(), "kind %s is on the wrong lane", kind)
	}
}

// Test flow:
//  1. Table-driven: each case pairs an `engine.RaceStepKind` with the `Kind` and lane (money or progress) it must map to.
//  2. For each case, convert the step via `raceStepKind`.
//  3. Assert the resulting kind matches and its lane, from `onMoneyLane`, matches the case's expectation.
func TestRaceStepKindMapsEveryStepToItsLane(t *testing.T) {
	testCases := []struct {
		name     string
		step     engine.RaceStepKind
		wantKind Kind
		money    bool
	}{
		{name: "nonce committed stays on the progress lane", step: engine.RaceStepNonceCommitted, wantKind: KindNonceCommitted, money: false},
		{name: "escalation unfilled stays on the progress lane", step: engine.RaceStepEscalationUnfilled, wantKind: KindEscalationUnfilled, money: false},
		{name: "attempt crowned stays on the progress lane", step: engine.RaceStepAttemptCrowned, wantKind: KindAttemptCrowned, money: false},
		{name: "attempt finished stays on the progress lane", step: engine.RaceStepAttemptFinished, wantKind: KindAttemptFinished, money: false},
		{name: "a served winner's late finish stays on the progress lane", step: engine.RaceStepWinnerFinishLanded, wantKind: KindWinnerFinishLanded, money: false},
		{name: "nonce stranded rides the money lane", step: engine.RaceStepNonceStranded, wantKind: KindNonceStranded, money: true},
		{name: "host blocked rides the money lane", step: engine.RaceStepHostBlocked, wantKind: KindHostDiverged, money: true},
		{name: "host rewound rides the money lane", step: engine.RaceStepHostRewound, wantKind: KindHostDiverged, money: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := raceStepKind(testCase.step)
			require.Equal(t, testCase.wantKind, got)
			require.Equal(t, testCase.money, got.onMoneyLane(), "kind %s is on the wrong lane", got)
		})
	}
}
