package accounting

import (
	"sync"
	"testing"
	"time"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

type captureSink struct {
	mu     sync.Mutex
	events []DispositionEvent
	onEach func(DispositionEvent)
}

func (s *captureSink) OnDisposition(ev DispositionEvent) {
	if s.onEach != nil {
		s.onEach(ev)
	}
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
}

func (s *captureSink) all() []DispositionEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]DispositionEvent(nil), s.events...)
}

// delivered blocks until everything queued so far has reached the sink.
// Delivery is asynchronous by design (see TestDispositionSinkNeverBlocksTheRecorder),
// so every assertion on sink contents has to go through here.
func (s *captureSink) delivered(tr *Tracker) []DispositionEvent {
	tr.FlushDispositions()
	return s.all()
}

func TestFinalizeNonceEmitsOncePerNonce(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 22, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, accountingTestNow.Add(-2*time.Minute), PhaseNormal, QuarantineNone, TraceRef{}))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID:      "e1",
		Nonce:         1,
		Kind:          TimeoutRefused,
		Phase:         PhaseNormal,
		Outcome:       TimeoutVoteCollectionFailed,
		FailureOrigin: FailureTransportUnknown,
	}))
	require.Empty(t, sink.delivered(tr), "non-terminal unfinished_refused must not emit yet")

	require.NoError(t, tr.RecordUsage("e1", 1, UsageWinner, TraceRef{}))
	require.NoError(t, tr.RecordProtocol("e1", 1, 1, ProtocolFinishApplied, types.HostStats{}))

	events := sink.delivered(tr)
	require.Len(t, events, 1)
	require.Equal(t, uint64(1), events[0].Nonce)
	require.Equal(t, DispositionFinishedUsed, events[0].Key.Disposition)
}

func TestFinalizeNonceEmitsOnSettlementRelease(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 43, "m")
	now := accountingTestNow
	tr.now = func() time.Time { return now }
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, now, PhaseNormal, QuarantineNone, TraceRef{}))
	now = now.Add(66 * time.Second)
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: 1, Kind: TimeoutRefused, Phase: PhaseNormal,
		Outcome: TimeoutInsufficientVotes,
	}))
	require.Empty(t, sink.delivered(tr))

	require.NoError(t, tr.RecordPhase("e1", EscrowSettled))
	events := sink.delivered(tr)
	require.Len(t, events, 1)
	require.Equal(t, DispositionUnfinishedRefused, events[0].Key.Disposition)
	require.Equal(t, TimeoutInsufficientVotes, events[0].Key.TimeoutOutcome)
}

func TestDispositionEventCarriesTraceRef(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 7, "m")

	var ref TraceRef
	ref.TraceID = [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	ref.SpanID = [8]byte{9, 8, 7, 6, 5, 4, 3, 2}
	ref.Sampled = true

	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordRealSend("e1", 1, accountingTestNow, PhaseNormal, QuarantineNone, ref))
	require.NoError(t, tr.RecordUsage("e1", 1, UsageWinner, TraceRef{})) // must not overwrite
	require.NoError(t, tr.RecordProtocol("e1", 1, 1, ProtocolFinishApplied, types.HostStats{}))

	events := sink.delivered(tr)
	require.Len(t, events, 1)
	require.Equal(t, ref.TraceID, events[0].Trace.TraceID)
	require.Equal(t, ref.SpanID, events[0].Trace.SpanID)
	require.True(t, events[0].Trace.Sampled)
}

func TestDispositionEventEmittedOutsideLock(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	sink.onEach = func(DispositionEvent) {
		_, _ = tr.ErrorCounts() // must not deadlock
	}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", TraceRef{}))
	require.Len(t, sink.delivered(tr), 1)
}

// TestDispositionSinkNeverBlocksTheRecorder pins the reason delivery is
// asynchronous: RecordDiff runs inside the sequencer's diff-commit critical
// section, so a sink that stalls must not stall inference.
func TestDispositionSinkNeverBlocksTheRecorder(t *testing.T) {
	tr := newTestTracker(t)
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	sink := &captureSink{onEach: func(DispositionEvent) {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-release
	}}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 7, "m")

	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, tr.RecordDiff("e1", 1, false)) // protocol_only: emits immediately
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recording blocked on a stalled sink")
	}

	<-entered
	// The tracker must still take writes while the sink is stuck.
	require.NoError(t, tr.RecordDiff("e1", 2, false))
	close(release)
}

func TestDispositionQueueDropsInsteadOfBlocking(t *testing.T) {
	tr := newTestTracker(t)
	release := make(chan struct{})
	blocked := make(chan struct{})
	var once sync.Once
	sink := &captureSink{onEach: func(DispositionEvent) {
		once.Do(func() { close(blocked); <-release })
	}}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 7, "m")

	require.NoError(t, tr.RecordDiff("e1", 1, false))
	<-blocked
	for nonce := uint64(2); nonce < uint64(dispositionQueueSize)+64; nonce++ {
		require.NoError(t, tr.RecordDiff("e1", nonce, false))
	}
	require.Positive(t, tr.DispositionDrops(), "a full queue must drop rather than block classification")
	close(release)
}

func TestDispositionSinkSwapIsRaceFree(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			tr.SetDispositionSink(&captureSink{})
		}
	}()
	go func() {
		defer wg.Done()
		for nonce := uint64(1); nonce <= 200; nonce++ {
			require.NoError(t, tr.RecordDiff("e1", nonce, false))
		}
	}()
	wg.Wait()
	tr.FlushDispositions()
}

func TestProtocolOnlyEmitsWithEmptyTrace(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, false))
	events := sink.delivered(tr)
	require.Len(t, events, 1)
	require.Equal(t, DispositionProtocolOnly, events[0].Key.Disposition)
	require.True(t, events[0].Trace.IsZero())
	require.Equal(t, "", events[0].Trace.TraceIDString())
}

// TestTerminalUnclassifiedNonceKeepsItsIdentity covers the nonce that settles
// its protocol timeout before the accounting deadline: it leaves Live without
// being counted, so its event has no disposition -- but it must still name the
// slot it belonged to, or the event is attributed to the wrong participant.
func TestTerminalUnclassifiedNonceKeepsItsIdentity(t *testing.T) {
	tr := newTestTracker(t)
	sink := &captureSink{}
	tr.SetDispositionSink(sink)
	registerEscrow(t, tr, "e1", 77, "m")
	now := accountingTestNow
	tr.now = func() time.Time { return now }

	const nonce = uint64(3) // slot 1 of a two-slot escrow
	require.NoError(t, tr.RecordDiff("e1", nonce, true))
	require.NoError(t, tr.RecordRealSend("e1", nonce, now, PhasePoC, QuarantineShadow, TraceRef{}))
	require.NoError(t, tr.RecordProtocol("e1", nonce, 1, ProtocolTimeoutApplied, types.HostStats{Missed: 1}))
	require.NoError(t, tr.RecordTimeout(TimeoutRecord{
		EscrowID: "e1", Nonce: nonce, Kind: TimeoutRefused, Phase: PhaseNormal,
		Outcome: TimeoutApplied,
	}))

	events := sink.delivered(tr)
	require.Len(t, events, 1)
	require.Equal(t, Disposition(""), events[0].Key.Disposition,
		"an uncounted terminal nonce must not claim a disposition")
	require.Equal(t, uint32(1), events[0].Key.SlotID)
	require.Equal(t, PhasePoC, events[0].Key.DispatchPhase)
	require.Equal(t, QuarantineShadow, events[0].Key.QuarantineMode)
	require.Equal(t, TimeoutApplied, events[0].Key.TimeoutOutcome)
	require.NotEmpty(t, events[0].Participant, "slot must resolve to its participant")
}

func TestNoSinkIsSafe(t *testing.T) {
	tr := newTestTracker(t)
	registerEscrow(t, tr, "e1", 7, "m")
	require.NoError(t, tr.RecordDiff("e1", 1, true))
	require.NoError(t, tr.RecordGhost("e1", 1, PhaseNormal, QuarantineNone, NoSendPoCUnavailable, "", TraceRef{}))
	require.False(t, tr.hasSink())
	require.Zero(t, tr.DispositionDrops(), "events must not be queued when nobody is listening")
}

func TestUsageMappingIsSharedWithTheGateway(t *testing.T) {
	require.Equal(t, UsageWinner, UsageFor(7, 7))
	require.Equal(t, UsageLoser, UsageFor(8, 7))
	require.Equal(t, UsageUnknownValue, UsageFor(8, 0))
	require.Equal(t, DispositionFinishedUsed, DispositionForUsage(UsageWinner))
	require.Equal(t, DispositionFinishedUnused, DispositionForUsage(UsageLoser))
	require.Equal(t, DispositionFinishedUsageUnknown, DispositionForUsage(UsageUnknownValue))
	require.Equal(t, Disposition(""), DispositionForUsage(""))
}

func TestTimeoutActionRecordedMatchesRecorder(t *testing.T) {
	require.False(t, TimeoutActionRecorded("started", "none"))
	require.False(t, TimeoutActionRecorded("skipped", "nonce_already_finished"))
	require.False(t, TimeoutActionRecorded("skipped", "empty_stream_without_non_empty_winner"))
	require.True(t, TimeoutActionRecorded("skipped", "phase_transition_aborted"))
	require.True(t, TimeoutActionRecorded("completed", "none"))
	require.True(t, TimeoutActionRecorded("failed", "insufficient_votes"))
}
