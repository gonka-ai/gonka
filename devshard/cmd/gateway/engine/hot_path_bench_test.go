package engine

import (
	"strconv"
	"testing"
	"time"

	"devshard/cmd/gateway/config"
)

// The sink keeps what a benchmark built from being optimised away.
var sinkOutcome RaceOutcome

func benchAttempts(count int) []EscalationAttempt {
	start := time.Unix(1786114580, 0)
	attempts := make([]EscalationAttempt, 0, count)
	for i := range count {
		attempts = append(attempts, EscalationAttempt{
			LaunchTime:      start,
			ClockStart:      start,
			ReceiptTime:     start.Add(200 * time.Millisecond),
			FirstContentP75: time.Duration(i+1) * time.Second,
		})
	}
	return attempts
}

// nextDeadline runs on every event the coordinator takes, chunks included.
func BenchmarkNextDeadline(b *testing.B) {
	policy := EscalationPolicy{
		ReceiptTimeout: 10 * time.Second, FirstTokenFloor: time.Second,
		FirstTokenCeiling: 60 * time.Second, InterChunkStall: time.Minute,
		LoserGrace: 5 * time.Second, MaxAttemptsPerRequest: 4,
	}
	plan := deadlinePlan{
		Policy:   policy,
		Request:  EscalationRequest{InputTokens: 4096},
		Attempts: benchAttempts(4),
	}
	now := time.Unix(1786114582, 0)

	b.ReportAllocs()
	for b.Loop() {
		nextDeadline(now, plan)
	}
}

func BenchmarkFirstTokenBudget(b *testing.B) {
	policy := EscalationPolicy{FirstTokenFloor: time.Second, FirstTokenCeiling: 60 * time.Second}

	b.ReportAllocs()
	for b.Loop() {
		policy.firstTokenBudget(4096, 5*time.Second)
	}
}

// recordChunkGap runs once per streamed chunk, beside the classifier.
func BenchmarkRecordChunkGap(b *testing.B) {
	content := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello there"}}]}` + "\n\n")
	state := &attemptState{}
	at := time.Unix(1786114580, 0)

	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(40 * time.Millisecond)
		state.recordChunkGap(at, content)
	}
}

func BenchmarkRecordChunkGapAtDone(b *testing.B) {
	done := []byte("data: [DONE]\n\n")
	state := &attemptState{}
	at := time.Unix(1786114580, 0)

	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(40 * time.Millisecond)
		state.recordChunkGap(at, done)
	}
}

// A host that bursts reasoning sends chunks far larger than one delta; the terminator check walks them.
func BenchmarkRecordChunkGapLargeChunk(b *testing.B) {
	payload := make([]byte, 0, 8<<10)
	payload = append(payload, `data: {"choices":[{"index":0,"delta":{"reasoning":"`...)
	for len(payload) < 8<<10 {
		payload = append(payload, "reasoning text that a host streamed in one go "...)
	}
	payload = append(payload, `"}}]}`+"\n\n"...)
	state := &attemptState{}
	at := time.Unix(1786114580, 0)

	b.ReportAllocs()
	for b.Loop() {
		at = at.Add(40 * time.Millisecond)
		state.recordChunkGap(at, payload)
	}
}

// A steady stream sets a new maximum gap only rarely, so most chunks are judged against a gap they cannot beat.
func BenchmarkRecordChunkGapJitteredStream(b *testing.B) {
	content := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello there"}}]}` + "\n\n")
	state := &attemptState{}
	at := time.Unix(1786114580, 0)
	jitter := 0

	b.ReportAllocs()
	for b.Loop() {
		jitter = (jitter*7 + 3) % 11
		at = at.Add(time.Duration(35+jitter) * time.Millisecond)
		state.recordChunkGap(at, content)
	}
}

// benchSink is the client end of the writer chain, counting bytes and nothing else.
type benchSink struct{ written int64 }

func (s *benchSink) Write(chunk []byte) (int, error) {
	s.written += int64(len(chunk))
	return len(chunk), nil
}

// benchWriterChain is the whole per-chunk path a crowned attempt runs on the goroutine that writes to the client.
func benchWriterChain(events chan AttemptEvent) (*attemptWriter, *benchSink) {
	client := &benchSink{}
	winner := newWinnerWriter(77, client, make(chan crownRequest), make(chan struct{}), testPrefix())
	winner.verdict, winner.client, winner.withheld = streamWinner, client, nil
	sink := &attemptSink{winner: winner}
	classifier := contentGate{streamClassifier: &fakeClassifier{}, sink: sink}
	state := &attemptState{firstToken: time.Unix(1786114580, 0)}
	tick := time.Unix(1786114580, 0)
	spec := AttemptSpec{
		Escrow: "escrow-1", Model: testModel, Participant: testParticipant,
		HostIdx: 3, HostLabel: "host-3", Role: RolePrimary, StartReason: StartPrimary,
		Nonce:       fakePrepared{nonce: 77, hostIdx: 3},
		ReleaseSlot: func() {}, Classifier: classifier, Sink: sink, Events: events,
		Now: func() time.Time {
			tick = tick.Add(40 * time.Millisecond)
			return tick
		},
	}
	return &attemptWriter{spec: spec, state: state, nonce: 77}, client
}

// attemptWriter.Write runs once per streamed chunk, on the goroutine that writes to the client.
func BenchmarkAttemptWriterWrite(b *testing.B) {
	chunk := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello there"}}]}` + "\n\n")
	events := make(chan AttemptEvent, eventBuffer)
	for range eventBuffer {
		events <- AttemptEvent{}
	}
	writer, _ := benchWriterChain(events)

	b.ReportAllocs()
	for b.Loop() {
		if _, err := writer.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
}

// plan and nextDeadline run on the coordinator for every event, chunk progress included.
func benchCoordinator(attemptCount int) *raceCoordinator {
	attempts := make([]*liveAttempt, 0, attemptCount)
	for index := range attemptCount {
		attempts = append(attempts, &liveAttempt{
			nonce:         uint64(500 + index),
			participant:   "host-" + strconv.Itoa(index),
			sendTime:      testEpoch.Add(-time.Duration(index+1) * time.Second),
			receiptTime:   testEpoch.Add(-time.Duration(index+1) * time.Second),
			firstToken:    testEpoch.Add(-time.Duration(index) * time.Second),
			firstContent:  testEpoch.Add(-time.Duration(index) * time.Second),
			lastChunk:     testEpoch,
			observedFirst: time.Duration(index+1) * time.Second,
			cancel:        func() {},
		})
	}
	policy := settledPolicy()
	policy.InterChunkStall = 30 * time.Second
	fixture := newRaceFixture(policy, attemptCount)
	for index := range attemptCount {
		fixture.target.labels[index] = "label-host-" + strconv.Itoa(index)
	}
	return pausedCoordinator(fixture, attemptCount, attempts...)
}

func BenchmarkCoordinatorDeadlinePlan(b *testing.B) {
	coordinator := benchCoordinator(3)
	now := testEpoch

	b.ReportAllocs()
	for b.Loop() {
		nextDeadline(now, coordinator.plan())
	}
}

// The transport writes one whole SSE event per chunk; only a split event reaches the join path.
func BenchmarkCarryBufferTakeAligned(b *testing.B) {
	budget := newCarryBudget(config.Stream{
		ClassifyMaxAttemptBytes: 1 << 20, ClassifyMaxParticipantBytes: 10 << 20, ClassifyMaxGlobalBytes: 100 << 20,
	})
	buffer := newCarryBuffer(budget, testParticipant)
	chunk := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hello there"}}]}` + "\n\n")

	b.ReportAllocs()
	for b.Loop() {
		buffer.Take(chunk)
	}
}

func BenchmarkCarryBufferTakeSplit(b *testing.B) {
	budget := newCarryBudget(config.Stream{
		ClassifyMaxAttemptBytes: 1 << 20, ClassifyMaxParticipantBytes: 10 << 20, ClassifyMaxGlobalBytes: 100 << 20,
	})
	buffer := newCarryBuffer(budget, testParticipant)
	head := []byte(`data: {"choices":[{"index":0,"delta":{"content":"hel`)
	tail := []byte(`lo there"}}]}` + "\n\n" + `data: {"choices":[{"index":0,"delta":{"content":"and`)

	b.ReportAllocs()
	for b.Loop() {
		buffer.Take(head)
		buffer.Take(tail)
	}
}

// outcome folds the whole race for its one report, on the goroutine the client is waiting on.
func BenchmarkRaceOutcome(b *testing.B) {
	coordinator := benchCoordinator(3)
	for _, attempt := range coordinator.attempts {
		attempt.done, attempt.completed = true, testEpoch
		attempt.nonceFinished = true
		attempt.outcome = &AttemptOutcome{
			Participant: attempt.participant, HostIdx: attempt.hostIdx, HostLabel: "label",
			Nonce: attempt.nonce, Role: RoleSpeculative, StartReason: StartPrimary,
			SendTime: attempt.sendTime, ReceiptTime: attempt.receiptTime,
			FirstToken: attempt.firstToken, FirstContent: attempt.firstContent,
			LastChunk: attempt.lastChunk, Completed: testEpoch,
			ContentChunks: 412, StreamChunks: 415, OutputBytes: 61_440,
			UsageCompletionTokens: 512, ContentSource: "delta.content", Terminal: TerminalLost,
		}
	}
	coordinator.winner = coordinator.attempts[0]

	b.ReportAllocs()
	for b.Loop() {
		sinkOutcome = coordinator.outcome()
	}
}
