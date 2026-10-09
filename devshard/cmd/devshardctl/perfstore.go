package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// PerfStore persists host performance samples, the request log and request accounting.
type PerfStore interface {
	Close() error
	perfSampleStore
	requestAccountingStore
	perfPruneStore
}

// perfSampleStore is what PerfTracker reads at startup and writes per request.
type perfSampleStore interface {
	InsertSample(ctx context.Context, sample RequestSample) error
	InsertRequest(ctx context.Context, rec RequestRecord) error
	LoadSamples(ctx context.Context) ([]RequestSample, error)
	LoadRequests(ctx context.Context) ([]RequestRecord, error)
	BackfillLegacyEscrowSamples(ctx context.Context, sourceEscrow, sourcePath string, participantKeys []string) ([]RequestSample, error)
}

// requestAccountingStore is the per-request accounting the gateway records and the accounting endpoint reads.
type requestAccountingStore interface {
	UpsertAccountingRequest(ctx context.Context, requestID, escrowID, model string, startedAt time.Time) error
	UpsertAccountingAttempt(ctx context.Context, attempt RequestAccountingAttempt) error
	CompleteAccountingRequest(ctx context.Context, requestID, escrowID string, winnerNonce uint64, decision, outcome string, completedAt time.Time) error
	UpsertAccountingAlias(ctx context.Context, requestID, escrowID, sourceRequestID, sourceEscrowID, reason string, createdAt time.Time) error
	ImportRequestAccounting(ctx context.Context, sourcePath, escrowID string) (int64, int64, error)
	FindAccountingRequest(ctx context.Context, requestID, escrowID string) (RequestAccountingRecord, bool, error)
}

// perfPruneStore is what the pruner reads and deletes for samples and the request log.
type perfPruneStore interface {
	hostSampleBoundaryBatch(ctx context.Context, stopBefore time.Time, beforeID int64, limit int) (boundary, lastScannedID int64, scanned int, err error)
	requestLogRetentionBoundary(ctx context.Context) (int64, error)
	deleteHostSamplesUpTo(ctx context.Context, boundaryID int64, limit int) (int64, error)
	deleteRequestLogUpTo(ctx context.Context, boundaryID int64, limit int) (int64, error)
}

// accountingPruneStore is SQLite-only: on shared Postgres another replica's escrow may be missing from this replica's ledger.
type accountingPruneStore interface {
	deleteUnretainedAccountingRequests(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (lastRowID int64, scanned int, deleted int64, err error)
	deleteUnretainedAccountingAttempts(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (lastRowID int64, scanned int, deleted int64, err error)
	deleteUnretainedAccountingAliases(ctx context.Context, afterRowID int64, limit int, retainsEscrow func(string) bool) (lastRowID int64, scanned int, deleted int64, err error)
}

var _ accountingPruneStore = (*sqlitePerfStore)(nil)

var _ PerfStore = (*sqlitePerfStore)(nil)

type sqlitePerfStore struct {
	db   *sql.DB
	path string
}

func newSQLitePerfStore(dbPath string) (*sqlitePerfStore, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open perf db: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, p := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("pragma %s: %w", p, err)
		}
	}

	schema := `
	CREATE TABLE IF NOT EXISTS perf_host_samples (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		host_idx     INTEGER NOT NULL,
		participant_key TEXT NOT NULL DEFAULT '',
		responsive   INTEGER NOT NULL,
		send_time    TEXT NOT NULL,
		receipt_time TEXT NOT NULL,
		first_token  TEXT NOT NULL,
		total_time_ms REAL NOT NULL,
		input_tokens INTEGER NOT NULL,
		source_escrow TEXT NOT NULL DEFAULT '',
		source_sample_id INTEGER
	);
	CREATE TABLE IF NOT EXISTS perf_request_log (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		timestamp       TEXT NOT NULL,
		model           TEXT NOT NULL DEFAULT '',
		input_tokens    INTEGER NOT NULL,
		winner_host_idx INTEGER NOT NULL,
		winner_nonce    INTEGER NOT NULL,
		decision        TEXT NOT NULL,
		hosts_json      TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS request_accounting (
		request_id   TEXT NOT NULL,
		escrow_id    TEXT NOT NULL,
		model        TEXT NOT NULL DEFAULT '',
		started_at   TEXT NOT NULL,
		completed_at TEXT NOT NULL DEFAULT '',
		outcome      TEXT NOT NULL DEFAULT 'running',
		decision     TEXT NOT NULL DEFAULT '',
		winner_nonce INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (request_id, escrow_id)
	);
	CREATE TABLE IF NOT EXISTS request_accounting_attempts (
		request_id      TEXT NOT NULL,
		escrow_id       TEXT NOT NULL,
		nonce           INTEGER NOT NULL,
		host_idx        INTEGER NOT NULL,
		participant_key TEXT NOT NULL DEFAULT '',
		probe           INTEGER NOT NULL DEFAULT 0,
		winner          INTEGER NOT NULL DEFAULT 0,
		created_at      TEXT NOT NULL,
		PRIMARY KEY (request_id, escrow_id, nonce)
	);
	CREATE TABLE IF NOT EXISTS request_accounting_aliases (
		request_id        TEXT NOT NULL,
		escrow_id         TEXT NOT NULL,
		source_request_id TEXT NOT NULL,
		source_escrow_id  TEXT NOT NULL,
		reason            TEXT NOT NULL DEFAULT '',
		created_at        TEXT NOT NULL,
		PRIMARY KEY (request_id, escrow_id)
	);
	`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create perf schema: %w", err)
	}
	for _, col := range []struct {
		name string
		ddl  string
	}{
		{"participant_key", "TEXT NOT NULL DEFAULT ''"},
		{"source_escrow", "TEXT NOT NULL DEFAULT ''"},
		{"source_sample_id", "INTEGER"},
	} {
		if err := ensureColumn(db, "perf_host_samples", col.name, col.ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate perf samples: %w", err)
		}
	}
	if err := ensureColumn(db, "perf_request_log", "model", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate perf request log: %w", err)
	}
	for _, col := range []struct {
		table string
		name  string
		ddl   string
	}{
		{"request_accounting", "model", "TEXT NOT NULL DEFAULT ''"},
		{"request_accounting", "completed_at", "TEXT NOT NULL DEFAULT ''"},
		{"request_accounting", "outcome", "TEXT NOT NULL DEFAULT 'running'"},
		{"request_accounting", "decision", "TEXT NOT NULL DEFAULT ''"},
		{"request_accounting", "winner_nonce", "INTEGER NOT NULL DEFAULT 0"},
		{"request_accounting_attempts", "participant_key", "TEXT NOT NULL DEFAULT ''"},
		{"request_accounting_attempts", "probe", "INTEGER NOT NULL DEFAULT 0"},
		{"request_accounting_attempts", "winner", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(db, col.table, col.name, col.ddl); err != nil {
			db.Close()
			return nil, fmt.Errorf("migrate request accounting: %w", err)
		}
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS perf_host_samples_source_idx ON perf_host_samples(source_escrow, source_sample_id) WHERE source_sample_id IS NOT NULL`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create perf source index: %w", err)
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS request_accounting_attempts_lookup_idx ON request_accounting_attempts(request_id, escrow_id)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create request accounting attempt index: %w", err)
	}

	return &sqlitePerfStore{db: db, path: dbPath}, nil
}

func (s *sqlitePerfStore) Close() error {
	return s.db.Close()
}

func (s *sqlitePerfStore) InsertSample(ctx context.Context, sample RequestSample) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO perf_host_samples (host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sample.HostIdx,
		sample.ParticipantKey,
		boolToInt(sample.Responsive),
		timeToStr(sample.SendTime),
		timeToStr(sample.ReceiptTime),
		timeToStr(sample.FirstToken),
		float64(sample.TotalTime.Milliseconds()),
		sample.InputTokens,
	)
	return err
}

func (s *sqlitePerfStore) InsertRequest(ctx context.Context, rec RequestRecord) error {
	hostsJSON, err := json.Marshal(rec.Hosts)
	if err != nil {
		return fmt.Errorf("marshal hosts: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO perf_request_log (timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.Timestamp.Format(time.RFC3339Nano),
		rec.Model,
		rec.InputTokens,
		rec.WinnerHostIdx,
		rec.WinnerNonce,
		rec.Decision,
		string(hostsJSON),
	)
	return err
}

// LoadSamples returns recent participant-keyed samples, reading newest first and stopping past the window. See devshard/docs/host-health.md, "perf.db: what startup reads and what is pruned".
func (s *sqlitePerfStore) LoadSamples(ctx context.Context) ([]RequestSample, error) {
	walk := newSampleWindowWalk(time.Now())
	rows, err := s.db.QueryContext(ctx, `SELECT host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow
		 FROM perf_host_samples ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			hostIdx        int
			participantKey string
			responsive     int
			sendStr        string
			receiptStr     string
			firstStr       string
			totalMs        float64
			inputTokens    uint64
			sourceEscrow   string
		)
		if err := rows.Scan(&hostIdx, &participantKey, &responsive, &sendStr, &receiptStr, &firstStr, &totalMs, &inputTokens, &sourceEscrow); err != nil {
			return nil, err
		}
		sample := RequestSample{
			HostIdx:        hostIdx,
			ParticipantKey: participantKey,
			Responsive:     responsive != 0,
			SendTime:       strToTime(sendStr),
			ReceiptTime:    strToTime(receiptStr),
			FirstToken:     strToTime(firstStr),
			TotalTime:      time.Duration(totalMs) * time.Millisecond,
			InputTokens:    inputTokens,
		}
		if !walk.take(sample, sourceEscrow) {
			break
		}
	}
	return walk.oldestFirst(), rows.Err()
}

// LoadRequests returns the most recent requestLogSize request records.
func (s *sqlitePerfStore) LoadRequests(ctx context.Context) ([]RequestRecord, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT timestamp, model, input_tokens, winner_host_idx, winner_nonce, decision, hosts_json
		 FROM perf_request_log ORDER BY id DESC LIMIT ?`, requestLogSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []RequestRecord
	for rows.Next() {
		var (
			tsStr       string
			model       string
			inputTokens uint64
			winnerIdx   int
			winnerNonce uint64
			decision    string
			hostsJSON   string
		)
		if err := rows.Scan(&tsStr, &model, &inputTokens, &winnerIdx, &winnerNonce, &decision, &hostsJSON); err != nil {
			return nil, err
		}
		rec := RequestRecord{
			Timestamp:     strToTime(tsStr),
			Model:         model,
			InputTokens:   inputTokens,
			WinnerHostIdx: winnerIdx,
			WinnerNonce:   winnerNonce,
			Decision:      decision,
		}
		if err := json.Unmarshal([]byte(hostsJSON), &rec.Hosts); err != nil {
			return nil, fmt.Errorf("unmarshal hosts: %w", err)
		}
		records = append(records, rec)
	}

	slices.Reverse(records)
	return records, rows.Err()
}

// sampleWindow is the oldest send time startup loads and the older one past which a live sample ends the newest-first walk.
func sampleWindow(now time.Time) (cutoff, stopBefore time.Time) {
	if ParticipantPerfWindow <= 0 {
		return time.Time{}, time.Time{}
	}
	cutoff = now.Add(-2*ParticipantPerfWindow - time.Hour)
	return cutoff, cutoff.Add(-time.Hour)
}

func isPastSampleWindow(sendTime time.Time, sourceEscrow string, stopBefore time.Time) bool {
	return !stopBefore.IsZero() && sourceEscrow == "" && !sendTime.IsZero() && sendTime.Before(stopBefore)
}

// sampleWindowWalk is LoadSamples' newest-first walk, shared by both backends: it skips samples before the window and ends at the first live one past the stop line.
type sampleWindowWalk struct {
	cutoff     time.Time
	stopBefore time.Time
	limit      int
	samples    []RequestSample
}

func newSampleWindowWalk(now time.Time) *sampleWindowWalk {
	cutoff, stopBefore := sampleWindow(now)
	return &sampleWindowWalk{cutoff: cutoff, stopBefore: stopBefore, limit: PerfWindowSize * 4096}
}

// take reports whether the walk goes on past this sample.
func (walk *sampleWindowWalk) take(sample RequestSample, sourceEscrow string) bool {
	if len(walk.samples) >= walk.limit {
		return false
	}
	if !walk.cutoff.IsZero() && sample.SendTime.Before(walk.cutoff) {
		return !isPastSampleWindow(sample.SendTime, sourceEscrow, walk.stopBefore)
	}
	if sample.ParticipantKey != "" {
		walk.samples = append(walk.samples, sample)
	}
	return len(walk.samples) < walk.limit
}

func (walk *sampleWindowWalk) oldestFirst() []RequestSample {
	slices.Reverse(walk.samples)
	return walk.samples
}

// legacyPerfSample is one sample of a per-escrow perf file, with the stored text kept as written.
type legacyPerfSample struct {
	sourceSampleID int64
	sendTime       string
	receiptTime    string
	firstToken     string
	totalTimeMs    float64
	sample         RequestSample
}

func isLegacyPerfSource(sourceEscrow, sourcePath, ownPath string) bool {
	if strings.TrimSpace(sourceEscrow) == "" || strings.TrimSpace(sourcePath) == "" {
		return false
	}
	return ownPath == "" || filepath.Clean(sourcePath) != filepath.Clean(ownPath)
}

// readLegacyPerfSamples reads a per-escrow perf file, keeping only hosts it can name by participant key.
func readLegacyPerfSamples(ctx context.Context, sourcePath string, participantKeys []string) ([]legacyPerfSample, error) {
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open legacy perf source: %w", err)
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)

	rows, err := sourceDB.QueryContext(ctx, `SELECT id, host_idx, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens FROM perf_host_samples ORDER BY id ASC`)
	if err != nil {
		if isSQLiteMissingTable(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read legacy perf samples: %w", err)
	}
	defer rows.Close()

	var samples []legacyPerfSample
	for rows.Next() {
		var (
			legacy     legacyPerfSample
			hostIdx    int
			responsive int
			inputCount uint64
		)
		if err := rows.Scan(&legacy.sourceSampleID, &hostIdx, &responsive, &legacy.sendTime, &legacy.receiptTime, &legacy.firstToken, &legacy.totalTimeMs, &inputCount); err != nil {
			return nil, err
		}
		if hostIdx < 0 || hostIdx >= len(participantKeys) || strings.TrimSpace(participantKeys[hostIdx]) == "" {
			continue
		}
		legacy.sample = RequestSample{
			HostIdx:        hostIdx,
			ParticipantKey: strings.TrimSpace(participantKeys[hostIdx]),
			Responsive:     responsive != 0,
			SendTime:       strToTime(legacy.sendTime),
			ReceiptTime:    strToTime(legacy.receiptTime),
			FirstToken:     strToTime(legacy.firstToken),
			TotalTime:      time.Duration(legacy.totalTimeMs) * time.Millisecond,
			InputTokens:    inputCount,
		}
		samples = append(samples, legacy)
	}
	return samples, rows.Err()
}

func (s *sqlitePerfStore) BackfillLegacyEscrowSamples(ctx context.Context, sourceEscrow, sourcePath string, participantKeys []string) ([]RequestSample, error) {
	if s == nil || s.db == nil || !isLegacyPerfSource(sourceEscrow, sourcePath, s.path) {
		return nil, nil
	}
	legacySamples, err := readLegacyPerfSamples(ctx, sourcePath, participantKeys)
	if err != nil {
		return nil, err
	}
	if len(legacySamples) == 0 {
		return nil, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO perf_host_samples
		(host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	var inserted []RequestSample
	for _, legacy := range legacySamples {
		res, err := stmt.ExecContext(ctx, legacy.sample.HostIdx, legacy.sample.ParticipantKey, boolToInt(legacy.sample.Responsive),
			legacy.sendTime, legacy.receiptTime, legacy.firstToken, legacy.totalTimeMs, legacy.sample.InputTokens, sourceEscrow, legacy.sourceSampleID)
		if err != nil {
			return nil, err
		}
		if insertedRows, _ := res.RowsAffected(); insertedRows > 0 {
			inserted = append(inserted, legacy.sample)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
}

// importSourceID is this perf.db file's id, made on first use and kept in the file, so every replica's file is imported once.
func (s *sqlitePerfStore) importSourceID(ctx context.Context) (string, error) {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS perf_store_meta (key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		return "", err
	}
	fresh := make([]byte, 16)
	if _, err := rand.Read(fresh); err != nil {
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO perf_store_meta (key, value) VALUES ('import_source_id', ?)`, hex.EncodeToString(fresh)); err != nil {
		return "", err
	}
	var sourceID string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM perf_store_meta WHERE key = 'import_source_id'`).Scan(&sourceID)
	return sourceID, err
}

// readImportPage returns up to perfImportPageSize rows after afterKey without their key column, and the last key read.
func (s *sqlitePerfStore) readImportPage(ctx context.Context, selectPage string, afterKey int64) ([][]any, int64, error) {
	rows, err := s.db.QueryContext(ctx, selectPage, afterKey, perfImportPageSize)
	if err != nil {
		return nil, afterKey, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, afterKey, err
	}
	var page [][]any
	lastKey := afterKey
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range values {
			pointers[index] = &values[index]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, afterKey, err
		}
		key, isInteger := values[0].(int64)
		if !isInteger {
			return nil, afterKey, fmt.Errorf("import key %v is not an integer", values[0])
		}
		lastKey = key
		page = append(page, values[1:])
	}
	return page, lastKey, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func timeToStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

func strToTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
