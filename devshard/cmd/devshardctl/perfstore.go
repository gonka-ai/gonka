package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// PerfStore persists host performance samples and request records to SQLite.
type PerfStore struct {
	db   *sql.DB
	path string
}

func NewPerfStore(dbPath string) (*PerfStore, error) {
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

	return &PerfStore{db: db, path: dbPath}, nil
}

func (s *PerfStore) Close() error {
	return s.db.Close()
}

func (s *PerfStore) InsertSample(sample RequestSample) error {
	_, err := s.db.Exec(
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

func (s *PerfStore) InsertRequest(rec RequestRecord) error {
	hostsJSON, err := json.Marshal(rec.Hosts)
	if err != nil {
		return fmt.Errorf("marshal hosts: %w", err)
	}
	_, err = s.db.Exec(
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

// LoadSamples returns recent participant-keyed samples.
func (s *PerfStore) LoadSamples() ([]RequestSample, error) {
	cutoff := ""
	if ParticipantPerfWindow > 0 {
		cutoff = time.Now().Add(-2*ParticipantPerfWindow - time.Hour).Format(time.RFC3339Nano)
	}
	query := `SELECT host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens
		 FROM perf_host_samples WHERE participant_key <> ''`
	args := []any{}
	if cutoff != "" {
		query += ` AND send_time >= ?`
		args = append(args, cutoff)
	}
	query += ` ORDER BY id DESC LIMIT ?`
	args = append(args, PerfWindowSize*4096)
	rows, err := s.db.Query(
		query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var samples []RequestSample
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
		)
		if err := rows.Scan(&hostIdx, &participantKey, &responsive, &sendStr, &receiptStr, &firstStr, &totalMs, &inputTokens); err != nil {
			return nil, err
		}
		samples = append(samples, RequestSample{
			HostIdx:        hostIdx,
			ParticipantKey: participantKey,
			Responsive:     responsive != 0,
			SendTime:       strToTime(sendStr),
			ReceiptTime:    strToTime(receiptStr),
			FirstToken:     strToTime(firstStr),
			TotalTime:      time.Duration(totalMs) * time.Millisecond,
			InputTokens:    inputTokens,
		})
	}

	// Reverse so oldest-first (ring buffer expects chronological insert order).
	for i, j := 0, len(samples)-1; i < j; i, j = i+1, j-1 {
		samples[i], samples[j] = samples[j], samples[i]
	}
	return samples, rows.Err()
}

// LoadRequests returns the most recent requestLogSize request records.
func (s *PerfStore) LoadRequests() ([]RequestRecord, error) {
	rows, err := s.db.Query(
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

	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, rows.Err()
}

// Prune removes old rows beyond the retention window.
func (s *PerfStore) Prune() error {
	if ParticipantPerfWindow > 0 {
		cutoff := time.Now().Add(-2*ParticipantPerfWindow - time.Hour).Format(time.RFC3339Nano)
		if _, err := s.db.Exec(`DELETE FROM perf_host_samples WHERE send_time <> '' AND send_time < ?`, cutoff); err != nil {
			return err
		}
	}
	_, err := s.db.Exec(
		`DELETE FROM perf_host_samples WHERE id NOT IN (SELECT id FROM perf_host_samples ORDER BY id DESC LIMIT ?)`,
		PerfWindowSize*4096)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(
		`DELETE FROM perf_request_log WHERE id NOT IN (SELECT id FROM perf_request_log ORDER BY id DESC LIMIT ?)`,
		requestLogSize)
	return err
}

func (s *PerfStore) BackfillLegacyEscrowSamples(sourceEscrow, sourcePath string, participantKeys []string) ([]RequestSample, error) {
	if s == nil || s.db == nil || strings.TrimSpace(sourceEscrow) == "" || strings.TrimSpace(sourcePath) == "" {
		return nil, nil
	}
	if filepath.Clean(sourcePath) == filepath.Clean(s.path) {
		return nil, nil
	}
	sourceDB, err := sql.Open("sqlite", sourcePath)
	if err != nil {
		return nil, fmt.Errorf("open legacy perf source: %w", err)
	}
	defer sourceDB.Close()
	sourceDB.SetMaxOpenConns(1)

	rows, err := sourceDB.Query(`SELECT id, host_idx, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens FROM perf_host_samples ORDER BY id ASC`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("read legacy perf samples: %w", err)
	}
	defer rows.Close()

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO perf_host_samples
		(host_idx, participant_key, responsive, send_time, receipt_time, first_token, total_time_ms, input_tokens, source_escrow, source_sample_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	var inserted []RequestSample
	for rows.Next() {
		var (
			id          int64
			hostIdx     int
			responsive  int
			sendStr     string
			receiptStr  string
			firstStr    string
			totalMs     float64
			inputTokens uint64
		)
		if err := rows.Scan(&id, &hostIdx, &responsive, &sendStr, &receiptStr, &firstStr, &totalMs, &inputTokens); err != nil {
			return nil, err
		}
		if hostIdx < 0 || hostIdx >= len(participantKeys) || strings.TrimSpace(participantKeys[hostIdx]) == "" {
			continue
		}
		participantKey := strings.TrimSpace(participantKeys[hostIdx])
		res, err := stmt.Exec(hostIdx, participantKey, responsive, sendStr, receiptStr, firstStr, totalMs, inputTokens, sourceEscrow, id)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			inserted = append(inserted, RequestSample{
				HostIdx:        hostIdx,
				ParticipantKey: participantKey,
				Responsive:     responsive != 0,
				SendTime:       strToTime(sendStr),
				ReceiptTime:    strToTime(receiptStr),
				FirstToken:     strToTime(firstStr),
				TotalTime:      time.Duration(totalMs) * time.Millisecond,
				InputTokens:    inputTokens,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return inserted, nil
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
