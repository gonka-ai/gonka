package storage

import (
	"fmt"
	"sync"
)

// maxQueuedObsOps bounds the per-escrow queue held during a repair. Overflow
// drops the newest ops, matching the live path, which already drops obs batches
// when too many writes are in flight. Dropped counters are recovered by the
// next repair.
const maxQueuedObsOps = 8192

// ObsRepairGate wraps a Storage so a validation-obs rebuild can run in the
// background against a live escrow.
//
// A rebuild is only correct with exclusive access: it clears both obs tables
// and refills them from the diff journal, and a concurrent write would be
// counted twice, because the drain deletes the live row that
// RecordValidationsAppliedOnce dedups against. Rather than block the apply
// path, the gate queues live obs writes for that escrow in arrival order and
// applies them once the rebuild finishes, which yields the same result as
// running them after it. The rebuild covers the journal up to the nonce it
// started at, and diffs applied during the window are past that nonce, so the
// two never overlap.
//
// Queueing is safe because obs writes are already best-effort everywhere:
// recording is dropped under backpressure and deferred drains are logged and
// skipped rather than failing a commit. Every other method, reads included,
// passes straight through.
type ObsRepairGate struct {
	Storage

	mu     sync.Mutex
	queues map[string]*obsQueue
}

// obsQueue has its own lock so a queued write never contends with the gate
// map. Lock order is always gate then queue; push only ever takes the queue.
type obsQueue struct {
	mu      sync.Mutex
	ops     []obsOp
	dropped int
}

// obsOp is a queued obs write. Exactly one of the two forms is set.
type obsOp struct {
	entries []ValidationObsEntry
	drainID uint64
	drain   bool
}

func NewObsRepairGate(inner Storage) *ObsRepairGate {
	return &ObsRepairGate{Storage: inner}
}

// Ready forwards the optional readiness probe, which callers type-assert on the
// outermost store.
func (g *ObsRepairGate) Ready() bool {
	if r, ok := g.Storage.(interface{ Ready() bool }); ok {
		return r.Ready()
	}
	return true
}

// RepairInProgress reports whether an escrow's obs writes are being queued.
func (g *ObsRepairGate) RepairInProgress(escrowID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, ok := g.queues[escrowID]
	return ok
}

func (g *ObsRepairGate) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	if q := g.queueFor(escrowID); q != nil {
		q.push(obsOp{entries: append([]ValidationObsEntry(nil), entries...)})
		return nil
	}
	return g.Storage.RecordValidationsAppliedOnce(escrowID, entries)
}

func (g *ObsRepairGate) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	if q := g.queueFor(escrowID); q != nil {
		q.push(obsOp{drainID: inferenceID, drain: true})
		return nil
	}
	return g.Storage.DrainInferenceValidationObs(escrowID, inferenceID)
}

// queueFor returns the active queue for an escrow, or nil to write through.
// The queue is returned while the gate lock is dropped, so push takes its own
// lock; ops appended after a drain round still get picked up by the next one.
func (g *ObsRepairGate) queueFor(escrowID string) *obsQueue {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.queues[escrowID]
}

func (q *obsQueue) push(op obsOp) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.ops) >= maxQueuedObsOps {
		q.dropped++
		return
	}
	q.ops = append(q.ops, op)
}

// RepairValidationObs runs rebuild with exclusive access to an escrow's obs
// rows, then applies everything the live path wrote in the meantime. rebuild
// receives the underlying store so its own writes bypass the queue.
//
// Concurrent repairs for one escrow are rejected rather than serialized: the
// caller would otherwise wait out a full journal replay holding nothing useful.
func (g *ObsRepairGate) RepairValidationObs(escrowID string, rebuild func(Storage) error) error {
	if err := g.open(escrowID); err != nil {
		return err
	}
	rebuildErr := rebuild(g.Storage)
	// Flush regardless: on failure the queued writes are still the only record
	// of what the live path did during the window.
	flushErr := g.drainAndClose(escrowID)
	if rebuildErr != nil {
		return rebuildErr
	}
	return flushErr
}

func (g *ObsRepairGate) open(escrowID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.queues == nil {
		g.queues = make(map[string]*obsQueue)
	}
	if _, busy := g.queues[escrowID]; busy {
		return fmt.Errorf("validation obs repair already running for escrow %s", escrowID)
	}
	g.queues[escrowID] = &obsQueue{}
	return nil
}

// maxObsDrainRounds bounds the flush so a continuously busy escrow cannot keep
// the gate open indefinitely. After the last round the gate closes and any
// remaining ops are applied with the gate already open to write-through, which
// can only reorder writes the live path itself treats as best-effort.
const maxObsDrainRounds = 64

func (g *ObsRepairGate) drainAndClose(escrowID string) error {
	var firstErr error
	totalDropped := 0
	for round := 0; ; round++ {
		g.mu.Lock()
		q := g.queues[escrowID]
		if q == nil {
			g.mu.Unlock()
			break
		}
		ops, dropped := q.take()
		totalDropped += dropped
		last := len(ops) == 0 || round >= maxObsDrainRounds
		if last {
			// Closing while holding the gate lock means no writer can observe
			// an empty queue and still be queued.
			delete(g.queues, escrowID)
		}
		g.mu.Unlock()

		if err := g.applyOps(escrowID, ops); err != nil && firstErr == nil {
			firstErr = err
		}
		if last {
			break
		}
	}
	if firstErr == nil && totalDropped > 0 {
		return fmt.Errorf("validation obs repair for escrow %s dropped %d queued writes", escrowID, totalDropped)
	}
	return firstErr
}

func (q *obsQueue) take() ([]obsOp, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	ops, dropped := q.ops, q.dropped
	q.ops, q.dropped = nil, 0
	return ops, dropped
}

func (g *ObsRepairGate) applyOps(escrowID string, ops []obsOp) error {
	var firstErr error
	for _, op := range ops {
		var err error
		if op.drain {
			err = g.Storage.DrainInferenceValidationObs(escrowID, op.drainID)
		} else {
			err = g.Storage.RecordValidationsAppliedOnce(escrowID, op.entries)
		}
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("apply queued validation obs write: %w", err)
		}
	}
	return firstErr
}
