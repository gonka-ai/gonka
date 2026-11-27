package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/internal/logkey"
	"devshard/logging"
)

// Ledger writes from one goroutine and sheds rows rather than block: its caller is on the response path.
type Ledger struct {
	rows      RequestRows
	retention Retention
	now       func() time.Time

	queue chan RequestRecord
	done  chan struct{}

	mu     sync.RWMutex
	closed bool

	written     atomic.Int64
	dropped     atomic.Int64
	failed      atomic.Int64
	sweepFailed atomic.Int64
	lastSweep   time.Time
}

func validateLedger(retention Retention, now func() time.Time) error {
	switch {
	case now == nil:
		return errors.New("accounting ledger: clock is required")
	case retention.MaxAge <= 0:
		return fmt.Errorf("accounting ledger: retention max age %v must be positive", retention.MaxAge)
	case retention.MaxRows < 1:
		return fmt.Errorf("accounting ledger: retention max rows %d must be >= 1", retention.MaxRows)
	}
	return nil
}

func newLedger(rows RequestRows, retention Retention, now func() time.Time) *Ledger {
	return &Ledger{
		rows:      rows,
		retention: retention,
		now:       now,
		queue:     make(chan RequestRecord, ledgerQueueDepth),
		done:      make(chan struct{}),
	}
}

// Record queues the row; a full queue or a closed ledger counts it as dropped.
func (l *Ledger) Record(record RequestRecord) {
	if record.RequestID == "" {
		return
	}
	record.RecordedAt = l.now()
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.closed {
		l.dropped.Add(1)
		return
	}
	select {
	case l.queue <- record:
	default:
		l.dropped.Add(1)
	}
}

// Find reads one row back.
func (l *Ledger) Find(ctx context.Context, requestID string) (RequestRecord, bool, error) {
	return l.rows.FindRequest(ctx, requestID)
}

// Stats returns the counters of what the writer has done so far.
func (l *Ledger) Stats() LedgerStats {
	return LedgerStats{
		Written:     l.written.Load(),
		Dropped:     l.dropped.Load(),
		Failed:      l.failed.Load(),
		SweepFailed: l.sweepFailed.Load(),
	}
}

// Close drains what is queued; a lost row is counted in Stats, never returned. See README.md, "The accounting ledger".
func (l *Ledger) Close() error {
	l.mu.Lock()
	alreadyClosed := l.closed
	l.closed = true
	l.mu.Unlock()
	if alreadyClosed {
		return nil
	}
	close(l.queue)
	<-l.done
	return nil
}

func (l *Ledger) isClosed() bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.closed
}

func (l *Ledger) run() {
	defer close(l.done)
	for record := range l.queue {
		if err := l.rows.InsertRequest(context.Background(), record); err != nil {
			l.failed.Add(1)
			logging.Warn("accounting row lost: the insert failed",
				logkey.Request, record.RequestID, logkey.Escrow, record.EscrowID, logkey.Nonce, record.WinnerNonce,
				logkey.Model, record.Model, logkey.Outcome, string(record.Outcome), logkey.Error, err)
		} else {
			l.written.Add(1)
		}
		if now := l.now(); l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= retentionSweepEvery {
			l.sweep(now)
		}
	}
	l.sweep(l.now())
}

func (l *Ledger) sweep(now time.Time) {
	l.lastSweep = now
	ctx := context.Background()
	if err := l.rows.DeleteRequestsBefore(ctx, now.Add(-l.retention.MaxAge)); err != nil {
		l.sweepFailed.Add(1)
	}
	if err := l.rows.DeleteRequestsBeyond(ctx, l.retention.MaxRows); err != nil {
		l.sweepFailed.Add(1)
	}
}
