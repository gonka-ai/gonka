package engine

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/scheduler"
)

const maxUpstreamBodyLogged = 256

// See README.md, "Errors the engine returns".
var (
	ErrStateRootDivergence  = errors.New("post state root divergence")
	ErrLocalRootUnavailable = errors.New("local post state root unavailable")
)

// dispatcher is the engine's only route to a host; an adapter over devshard/user.Session satisfies it. See race.md, "Deadlines".
type dispatcher interface {
	// CatchUp runs onFault at most once, on the caller's goroutine, when the catch-up finds the host unreachable.
	CatchUp(ctx context.Context, nonce scheduler.Prepared, onFault func()) error
	Send(ctx context.Context, nonce scheduler.Prepared, stream io.Writer, onReceipt func()) (Response, error)
}

// Response is one host's reply, forwarded untouched: only the session that produced it can apply the rest.
type Response interface {
	Confirmed() bool
	// ConfirmedAt is the executor's wall clock in seconds when it signed; 0 when not an executor.
	ConfirmedAt() int64
	// ReleaseFinish lets the host's Finish for this nonce into a diff again; the session holds it from the moment the reply lands.
	ReleaseFinish()
}

// chunkFacts is what one SSE chunk carried. See README, "Classification and reassembly".
type chunkFacts struct {
	Content       bool
	ContentSource string

	Error             bool
	ErrorSource       string
	ErrorCode         string
	ErrorType         string
	ErrorMessage      string
	ErrorPayload      string
	CapabilityRefused bool
	Capability        CapabilitySignal

	UsageCompletionTokens int64
	UsagePromptTokens     int64
	LogprobTokens         int64
	TokensBurned          bool
	LogprobsDecoded       bool
	FinishReason          string
}

// streamClassifier reassembles one attempt's SSE bytes and reports what they contained; Release frees the buffer.
type streamClassifier interface {
	Classify(chunk []byte) chunkFacts
	Flush() chunkFacts
	Release()
}

// AttemptSpec is everything one attempt needs, fixed at construction; Events must be drained until AttemptDone. See rules.md, "1. A committed nonce is always settled".
type AttemptSpec struct {
	Escrow      string
	Model       string
	Participant string
	HostIdx     int
	HostLabel   string
	Role        string
	StartReason string
	Suspicious  bool

	Nonce scheduler.Prepared

	Dispatch    dispatcher
	ReleaseSlot func()
	Classifier  streamClassifier
	Sink        io.Writer
	Now         func() time.Time

	Events   chan<- AttemptEvent
	Reported <-chan struct{}
}

type AttemptEventKind int

const (
	AttemptDispatched AttemptEventKind = iota
	AttemptCatchUpFaulted
	AttemptReceipt
	AttemptFirstToken
	AttemptContent
	AttemptChunk
	AttemptDone
)

type AttemptEvent struct {
	Kind  AttemptEventKind
	Nonce uint64
	At    time.Time

	Outcome   *AttemptOutcome
	Lifecycle Lifecycle
}

// attemptState is owned by the attempt's own goroutine alone. See README, "Who owns which state".
type attemptState struct {
	sendTime     time.Time
	receiptTime  time.Time
	firstToken   time.Time
	firstContent time.Time
	completed    time.Time

	contentChunks int64
	streamChunks  int64
	outputBytes   int64

	lastChunk      time.Time
	firstChunkHead string
	lastChunkHead  string
	maxChunkGap    time.Duration
	maxGapChunk    int64
	droppedEvents  int64

	usageCompletionTokens int64
	usagePromptTokens     int64
	logprobTokens         int64
	tokensBurned          bool
	logprobsDecoded       bool
	hostTerminated        bool
	finishReason          string
	contentSource         string
	capability            CapabilitySignal
	errorSource           string
	errorCode             string
	errorType             string
	errorMessage          string
	errorPayload          string
	capabilityRefused     bool

	confirmed       bool
	confirmedAt     int64
	stateDivergent  bool
	localFault      bool
	hostAbsent      bool
	gatewayPressure bool
	terminal        Terminal
	lifecycle       Lifecycle

	upstreamStatus int
	upstreamBody   string
}

type prefixHolder interface{ releasePrefix() }

func runAttempt(ctx context.Context, spec AttemptSpec) {
	defer spec.Classifier.Release()
	releaseSlot := sync.OnceFunc(spec.ReleaseSlot)
	defer releaseSlot()

	nonce := spec.Nonce.Nonce()
	catchUpErr := spec.Dispatch.CatchUp(ctx, spec.Nonce, func() {
		spec.emit(AttemptEvent{Kind: AttemptCatchUpFaulted, Nonce: nonce, At: spec.Now()})
	})
	state := &attemptState{sendTime: spec.Now()}
	spec.emit(AttemptEvent{Kind: AttemptDispatched, Nonce: nonce, At: state.sendTime})

	var response Response
	err := catchUpErr
	if catchUpErr == nil {
		writer := &attemptWriter{spec: spec, state: state, nonce: nonce}
		response, err = spec.Dispatch.Send(ctx, spec.Nonce, writer, func() {
			if !state.receiptTime.IsZero() {
				return
			}
			state.receiptTime = spec.Now()
			spec.emit(AttemptEvent{Kind: AttemptReceipt, Nonce: nonce, At: state.receiptTime})
		})
	}

	if holder, holds := spec.Sink.(prefixHolder); holds {
		holder.releasePrefix()
	}
	state.completed = spec.Now()
	if response != nil {
		state.confirmed, state.confirmedAt = response.Confirmed(), response.ConfirmedAt()
	}
	state.classify(ctx, spec, err)

	releaseSlot()
	outcome := state.outcome(spec)
	holdFinishForClaim(response, outcome)
	spec.finish(AttemptEvent{
		Kind:      AttemptDone,
		Nonce:     nonce,
		At:        state.completed,
		Outcome:   outcome,
		Lifecycle: state.lifecycle,
	})
}

func holdFinishForClaim(response Response, outcome *AttemptOutcome) {
	if response == nil {
		return
	}
	if outcome.claimsMiss() {
		outcome.MissProof.releaseFinish = response.ReleaseFinish
		return
	}
	response.ReleaseFinish()
}

type attemptWriter struct {
	spec  AttemptSpec
	state *attemptState
	nonce uint64
}

func (w *attemptWriter) Write(chunk []byte) (int, error) {
	now := w.spec.Now()

	w.state.streamChunks++
	w.state.outputBytes += int64(len(chunk))
	w.state.recordChunkGap(now, chunk)
	w.state.hostTerminated = w.state.hostTerminated || filters.HasSSEDone(chunk)
	if w.state.firstToken.IsZero() {
		w.state.firstToken = now
		w.spec.emit(w.progress(AttemptFirstToken, now))
	}

	facts := w.spec.Classifier.Classify(chunk)
	w.state.record(facts)
	if w.state.contentChunks == 0 {
		w.state.keepChunkHeads(chunk)
	}
	if facts.Content && w.state.firstContent.IsZero() {
		w.state.firstContent = now
		w.spec.emit(w.progress(AttemptContent, now))
	}
	if facts.Content && !w.spec.offer(w.progress(AttemptChunk, now)) {
		w.state.droppedEvents++
	}

	return w.spec.Sink.Write(chunk)
}

// Flush is how the transport's per-line flush reaches the client; without it a winner's bytes sit in the server's buffer.
func (w *attemptWriter) Flush() {
	if flusher, ok := w.spec.Sink.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *attemptWriter) progress(kind AttemptEventKind, at time.Time) AttemptEvent {
	return AttemptEvent{Kind: kind, Nonce: w.nonce, At: at}
}

// recordChunkGap keeps the longest mid-stream silence; the one before [DONE] is the end, not a quiet host.
func (s *attemptState) recordChunkGap(now time.Time, chunk []byte) {
	previous := s.lastChunk
	s.lastChunk = now
	if previous.IsZero() {
		return
	}
	gap := now.Sub(previous)
	if gap <= s.maxChunkGap || filters.HasSSEDone(chunk) {
		return
	}
	s.maxChunkGap, s.maxGapChunk = gap, s.streamChunks
}

// meanChunkGap is the average silence between chunks, which is the inverse of the delivered rate.
func (s *attemptState) meanChunkGap() time.Duration {
	if s.streamChunks < 2 || s.firstToken.IsZero() || !s.lastChunk.After(s.firstToken) {
		return 0
	}
	return s.lastChunk.Sub(s.firstToken) / time.Duration(s.streamChunks-1)
}

func (s *attemptState) record(facts chunkFacts) {
	if facts.Content || facts.Error {
		s.contentChunks++
	}

	if facts.ContentSource != "" && s.contentSource == "" {
		s.contentSource = facts.ContentSource
	}
	if facts.ErrorSource != "" && s.errorSource == "" {
		s.errorSource = facts.ErrorSource
		s.errorCode = facts.ErrorCode
		s.errorType = facts.ErrorType
		s.errorMessage = facts.ErrorMessage
		s.errorPayload = facts.ErrorPayload
		s.capabilityRefused = facts.CapabilityRefused
		s.capability = facts.Capability
	}
	if facts.UsageCompletionTokens > 0 {
		s.usageCompletionTokens = facts.UsageCompletionTokens
	}
	if facts.FinishReason != "" {
		s.finishReason = facts.FinishReason
	}
	if facts.UsagePromptTokens > 0 {
		s.usagePromptTokens = facts.UsagePromptTokens
	}
	s.logprobTokens += facts.LogprobTokens
	s.tokensBurned = s.tokensBurned || facts.TokensBurned
	s.logprobsDecoded = s.logprobsDecoded || facts.LogprobsDecoded
}

func (spec AttemptSpec) emit(event AttemptEvent) bool {
	select {
	case spec.Events <- event:
		return true
	case <-spec.Reported:
		return false
	}
}

// finish hands a held Finish to the race, or releases it when no race is left to take it. See ../docs/race.md, "Error misses".
func (spec AttemptSpec) finish(event AttemptEvent) {
	delivered := spec.emit(event)
	if event.Outcome == nil {
		return
	}
	if !delivered {
		event.Outcome.MissProof.releaseHeldFinish()
		return
	}
	select {
	case <-spec.Reported:
		event.Outcome.MissProof.releaseUntakenFinish()
	default:
	}
}

// offer drops progress the coordinator is too busy to take, where emit blocks. See README, "Who owns which state".
func (spec AttemptSpec) offer(event AttemptEvent) bool {
	select {
	case spec.Events <- event:
		return true
	default:
		return false
	}
}
