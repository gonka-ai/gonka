// Package journal is the one place a lifecycle step becomes a log line or a nonce ledger fact. See README.md.
package journal

import (
	"fmt"
	"sync"

	"devshard/cmd/gateway/accounting"
	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/scheduler"
	"devshard/logging"
	"devshard/types"
)

// See README.md, "Order and the locks it takes".
const (
	defaultMoneyCeiling    = 100_000
	defaultProgressBacklog = 8_192
	retainedBatchCapacity  = 4_096

	skippedProgressMessage = "journal skipped progress lines"
	lateEventMessage       = "journal received an event after it closed"
)

type logSink interface {
	Info(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
}

type ledgerSink interface {
	RecordRace(outcome engine.RaceOutcome)
	RecordGhost(escrowID string, nonce uint64, reason string)
	RecordTimeout(event engine.TimeoutEvent)
	RecordDiffFacts(escrowID string, facts []DiffFact)
	RecordProbe(escrowID string, attempt accounting.Attempt) error
}

// Settings wires a journal: zero limits take the defaults, and a nil Ledger means nonce accounting is off.
type Settings struct {
	Lines           logSink
	Ledger          ledgerSink
	MoneyCeiling    int
	ProgressBacklog int
}

// Journal hands every lifecycle step to its sinks in order, from one consumer goroutine. See README.md.
type Journal struct {
	lines           logSink
	ledger          ledgerSink
	moneyCeiling    int
	progressBacklog int
	done            chan struct{}

	mu               sync.Mutex
	arrived          *sync.Cond
	drained          *sync.Cond
	pending          []queuedEvent
	spare            []queuedEvent
	pendingMoney     int
	pendingProgress  int
	accepted         uint64
	delivered        uint64
	skippedSinceLine uint64
	skippedWritten   uint64
	closed           bool
	lateKinds        [kindCount]bool

	moneyRefused    uint64
	progressDropped uint64
	lateEvents      uint64
}

// gatewayLog writes through devshard/logging, so internal/logcapture sees every line.
type gatewayLog struct{}

func (gatewayLog) Info(msg string, keyvals ...any)  { logging.Info(msg, keyvals...) }
func (gatewayLog) Warn(msg string, keyvals ...any)  { logging.Warn(msg, keyvals...) }
func (gatewayLog) Error(msg string, keyvals ...any) { logging.Error(msg, keyvals...) }

// New starts the consumer; Close stops it.
func New(settings Settings) *Journal {
	created := &Journal{
		lines:           settings.Lines,
		ledger:          settings.Ledger,
		moneyCeiling:    settings.MoneyCeiling,
		progressBacklog: settings.ProgressBacklog,
		done:            make(chan struct{}),
	}
	if created.lines == nil {
		created.lines = gatewayLog{}
	}
	if created.moneyCeiling <= 0 {
		created.moneyCeiling = defaultMoneyCeiling
	}
	if created.progressBacklog <= 0 {
		created.progressBacklog = defaultProgressBacklog
	}
	created.arrived = sync.NewCond(&created.mu)
	created.drained = sync.NewCond(&created.mu)
	go created.run()
	return created
}

// Flush returns once every event accepted and every drop counted before it has reached the sinks; never call it from a sink.
func (j *Journal) Flush() {
	j.mu.Lock()
	defer j.mu.Unlock()
	acceptedBefore, droppedBefore := j.accepted, j.progressDropped
	for j.delivered < acceptedBefore || j.skippedWritten < droppedBefore {
		j.drained.Wait()
	}
}

// Close refuses later events, drains the accepted ones, and reports money-lane events refused over the journal's life.
func (j *Journal) Close() error {
	j.mu.Lock()
	j.closed = true
	j.arrived.Signal()
	j.mu.Unlock()
	<-j.done

	j.mu.Lock()
	defer j.mu.Unlock()
	if j.moneyRefused > 0 {
		return fmt.Errorf("journal refused %d money-lane events past its ceiling of %d", j.moneyRefused, j.moneyCeiling)
	}
	return nil
}

// Counts is what the journal refused, dropped and received after Close.
func (j *Journal) Counts() (moneyRefused, progressDropped, lateEvents uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.moneyRefused, j.progressDropped, j.lateEvents
}

// RecordRace hands a race's outcome to the ledger; its attempts are shared with the response path and never written.
func (j *Journal) RecordRace(outcome engine.RaceOutcome) {
	j.emit(queuedEvent{kind: KindRaceReported, race: &outcome})
}

// RecordTimeout hands a race's timeout vote to the ledger.
func (j *Journal) RecordTimeout(vote engine.TimeoutEvent) {
	j.emit(queuedEvent{kind: KindTimeoutVote, timeout: &vote})
}

// GhostBurned hands a nonce the scheduler spent on nobody to the ledger.
func (j *Journal) GhostBurned(escrowID string, burned scheduler.Burn) {
	j.emit(queuedEvent{kind: KindNonceBurned, escrowID: escrowID, burn: burned})
}

// BurnBudgetExhausted reports an escrow that now queues callers rather than burning nonces on them.
func (j *Journal) BurnBudgetExhausted(escrowID string) {
	j.emit(queuedEvent{kind: KindBurnBudgetExhausted, escrowID: escrowID})
}

// RecordProbeTimeout hands a warmup probe's vote to the ledger without a race vote's line. See README.md, "Kinds".
func (j *Journal) RecordProbeTimeout(vote engine.TimeoutEvent) {
	j.emit(queuedEvent{kind: KindTimeoutVote, timeout: &vote, probeVote: true})
}

// RecordStep takes a step the coordinator copied at emit; a stranded nonce and a diverged host ride the money lane.
func (j *Journal) RecordStep(step engine.RaceStep) {
	j.emit(queuedEvent{kind: raceStepKind(step.Kind), raceStep: &step})
}

// RequestFinished writes the record of a finished request, raced or served from the cache.
func (j *Journal) RequestFinished(line RequestLine) {
	j.emit(queuedEvent{kind: KindRequestFinished, request: &line})
}

// RequestThrottled writes that the gateway's own limiter turned a request away.
func (j *Journal) RequestThrottled(line RequestLine) {
	j.emit(queuedEvent{kind: KindRequestThrottled, request: &line})
}

// ReplyNotCached writes that a served reply stopped mid-answer and was not cached.
func (j *Journal) ReplyNotCached(line RequestLine) {
	j.emit(queuedEvent{kind: KindReplyNotCached, request: &line})
}

// DiffComposed is called under the session lock that composed the diff: it counts before it allocates and queues only a diff that carries a ledger fact.
func (j *Journal) DiffComposed(escrowID string, diff *types.Diff) {
	facts := diffFacts(diff)
	if len(facts) == 0 {
		return
	}
	j.emit(queuedEvent{kind: KindDiffComposed, escrowID: escrowID, facts: facts})
}

// ProbeRecorded hands the warmup's own nonce to the ledger, after the escrow it was spent on was opened there.
func (j *Journal) ProbeRecorded(escrowID string, attempt accounting.Attempt) {
	j.emit(queuedEvent{kind: KindWarmupProbe, escrowID: escrowID, attempt: &attempt})
}

func raceStepKind(kind engine.RaceStepKind) Kind {
	switch kind {
	case engine.RaceStepNonceCommitted:
		return KindNonceCommitted
	case engine.RaceStepEscalationUnfilled:
		return KindEscalationUnfilled
	case engine.RaceStepAttemptCrowned:
		return KindAttemptCrowned
	case engine.RaceStepAttemptFinished:
		return KindAttemptFinished
	case engine.RaceStepNonceStranded:
		return KindNonceStranded
	case engine.RaceStepWinnerFinishLanded:
		return KindWinnerFinishLanded
	default:
		return KindHostDiverged
	}
}

// emit admits one event to its lane and never waits. See README.md, "Two lanes".
func (j *Journal) emit(entry queuedEvent) {
	j.mu.Lock()
	switch moneyLane := entry.kind.onMoneyLane(); {
	case j.closed:
		j.lateEvents++
		firstOfKind := !j.lateKinds[entry.kind]
		j.lateKinds[entry.kind] = true
		j.mu.Unlock()
		if firstOfKind {
			j.lines.Error(lateEventMessage, logkey.Kind, entry.kind.String())
		}
		return
	case moneyLane && j.pendingMoney >= j.moneyCeiling:
		j.moneyRefused++
	case !moneyLane && j.pendingProgress >= j.progressBacklog:
		j.progressDropped++
		j.skippedSinceLine++
	default:
		j.pending = append(j.pending, entry)
		j.accepted++
		if moneyLane {
			j.pendingMoney++
		} else {
			j.pendingProgress++
		}
		j.arrived.Signal()
	}
	j.mu.Unlock()
}

// emitLine queues a lifecycle line written outside a race; its kind picks the lane. See README.md, "Lifecycle lines outside a race".
func (j *Journal) emitLine(kind Kind, render func(lines logSink)) {
	j.emit(queuedEvent{kind: kind, render: render})
}

// run is the only goroutine that reaches the ledger, so the ledger sees events in the order they were accepted.
func (j *Journal) run() {
	defer close(j.done)
	for {
		batch, skipped, open := j.takeBatch()
		if !open {
			return
		}
		moneyEvents := 0
		for index := range batch {
			if batch[index].kind.onMoneyLane() {
				moneyEvents++
			}
			j.deliver(&batch[index])
			batch[index] = queuedEvent{}
		}
		if skipped > 0 {
			j.lines.Warn(skippedProgressMessage, logkey.SkippedLines, skipped)
		}
		j.finishBatch(batch, moneyEvents, skipped)
	}
}

// takeBatch waits for events or an unwritten drop and swaps the queue out, so producers keep appending while a batch is delivered.
func (j *Journal) takeBatch() (batch []queuedEvent, skipped uint64, open bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for len(j.pending) == 0 && j.skippedSinceLine == 0 && !j.closed {
		j.arrived.Wait()
	}
	if len(j.pending) == 0 && j.skippedSinceLine == 0 {
		return nil, 0, false
	}
	batch, j.pending, j.spare = j.pending, j.spare, nil
	skipped, j.skippedSinceLine = j.skippedSinceLine, 0
	return batch, skipped, true
}

// finishBatch releases the batch from its lanes only now, so a lane bounds what is accepted and not yet delivered.
func (j *Journal) finishBatch(batch []queuedEvent, moneyEvents int, skipped uint64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.delivered += uint64(len(batch))
	j.skippedWritten += skipped
	j.pendingMoney -= moneyEvents
	j.pendingProgress -= len(batch) - moneyEvents
	if cap(batch) <= retainedBatchCapacity {
		j.spare = batch[:0]
	}
	j.drained.Broadcast()
}

// deliver is the one place an event reaches a sink. See README.md, "Kinds".
func (j *Journal) deliver(entry *queuedEvent) {
	switch entry.kind {
	case KindRaceReported:
		if j.ledger != nil {
			j.ledger.RecordRace(*entry.race)
		}
	case KindTimeoutVote:
		if !entry.probeVote {
			renderTimeoutVote(j.lines, entry.timeout)
		}
		if j.ledger != nil {
			j.ledger.RecordTimeout(*entry.timeout)
		}
	case KindNonceBurned:
		renderBurn(j.lines, entry.escrowID, entry.burn)
		if j.ledger != nil {
			j.ledger.RecordGhost(entry.escrowID, entry.burn.Nonce, entry.burn.Reason)
		}
	case KindBurnBudgetExhausted:
		renderBurnBudgetExhausted(j.lines, entry.escrowID)
	case KindDiffComposed:
		if j.ledger != nil {
			j.ledger.RecordDiffFacts(entry.escrowID, entry.facts)
		}
	case KindWarmupProbe:
		if j.ledger != nil {
			if err := j.ledger.RecordProbe(entry.escrowID, *entry.attempt); err != nil {
				renderProbeRefused(j.lines, entry.escrowID, entry.attempt.Nonce, err)
			}
		}
	case KindNonceCommitted, KindEscalationUnfilled, KindAttemptCrowned, KindAttemptFinished,
		KindNonceStranded, KindHostDiverged, KindWinnerFinishLanded:
		renderRaceStep(j.lines, entry.raceStep)
	case KindRequestFinished:
		renderRequestFinished(j.lines, entry.request)
	case KindRequestThrottled:
		renderRequestThrottled(j.lines, entry.request)
	case KindReplyNotCached:
		renderReplyNotCached(j.lines, entry.request)
	case KindHostTransition, KindExcludedHostServed, KindForcedSend, KindEscrowTransition, KindChainTransition, KindFundingTransition:
		entry.render(j.lines)
	}
}
