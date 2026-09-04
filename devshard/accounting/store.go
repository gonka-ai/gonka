package accounting

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"devshard/types"
)

type Store struct {
	db        *sql.DB
	path      string
	retention uint64
}

type escrowBlob struct {
	Meta            EscrowMetadata             `json:"meta"`
	Latest          uint64                     `json:"latest"`
	HostStats       map[uint32]types.HostStats `json:"host_stats"`
	Counters        []counterBlob              `json:"counters"`
	ChallengeBySlot map[uint32]uint64          `json:"challenge_by_slot"`
	ValidatedBySlot map[uint32]uint64          `json:"validated_by_slot,omitempty"`
	TimedOutBySlot  map[uint32]uint64          `json:"timed_out_by_slot,omitempty"`
	InvalidBySlot   map[uint32]uint64          `json:"invalid_by_slot"`
	InvalidNonces   []uint64                   `json:"invalid_nonces,omitempty"`
	Events          []ProtocolEvent            `json:"events,omitempty"`
}

type counterBlob struct {
	Key   CounterKey `json:"key"`
	Count uint64     `json:"count"`
}

func OpenStore(path string, retention uint64) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("accounting database path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db, path: path, retention: retention}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Load(ctx context.Context, t *Tracker) error {
	if s == nil || s.db == nil || t == nil {
		return nil
	}
	metaRows, err := s.db.QueryContext(ctx, `SELECT key, value FROM accounting_meta`)
	if err != nil {
		return err
	}
	for metaRows.Next() {
		var key, value string
		if err := metaRows.Scan(&key, &value); err != nil {
			metaRows.Close()
			return err
		}
		switch key {
		case "updated_at":
			if updated, err := time.Parse(time.RFC3339Nano, value); err == nil {
				t.updated = updated
			}
		case "writer_errors":
			if count, err := strconv.ParseUint(value, 10, 64); err == nil {
				t.wrCount = count
			}
		}
	}
	if err := metaRows.Err(); err != nil {
		metaRows.Close()
		return err
	}
	if err := metaRows.Close(); err != nil {
		return err
	}

	rows, err := s.db.QueryContext(ctx, `SELECT payload FROM accounting_escrows`)
	if err != nil {
		return err
	}
	defer rows.Close()

	t.mu.Lock()
	defer t.mu.Unlock()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var blob escrowBlob
		if err := json.Unmarshal(raw, &blob); err != nil {
			return err
		}
		meta, err := normalizeMetadata(blob.Meta)
		if err != nil {
			return err
		}
		escrow := &escrowState{
			Meta:            meta,
			Latest:          blob.Latest,
			HostStats:       make(map[uint32]types.HostStats, len(blob.HostStats)),
			Counters:        make(map[CounterKey]uint64, len(blob.Counters)),
			OpenChallenge:   make(map[uint64]uint32),
			ChallengeBySlot: blob.ChallengeBySlot,
			ValidatedBySlot: blob.ValidatedBySlot,
			TimedOutBySlot:  blob.TimedOutBySlot,
			InvalidBySlot:   blob.InvalidBySlot,
			InvalidNonce:    make(map[uint64]struct{}, len(blob.InvalidNonces)),
			Live:            make(map[uint64]*nonceState),
			LiveRequests:    make(map[string]struct{}),
			Events:          blob.Events,
		}
		for _, nonce := range blob.InvalidNonces {
			escrow.InvalidNonce[nonce] = struct{}{}
		}
		if escrow.ChallengeBySlot == nil {
			escrow.ChallengeBySlot = make(map[uint32]uint64)
		}
		if escrow.InvalidBySlot == nil {
			escrow.InvalidBySlot = make(map[uint32]uint64)
		}
		if escrow.ValidatedBySlot == nil {
			escrow.ValidatedBySlot = make(map[uint32]uint64)
		}
		if escrow.TimedOutBySlot == nil {
			escrow.TimedOutBySlot = make(map[uint32]uint64)
		}
		maps.Copy(escrow.HostStats, blob.HostStats)
		for _, counter := range blob.Counters {
			if counter.Count > 0 {
				escrow.Counters[counter.Key] += counter.Count
			}
		}
		t.escrows[meta.EscrowID] = escrow
	}
	return rows.Err()
}

func (s *Store) Save(ctx context.Context, t *Tracker) error {
	if s == nil || s.db == nil || t == nil {
		return nil
	}
	snapshot := t.snapshot(s.retention)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounting_escrows`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM accounting_meta`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO accounting_meta(key, value) VALUES
		 ('schema_version', ?), ('updated_at', ?), ('writer_errors', ?)`,
		SchemaVersion, snapshot.UpdatedAt.Format(time.RFC3339Nano), snapshot.WriterErrors,
	); err != nil {
		return err
	}
	for _, blob := range snapshot.Escrows {
		raw, err := json.Marshal(blob)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO accounting_escrows(escrow_id, creation_epoch, model, payload)
			 VALUES (?, ?, ?, ?)`,
			blob.Meta.EscrowID, blob.Meta.CreationEpoch, blob.Meta.Model, raw,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type storeSnapshot struct {
	UpdatedAt    time.Time
	WriterErrors uint64
	Escrows      []escrowBlob
}

func (t *Tracker) snapshot(retention uint64) storeSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.nowUTC()
	for _, escrow := range t.escrows {
		escrow.refreshDerived(now)
	}
	t.updated = now
	t.pruneLocked(retention)
	out := storeSnapshot{UpdatedAt: t.updated, WriterErrors: t.wrCount}
	for _, escrow := range t.escrows {
		blob := escrowBlob{
			Meta:            escrow.Meta,
			Latest:          escrow.Latest,
			HostStats:       make(map[uint32]types.HostStats, len(escrow.HostStats)),
			ChallengeBySlot: copyUint32Map(escrow.ChallengeBySlot),
			ValidatedBySlot: copyUint32Map(escrow.ValidatedBySlot),
			TimedOutBySlot:  copyUint32Map(escrow.TimedOutBySlot),
			InvalidBySlot:   copyUint32Map(escrow.InvalidBySlot),
			InvalidNonces:   sortedNonces(escrow.InvalidNonce),
			Events:          append([]ProtocolEvent(nil), escrow.Events...),
		}
		maps.Copy(blob.HostStats, escrow.HostStats)
		for _, key := range sortedCounterKeys(escrow.Counters) {
			blob.Counters = append(blob.Counters, counterBlob{Key: key, Count: escrow.Counters[key]})
		}
		out.Escrows = append(out.Escrows, blob)
	}
	return out
}

func (t *Tracker) pruneLocked(retention uint64) {
	if retention == 0 {
		return
	}
	var maxEpoch uint64
	complete := make(map[uint64]bool)
	for _, escrow := range t.escrows {
		epoch := escrow.Meta.CreationEpoch
		if epoch > maxEpoch {
			maxEpoch = epoch
		}
		if _, ok := complete[epoch]; !ok {
			complete[epoch] = true
		}
		if escrow.Meta.Phase != EscrowSettled {
			complete[epoch] = false
		}
	}
	var cutoff uint64
	if maxEpoch+1 > retention {
		cutoff = maxEpoch + 1 - retention
	}
	for id, escrow := range t.escrows {
		if escrow.Meta.CreationEpoch < cutoff && complete[escrow.Meta.CreationEpoch] {
			delete(t.escrows, id)
		}
	}
}

func copyUint32Map(in map[uint32]uint64) map[uint32]uint64 {
	out := make(map[uint32]uint64, len(in))
	maps.Copy(out, in)
	return out
}

func sortedNonces(in map[uint64]struct{}) []uint64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]uint64, 0, len(in))
	for nonce := range in {
		out = append(out, nonce)
	}
	slices.Sort(out)
	return out
}

const schema = `
CREATE TABLE IF NOT EXISTS accounting_meta (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS accounting_escrows (
	escrow_id TEXT PRIMARY KEY,
	creation_epoch INTEGER NOT NULL,
	model TEXT NOT NULL,
	payload BLOB NOT NULL
);
CREATE INDEX IF NOT EXISTS accounting_escrows_epoch_model_idx
	ON accounting_escrows(creation_epoch, model);
`
