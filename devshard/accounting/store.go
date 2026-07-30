package accounting

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"devshard/types"

	_ "modernc.org/sqlite"
)

type Store struct {
	db              *sql.DB
	retentionEpochs uint64
}

func OpenStore(path string, retentionEpochs uint64) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("accounting database path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open accounting database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(accountingSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create accounting schema: %w", err)
	}
	return &Store{db: db, retentionEpochs: retentionEpochs}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Snapshot(ctx context.Context, book *Book) (err error) {
	if s == nil || s.db == nil {
		return errors.New("accounting store is nil")
	}
	if book == nil {
		return errors.New("accounting book is nil")
	}
	defer func() {
		if err != nil {
			book.RecordWriterError()
		}
	}()

	snapshot := retainedSnapshot(book.Snapshot(), s.retentionEpochs)
	if err := validateSnapshotIntegers(snapshot); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return fmt.Errorf("begin accounting snapshot: %w", err)
	}
	defer tx.Rollback()

	for _, table := range []string{
		"accounting_pending_timeouts",
		"accounting_challenges",
		"accounting_counters",
		"accounting_host_stats",
		"accounting_latest_nonce",
		"accounting_slots",
		"accounting_escrows",
		"accounting_meta",
	} {
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounting_meta (key, value) VALUES
		 ('schema_version', ?), ('updated_at', ?), ('recording_errors', ?), ('writer_errors', ?)`,
		strconv.Itoa(SchemaVersion),
		snapshot.UpdatedAt.Format(time.RFC3339Nano),
		strconv.FormatUint(snapshot.EventErrors, 10),
		strconv.FormatUint(snapshot.WriterErrors, 10),
	); err != nil {
		return fmt.Errorf("write accounting metadata: %w", err)
	}

	for _, escrow := range snapshot.Escrows {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounting_escrows
			 (escrow_id, creation_epoch, model, phase) VALUES (?, ?, ?, ?)`,
			escrow.Metadata.EscrowID,
			escrow.Metadata.CreationEpoch,
			escrow.Metadata.Model,
			escrow.Metadata.Phase,
		); err != nil {
			return fmt.Errorf("write escrow %q: %w", escrow.Metadata.EscrowID, err)
		}
		for _, slot := range escrow.Metadata.Slots {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounting_slots
				 (escrow_id, slot_id, participant, invalid_transitions) VALUES (?, ?, ?, ?)`,
				escrow.Metadata.EscrowID,
				slot.SlotID,
				slot.ValidatorAddress,
				escrow.InvalidTransitions[slot.SlotID],
			); err != nil {
				return fmt.Errorf("write escrow %q slot %d: %w", escrow.Metadata.EscrowID, slot.SlotID, err)
			}
		}
		for nonce, slotID := range escrow.Challenges {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounting_challenges (escrow_id, nonce, slot_id) VALUES (?, ?, ?)`,
				escrow.Metadata.EscrowID,
				nonce,
				slotID,
			); err != nil {
				return fmt.Errorf("write escrow %q challenge %d: %w", escrow.Metadata.EscrowID, nonce, err)
			}
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounting_latest_nonce (escrow_id, latest_nonce) VALUES (?, ?)`,
			escrow.Metadata.EscrowID,
			escrow.LatestNonce,
		); err != nil {
			return fmt.Errorf("write escrow %q latest nonce: %w", escrow.Metadata.EscrowID, err)
		}
		for slotID, stats := range escrow.HostStats {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounting_host_stats
				 (escrow_id, slot_id, missed, invalid_count, cost,
				  required_validations, completed_validations)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				escrow.Metadata.EscrowID,
				slotID,
				stats.Missed,
				stats.Invalid,
				stats.Cost,
				stats.RequiredValidations,
				stats.CompletedValidations,
			); err != nil {
				return fmt.Errorf("write escrow %q host stats: %w", escrow.Metadata.EscrowID, err)
			}
		}
		for key, count := range escrow.Counters {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounting_counters (
				 escrow_id, slot_id, disposition, dispatch_phase,
				 timeout_evaluation_phase, quarantine_mode, no_send_reason,
				 failure_origin, detail_reason, timeout_kind, timeout_outcome,
				 timeout_reason, count
				 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				escrow.Metadata.EscrowID,
				key.SlotID,
				key.Disposition,
				key.DispatchPhase,
				key.TimeoutEvaluationPhase,
				key.QuarantineMode,
				key.NoSendReason,
				key.FailureOrigin,
				key.DetailReason,
				key.TimeoutKind,
				key.TimeoutOutcome,
				key.TimeoutReason,
				count,
			); err != nil {
				return fmt.Errorf("write escrow %q counter: %w", escrow.Metadata.EscrowID, err)
			}
		}
		for _, pending := range escrow.Pending {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO accounting_pending_timeouts (
				 escrow_id, nonce, slot_id, receipt, sent, usage, dispatch_phase,
				 quarantine_mode, failure_origin, detail_reason,
				 timeout_evaluation_phase, timeout_kind, timeout_outcome,
				 timeout_reason, counted_key
				 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				escrow.Metadata.EscrowID,
				pending.Nonce,
				pending.SlotID,
				boolToSQLite(pending.Receipt),
				boolToSQLite(pending.Sent),
				pending.Usage,
				pending.DispatchPhase,
				pending.Quarantine,
				pending.FailureOrigin,
				pending.DetailReason,
				pending.TimeoutPhase,
				pending.TimeoutKind,
				pending.TimeoutOutcome,
				pending.TimeoutReason,
				counterSortKey(pending.Counted),
			); err != nil {
				return fmt.Errorf("write escrow %q pending timeout: %w", escrow.Metadata.EscrowID, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit accounting snapshot: %w", err)
	}
	// Only discard the in-memory copy after the retained snapshot is durable.
	// A failed snapshot must leave both memory and the previous database intact.
	book.Prune(s.retentionEpochs)
	return nil
}

func retainedSnapshot(snapshot Snapshot, retentionEpochs uint64) Snapshot {
	if retentionEpochs == 0 || len(snapshot.Escrows) == 0 {
		return snapshot
	}
	var maxEpoch uint64
	complete := make(map[uint64]bool)
	for _, escrow := range snapshot.Escrows {
		epoch := escrow.Metadata.CreationEpoch
		if epoch > maxEpoch {
			maxEpoch = epoch
		}
		if _, exists := complete[epoch]; !exists {
			complete[epoch] = true
		}
		if escrow.Metadata.Phase != EscrowSettled {
			complete[epoch] = false
		}
	}
	var cutoff uint64
	if maxEpoch+1 > retentionEpochs {
		cutoff = maxEpoch + 1 - retentionEpochs
	}
	kept := snapshot.Escrows[:0]
	for _, escrow := range snapshot.Escrows {
		if escrow.Metadata.CreationEpoch >= cutoff || !complete[escrow.Metadata.CreationEpoch] {
			kept = append(kept, escrow)
		}
	}
	snapshot.Escrows = kept
	return snapshot
}

func (s *Store) Load(ctx context.Context) (*Book, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("accounting store is nil")
	}
	snapshot, err := s.loadSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return NewBookFromSnapshot(snapshot)
}

func (s *Store) loadSnapshot(ctx context.Context) (Snapshot, error) {
	snapshot := Snapshot{SchemaVersion: SchemaVersion}
	metaRows, err := s.db.QueryContext(ctx, `SELECT key, value FROM accounting_meta`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting metadata: %w", err)
	}
	for metaRows.Next() {
		var key, value string
		if err := metaRows.Scan(&key, &value); err != nil {
			metaRows.Close()
			return Snapshot{}, err
		}
		switch key {
		case "schema_version":
			version, err := strconv.Atoi(value)
			if err != nil {
				metaRows.Close()
				return Snapshot{}, fmt.Errorf("parse accounting schema version: %w", err)
			}
			snapshot.SchemaVersion = version
		case "updated_at":
			snapshot.UpdatedAt, _ = time.Parse(time.RFC3339Nano, value)
		case "recording_errors":
			snapshot.EventErrors, _ = strconv.ParseUint(value, 10, 64)
		case "writer_errors":
			snapshot.WriterErrors, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	if err := metaRows.Close(); err != nil {
		return Snapshot{}, err
	}
	if snapshot.SchemaVersion != SchemaVersion {
		return Snapshot{}, fmt.Errorf("unsupported accounting schema version %d", snapshot.SchemaVersion)
	}

	escrows := make(map[string]int)
	rows, err := s.db.QueryContext(ctx,
		`SELECT escrow_id, creation_epoch, model, phase
		 FROM accounting_escrows ORDER BY escrow_id`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting escrows: %w", err)
	}
	for rows.Next() {
		var item EscrowSnapshot
		if err := rows.Scan(
			&item.Metadata.EscrowID,
			&item.Metadata.CreationEpoch,
			&item.Metadata.Model,
			&item.Metadata.Phase,
		); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		item.HostStats = make(map[uint32]types.HostStats)
		item.Counters = make(map[CounterKey]uint64)
		item.Challenges = make(map[uint64]uint32)
		item.InvalidTransitions = make(map[uint32]uint64)
		snapshot.Escrows = append(snapshot.Escrows, item)
		escrows[item.Metadata.EscrowID] = len(snapshot.Escrows) - 1
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, slot_id, participant, invalid_transitions
		 FROM accounting_slots ORDER BY escrow_id, slot_id`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting slots: %w", err)
	}
	for rows.Next() {
		var escrowID, participant string
		var slotID uint32
		var invalidTransitions uint64
		if err := rows.Scan(&escrowID, &slotID, &participant, &invalidTransitions); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		index, ok := escrows[escrowID]
		if !ok {
			rows.Close()
			return Snapshot{}, fmt.Errorf("slot references unknown escrow %q", escrowID)
		}
		snapshot.Escrows[index].Metadata.Slots = append(snapshot.Escrows[index].Metadata.Slots, types.SlotAssignment{
			SlotID:           slotID,
			ValidatorAddress: participant,
		})
		if invalidTransitions > 0 {
			snapshot.Escrows[index].InvalidTransitions[slotID] = invalidTransitions
		}
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, nonce, slot_id FROM accounting_challenges`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting challenges: %w", err)
	}
	for rows.Next() {
		var escrowID string
		var nonce uint64
		var slotID uint32
		if err := rows.Scan(&escrowID, &nonce, &slotID); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		index, ok := escrows[escrowID]
		if !ok {
			rows.Close()
			return Snapshot{}, fmt.Errorf("challenge references unknown escrow %q", escrowID)
		}
		snapshot.Escrows[index].Challenges[nonce] = slotID
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, latest_nonce FROM accounting_latest_nonce`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting latest nonces: %w", err)
	}
	for rows.Next() {
		var escrowID string
		var latest uint64
		if err := rows.Scan(&escrowID, &latest); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		if index, ok := escrows[escrowID]; ok {
			snapshot.Escrows[index].LatestNonce = latest
		}
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, slot_id, missed, invalid_count, cost,
		        required_validations, completed_validations
		 FROM accounting_host_stats`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting host stats: %w", err)
	}
	for rows.Next() {
		var escrowID string
		var slotID uint32
		var stats types.HostStats
		if err := rows.Scan(
			&escrowID,
			&slotID,
			&stats.Missed,
			&stats.Invalid,
			&stats.Cost,
			&stats.RequiredValidations,
			&stats.CompletedValidations,
		); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		if index, ok := escrows[escrowID]; ok {
			snapshot.Escrows[index].HostStats[slotID] = stats
		}
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}

	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, slot_id, disposition, dispatch_phase,
		        timeout_evaluation_phase, quarantine_mode, no_send_reason,
		        failure_origin, detail_reason, timeout_kind, timeout_outcome,
		        timeout_reason, count
		 FROM accounting_counters`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting counters: %w", err)
	}
	for rows.Next() {
		var escrowID string
		var key CounterKey
		var count uint64
		if err := rows.Scan(
			&escrowID,
			&key.SlotID,
			&key.Disposition,
			&key.DispatchPhase,
			&key.TimeoutEvaluationPhase,
			&key.QuarantineMode,
			&key.NoSendReason,
			&key.FailureOrigin,
			&key.DetailReason,
			&key.TimeoutKind,
			&key.TimeoutOutcome,
			&key.TimeoutReason,
			&count,
		); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		if index, ok := escrows[escrowID]; ok {
			snapshot.Escrows[index].Counters[key] = count
		}
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	rows, err = s.db.QueryContext(ctx,
		`SELECT escrow_id, nonce, slot_id, receipt, sent, usage, dispatch_phase,
		        quarantine_mode, failure_origin, detail_reason,
		        timeout_evaluation_phase, timeout_kind, timeout_outcome,
		        timeout_reason, counted_key
		 FROM accounting_pending_timeouts ORDER BY escrow_id, nonce`)
	if err != nil {
		return Snapshot{}, fmt.Errorf("read accounting pending timeouts: %w", err)
	}
	for rows.Next() {
		var (
			escrowID         string
			pending          PendingNonceSnapshot
			receipt, sent    int
			countedKeyString string
		)
		if err := rows.Scan(
			&escrowID,
			&pending.Nonce,
			&pending.SlotID,
			&receipt,
			&sent,
			&pending.Usage,
			&pending.DispatchPhase,
			&pending.Quarantine,
			&pending.FailureOrigin,
			&pending.DetailReason,
			&pending.TimeoutPhase,
			&pending.TimeoutKind,
			&pending.TimeoutOutcome,
			&pending.TimeoutReason,
			&countedKeyString,
		); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		index, ok := escrows[escrowID]
		if !ok {
			rows.Close()
			return Snapshot{}, fmt.Errorf("pending timeout references unknown escrow %q", escrowID)
		}
		pending.Receipt = receipt != 0
		pending.Sent = sent != 0
		counted, ok := findCounterKey(snapshot.Escrows[index].Counters, pending.SlotID, countedKeyString)
		if !ok {
			rows.Close()
			return Snapshot{}, fmt.Errorf("pending timeout %s/%d references unknown counter", escrowID, pending.Nonce)
		}
		pending.Counted = counted
		snapshot.Escrows[index].Pending = append(snapshot.Escrows[index].Pending, pending)
	}
	if err := rows.Close(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshotIntegers(snapshot Snapshot) error {
	for _, escrow := range snapshot.Escrows {
		if escrow.Metadata.CreationEpoch > math.MaxInt64 || escrow.LatestNonce > math.MaxInt64 {
			return fmt.Errorf("escrow %q has an integer too large for SQLite", escrow.Metadata.EscrowID)
		}
		for _, stats := range escrow.HostStats {
			if stats.Cost > math.MaxInt64 {
				return fmt.Errorf("escrow %q host cost is too large for SQLite", escrow.Metadata.EscrowID)
			}
		}
		for _, count := range escrow.Counters {
			if count > math.MaxInt64 {
				return fmt.Errorf("escrow %q counter is too large for SQLite", escrow.Metadata.EscrowID)
			}
		}
		for nonce := range escrow.Challenges {
			if nonce > math.MaxInt64 {
				return fmt.Errorf("escrow %q challenged nonce is too large for SQLite", escrow.Metadata.EscrowID)
			}
		}
		for _, count := range escrow.InvalidTransitions {
			if count > math.MaxInt64 {
				return fmt.Errorf("escrow %q invalid transition count is too large for SQLite", escrow.Metadata.EscrowID)
			}
		}
	}
	return nil
}

const accountingSchema = `
CREATE TABLE IF NOT EXISTS accounting_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_escrows (
	escrow_id      TEXT PRIMARY KEY,
	creation_epoch INTEGER NOT NULL,
	model          TEXT NOT NULL,
	phase          TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_slots (
	escrow_id           TEXT NOT NULL REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	slot_id             INTEGER NOT NULL,
	participant         TEXT NOT NULL,
	invalid_transitions INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_challenges (
	escrow_id TEXT NOT NULL REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	nonce     INTEGER NOT NULL,
	slot_id   INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
CREATE TABLE IF NOT EXISTS accounting_latest_nonce (
	escrow_id    TEXT PRIMARY KEY REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	latest_nonce INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_host_stats (
	escrow_id             TEXT NOT NULL REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	slot_id               INTEGER NOT NULL,
	missed                INTEGER NOT NULL,
	invalid_count         INTEGER NOT NULL,
	cost                  INTEGER NOT NULL,
	required_validations  INTEGER NOT NULL,
	completed_validations INTEGER NOT NULL,
	PRIMARY KEY (escrow_id, slot_id)
);
CREATE TABLE IF NOT EXISTS accounting_counters (
	escrow_id               TEXT NOT NULL REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	slot_id                 INTEGER NOT NULL,
	disposition             TEXT NOT NULL,
	dispatch_phase          TEXT NOT NULL,
	timeout_evaluation_phase TEXT NOT NULL,
	quarantine_mode         TEXT NOT NULL,
	no_send_reason          TEXT NOT NULL,
	failure_origin          TEXT NOT NULL,
	detail_reason           TEXT NOT NULL,
	timeout_kind            TEXT NOT NULL,
	timeout_outcome         TEXT NOT NULL,
	timeout_reason          TEXT NOT NULL,
	count                   INTEGER NOT NULL,
	PRIMARY KEY (
		escrow_id, slot_id, disposition, dispatch_phase,
		timeout_evaluation_phase, quarantine_mode, no_send_reason,
		failure_origin, detail_reason, timeout_kind, timeout_outcome,
		timeout_reason
	)
);
CREATE TABLE IF NOT EXISTS accounting_pending_timeouts (
	escrow_id                TEXT NOT NULL REFERENCES accounting_escrows(escrow_id) ON DELETE CASCADE,
	nonce                    INTEGER NOT NULL,
	slot_id                  INTEGER NOT NULL,
	receipt                  INTEGER NOT NULL,
	sent                     INTEGER NOT NULL,
	usage                    TEXT NOT NULL,
	dispatch_phase           TEXT NOT NULL,
	quarantine_mode          TEXT NOT NULL,
	failure_origin           TEXT NOT NULL,
	detail_reason            TEXT NOT NULL,
	timeout_evaluation_phase TEXT NOT NULL,
	timeout_kind             TEXT NOT NULL,
	timeout_outcome          TEXT NOT NULL,
	timeout_reason           TEXT NOT NULL,
	counted_key              TEXT NOT NULL,
	PRIMARY KEY (escrow_id, nonce)
);
CREATE INDEX IF NOT EXISTS accounting_escrows_epoch_model_idx
	ON accounting_escrows(creation_epoch, model);
`

func boolToSQLite(value bool) int {
	if value {
		return 1
	}
	return 0
}

func findCounterKey(counters map[CounterKey]uint64, slotID uint32, encoded string) (CounterKey, bool) {
	for key := range counters {
		if key.SlotID == slotID && counterSortKey(key) == encoded {
			return key, true
		}
	}
	return CounterKey{}, false
}
