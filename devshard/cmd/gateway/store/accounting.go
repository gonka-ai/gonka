package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"devshard/cmd/gateway/filters"
)

// Fixed-width where RFC3339Nano is not, because retention orders timestamps as text. See README.md, "Timestamps".
const storedTimeLayout = "2006-01-02T15:04:05.000000000Z07:00"

type RequestOutcome string

const (
	RequestSettled  RequestOutcome = "settled"
	RequestFailed   RequestOutcome = "failed"
	RequestNoEscrow RequestOutcome = "no_escrow"
)

// RequestRecord carries no monetary cost: the race outcome that writes it knows only token counts.
type RequestRecord struct {
	RequestID          string
	EscrowID           string
	Model              string
	Outcome            RequestOutcome
	Decision           string
	Stream             bool
	WinnerNonce        uint64
	WinnerParticipant  string
	WinnerHost         string
	WinnerHostIdx      int
	Attempts           int
	InputTokens        uint64
	InputTokensSource  filters.InputTokensSource
	PromptTokens       int64
	WinnerOutputTokens int64
	TotalOutputTokens  int64
	EscrowMissing      bool
	BalanceExhausted   bool
	StartedAt          time.Time
	CompletedAt        time.Time
	FirstTokenMS       int64
	DurationMS         int64
	RecordedAt         time.Time
}

// Retention rejects a zero on either axis, so an unbounded ledger cannot be configured.
type Retention struct {
	MaxAge  time.Duration
	MaxRows int64
}

type LedgerStats struct {
	Written     int64
	Dropped     int64
	Failed      int64
	SweepFailed int64
}

const (
	ledgerQueueDepth    = 1024
	retentionSweepEvery = time.Minute
)

// Ledger writes from one goroutine and sheds rows rather than block: its caller is on the response path.
type Ledger struct {
	store     *Store
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

// NewLedger registers the writer with the store, whose Close drains it before the database handle goes.
func (s *Store) NewLedger(retention Retention, now func() time.Time) (*Ledger, error) {
	switch {
	case now == nil:
		return nil, errors.New("accounting ledger: clock is required")
	case retention.MaxAge <= 0:
		return nil, fmt.Errorf("accounting ledger: retention max age %v must be positive", retention.MaxAge)
	case retention.MaxRows < 1:
		return nil, fmt.Errorf("accounting ledger: retention max rows %d must be >= 1", retention.MaxRows)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ledger != nil && !s.ledger.isClosed() {
		return nil, errors.New("accounting ledger: already open")
	}
	ledger := &Ledger{
		store:     s,
		retention: retention,
		now:       now,
		queue:     make(chan RequestRecord, ledgerQueueDepth),
		done:      make(chan struct{}),
	}
	s.ledger = ledger
	go ledger.run()
	return ledger, nil
}

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

func (l *Ledger) Find(ctx context.Context, requestID string) (RequestRecord, bool, error) {
	return l.store.FindRequest(ctx, requestID)
}

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
		if err := l.insert(record); err != nil {
			l.failed.Add(1)
		} else {
			l.written.Add(1)
		}
		// Swept whatever the insert did: gating retention behind success turns a write problem into an unbounded table.
		if now := l.now(); l.lastSweep.IsZero() || now.Sub(l.lastSweep) >= retentionSweepEvery {
			l.sweep(now)
		}
	}
	l.sweep(l.now())
}

func (l *Ledger) insert(record RequestRecord) error {
	_, err := l.store.db.Exec(`
		INSERT INTO request_accounting (
			request_id, escrow_id, model, outcome, decision, stream,
			winner_nonce, winner_participant, winner_host, winner_host_idx,
			attempts, input_tokens, input_tokens_source, prompt_tokens, winner_output_tokens, total_output_tokens,
			escrow_missing, balance_exhausted,
			started_at, completed_at, first_token_ms, duration_ms, recorded_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(request_id) DO NOTHING`,
		record.RequestID, record.EscrowID, record.Model, string(record.Outcome), record.Decision,
		record.Stream,
		record.WinnerNonce, record.WinnerParticipant, record.WinnerHost, record.WinnerHostIdx,
		record.Attempts, record.InputTokens, record.InputTokensSource, record.PromptTokens, record.WinnerOutputTokens, record.TotalOutputTokens,
		record.EscrowMissing, record.BalanceExhausted,
		FormatTime(record.StartedAt), FormatTime(record.CompletedAt),
		record.FirstTokenMS, record.DurationMS, FormatTime(record.RecordedAt))
	return err
}

func (l *Ledger) sweep(now time.Time) {
	l.lastSweep = now
	cutoff := FormatTime(now.Add(-l.retention.MaxAge))
	// Independent, so a busy lock on the age delete cannot stop the row cap running. See README.md, "Retention".
	if _, err := l.store.db.Exec(`DELETE FROM request_accounting WHERE recorded_at < ?`, cutoff); err != nil {
		l.sweepFailed.Add(1)
	}
	if _, err := l.store.db.Exec(`
		DELETE FROM request_accounting WHERE request_id IN (
			SELECT request_id FROM request_accounting
			ORDER BY recorded_at DESC, request_id DESC LIMIT -1 OFFSET ?)`, l.retention.MaxRows); err != nil {
		l.sweepFailed.Add(1)
	}
}

func (s *Store) FindRequest(ctx context.Context, requestID string) (RequestRecord, bool, error) {
	var record RequestRecord
	var outcome, startedAt, completedAt, recordedAt string
	err := s.db.QueryRowContext(ctx, `
		SELECT request_id, escrow_id, model, outcome, decision, stream,
			winner_nonce, winner_participant, winner_host, winner_host_idx,
			attempts, input_tokens, input_tokens_source, prompt_tokens, winner_output_tokens, total_output_tokens,
			escrow_missing, balance_exhausted,
			started_at, completed_at, first_token_ms, duration_ms, recorded_at
		FROM request_accounting WHERE request_id = ?`, requestID).
		Scan(&record.RequestID, &record.EscrowID, &record.Model, &outcome, &record.Decision, &record.Stream,
			&record.WinnerNonce, &record.WinnerParticipant, &record.WinnerHost, &record.WinnerHostIdx,
			&record.Attempts, &record.InputTokens, &record.InputTokensSource, &record.PromptTokens, &record.WinnerOutputTokens, &record.TotalOutputTokens,
			&record.EscrowMissing, &record.BalanceExhausted,
			&startedAt, &completedAt, &record.FirstTokenMS, &record.DurationMS, &recordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RequestRecord{}, false, nil
	}
	if err != nil {
		return RequestRecord{}, false, fmt.Errorf("loading request %s: %w", requestID, err)
	}
	record.Outcome = RequestOutcome(outcome)
	timestamps := []struct {
		target *time.Time
		raw    string
	}{
		{&record.StartedAt, startedAt},
		{&record.CompletedAt, completedAt},
		{&record.RecordedAt, recordedAt},
	}
	for _, timestamp := range timestamps {
		parsed, err := parseTime(timestamp.raw)
		if err != nil {
			return RequestRecord{}, false, fmt.Errorf("parsing request %s timestamp %q: %w", requestID, timestamp.raw, err)
		}
		*timestamp.target = parsed
	}
	return record, true, nil
}

// FormatTime renders every stored timestamp and the accounting API's, so the retention cutoff cannot drift from the rows it compares. See README.md, "Timestamps".
func FormatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(storedTimeLayout)
}

// parseTime is the only reader of what FormatTime writes, the empty zero timestamp included.
func parseTime(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, raw)
}
