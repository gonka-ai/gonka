package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"devshard/types"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Postgres implements Storage on top of PostgreSQL declarative partitioning.
//
// Nine parent tables (sessions, diffs, signatures, snapshots, sealed inferences,
// validation obs tables, validation leases) use PARTITION BY RANGE (epoch_id).
// One child partition per epoch is created lazily on first write. PruneEpoch is
// a DROP TABLE per child partition, so it is O(1) and never touches other epochs.
//
// Layout mirrors the per-epoch SQLite backend so that callers behave identically
// against both. A small unpartitioned escrowID -> epochID index enforces the
// mainnet-pinned mapping, and the in-memory copy lets escrow-keyed methods
// route to the right partition without scanning.
type Postgres struct {
	pool *pgxpool.Pool

	mu          sync.RWMutex
	knownEpochs map[uint64]struct{}
	escrowIdx   map[string]uint64
}

const (
	// postgresConnectTimeout bounds establishing a new connection.
	postgresConnectTimeout = 5 * time.Second
	// postgresStatementTimeout aborts any single query server-side. It is the
	// primary guard against a stalled backend hanging a caller indefinitely.
	postgresStatementTimeout = 5 * time.Second
	// postgresLockTimeout bounds waits on row/table locks server-side.
	postgresLockTimeout = 3 * time.Second
	// postgresOpTimeout bounds each storage operation Go-side. Unlike
	// statement_timeout it also covers the time spent acquiring a pooled
	// connection (pool exhaustion), which the server-side timeout cannot.
	postgresOpTimeout = 8 * time.Second
	// postgresLivePresenceTimeout bounds the HasAnySessionsLive query. It is
	// deliberately short: the check runs under HybridStorage.markerMu right
	// after a failed (often timed-out) create, so it must not extend the lock
	// hold time by another full op timeout during an outage.
	postgresLivePresenceTimeout = 2 * time.Second
)

const (
	pgSessionsParent               = "devshard_sessions"
	pgDiffsParent                  = "devshard_diffs"
	pgSignaturesParent             = "devshard_signatures"
	pgSnapshotsParent              = "devshard_snapshots"
	pgInferencesParent             = "devshard_sealed_inferences"
	pgValidationObsParent          = "devshard_slot_validation_obs"
	pgInferenceValidationObsParent = "devshard_inference_validation_obs"
	pgSealedValidationObsParent    = "devshard_sealed_validation_obs"
	pgValidationLeasesParent       = "devshard_validation_leases"
	pgSessionIndex                 = "devshard_session_index"
)

// postgresPartitionedParents lists every PARTITION BY RANGE (epoch_id) parent
// table; ensurePartition creates one child partition per epoch for each.
var postgresPartitionedParents = []string{
	pgSessionsParent,
	pgDiffsParent,
	pgSignaturesParent,
	pgSnapshotsParent,
	pgInferencesParent,
	pgValidationObsParent,
	pgInferenceValidationObsParent,
	pgSealedValidationObsParent,
	pgValidationLeasesParent,
}

func pgSessionsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgSessionsParent, epochID)
}
func pgDiffsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgDiffsParent, epochID)
}
func pgSignaturesPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgSignaturesParent, epochID)
}
func pgSnapshotsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgSnapshotsParent, epochID)
}
func pgInferencesPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgInferencesParent, epochID)
}
func pgValidationObsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgValidationObsParent, epochID)
}
func pgInferenceValidationObsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgInferenceValidationObsParent, epochID)
}
func pgSealedValidationObsPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgSealedValidationObsParent, epochID)
}
func pgValidationLeasesPartition(epochID uint64) string {
	return fmt.Sprintf("%s_epoch_%d", pgValidationLeasesParent, epochID)
}

// NewPostgres opens a Postgres-backed Storage using the standard libpq env
// vars (PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD). Schema is created
// idempotently and the escrow index is rebuilt by scanning devshard_sessions.
func NewPostgres(ctx context.Context) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig("") // reads libpq env vars
	if err != nil {
		return nil, fmt.Errorf("parse postgres config: %w", err)
	}
	cfg.ConnConfig.ConnectTimeout = postgresConnectTimeout
	if cfg.ConnConfig.RuntimeParams == nil {
		cfg.ConnConfig.RuntimeParams = make(map[string]string)
	}
	// Server-side per-query bounds applied to every pooled connection.
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = strconv.FormatInt(postgresStatementTimeout.Milliseconds(), 10)
	cfg.ConnConfig.RuntimeParams["lock_timeout"] = strconv.FormatInt(postgresLockTimeout.Milliseconds(), 10)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if err := MigratePostgres(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}

	s := &Postgres{
		pool:        pool,
		knownEpochs: make(map[uint64]struct{}),
		escrowIdx:   make(map[string]uint64),
	}
	if err := s.indexExisting(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("index existing sessions: %w", err)
	}
	return s, nil
}

func (s *Postgres) indexExisting(ctx context.Context) error {
	sessionsOnDisk := make(map[string]uint64)
	rows, err := s.pool.Query(ctx, `SELECT epoch_id, escrow_id FROM devshard_sessions`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var epochID uint64
		var escrowID string
		if err := rows.Scan(&epochID, &escrowID); err != nil {
			rows.Close()
			return err
		}
		if existingEpoch, ok := sessionsOnDisk[escrowID]; ok && existingEpoch != epochID {
			rows.Close()
			return fmt.Errorf("%w: escrow %s exists in epochs %d and %d",
				ErrSessionEpochConflict, escrowID, existingEpoch, epochID)
		}
		sessionsOnDisk[escrowID] = epochID
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	indexRows, err := s.pool.Query(ctx, `SELECT escrow_id, epoch_id FROM devshard_session_index`)
	if err != nil {
		return err
	}
	for indexRows.Next() {
		var escrowID string
		var epochID uint64
		if err := indexRows.Scan(&escrowID, &epochID); err != nil {
			indexRows.Close()
			return err
		}
		if diskEpoch, ok := sessionsOnDisk[escrowID]; ok && diskEpoch == epochID {
			continue
		}
		if _, err := s.pool.Exec(ctx,
			`DELETE FROM devshard_session_index WHERE escrow_id = $1 AND epoch_id = $2`,
			escrowID, epochID,
		); err != nil {
			indexRows.Close()
			return fmt.Errorf("remove stale session index for %s: %w", escrowID, err)
		}
	}
	indexRows.Close()
	if err := indexRows.Err(); err != nil {
		return err
	}

	for escrowID, epochID := range sessionsOnDisk {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO devshard_session_index (escrow_id, epoch_id)
			 VALUES ($1, $2)
			 ON CONFLICT (escrow_id) DO NOTHING`,
			escrowID, epochID,
		); err != nil {
			return fmt.Errorf("repair session index for %s: %w", escrowID, err)
		}
		s.escrowIdx[escrowID] = epochID
		s.knownEpochs[epochID] = struct{}{}
	}
	return nil
}

// Close releases the pool. Subsequent calls return immediately.
func (s *Postgres) Close() error {
	s.pool.Close()
	return nil
}

// ensurePartition creates per-epoch partitions for all partitioned parents
// on first touch. This is the only site that may issue CREATE TABLE ... PARTITION OF
// for devshard Postgres; write paths must call ensurePartition instead of inline DDL.
// The check + create is racy across multiple writers, but PG returns 42P07 (table
// already exists) which we swallow.
func (s *Postgres) ensurePartition(ctx context.Context, epochID uint64) error {
	s.mu.RLock()
	_, ok := s.knownEpochs[epochID]
	s.mu.RUnlock()
	if ok {
		return nil
	}

	create := func(parent, partition string) error {
		q := fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%d) TO (%d)`,
			partition, parent, epochID, epochID+1,
		)
		_, err := s.pool.Exec(ctx, q)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "42P07" {
				return nil
			}
			return fmt.Errorf("create partition %s: %w", partition, err)
		}
		return nil
	}

	if err := create(pgSessionsParent, pgSessionsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgDiffsParent, pgDiffsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgSignaturesParent, pgSignaturesPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgSnapshotsParent, pgSnapshotsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgInferencesParent, pgInferencesPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgValidationObsParent, pgValidationObsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgInferenceValidationObsParent, pgInferenceValidationObsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgSealedValidationObsParent, pgSealedValidationObsPartition(epochID)); err != nil {
		return err
	}
	if err := create(pgValidationLeasesParent, pgValidationLeasesPartition(epochID)); err != nil {
		return err
	}

	s.mu.Lock()
	s.knownEpochs[epochID] = struct{}{}
	s.mu.Unlock()
	return nil
}

func (s *Postgres) lookupEpoch(escrowID string) (uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	epochID, ok := s.escrowIdx[escrowID]
	if !ok {
		return 0, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	return epochID, nil
}

// HasEscrow reports whether escrowID is present in the in-memory routing index
// (rebuilt at boot from devshard_session_index). It lets the hybrid router
// resolve which backend owns an escrow.
func (s *Postgres) HasEscrow(escrowID string) bool {
	_, err := s.lookupEpoch(escrowID)
	return err == nil
}

// HasAnySessions reports whether any escrow is still held in Postgres. The
// in-memory index is kept in sync with devshard_session_index by CreateSession
// and by prune, so this is an accurate emptiness check without a round trip.
// The hybrid router uses it to clear the .pg-bound marker once PG is drained.
func (s *Postgres) HasAnySessions() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.escrowIdx) > 0
}

// EscrowIDs returns a snapshot of escrows in the in-memory routing index.
func (s *Postgres) EscrowIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.escrowIdx))
	for id := range s.escrowIdx {
		ids = append(ids, id)
	}
	return ids
}

// HasAnySessionsLive checks devshard_session_index in the database itself.
// The hybrid router uses it before clearing .pg-bound after a failed create:
// a timed-out CreateSession may have committed server-side without updating
// the in-memory index, so emptiness must be proven against the DB, not RAM.
func (s *Postgres) HasAnySessionsLive() (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), postgresLivePresenceTimeout)
	defer cancel()
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM devshard_session_index)`).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

// opCtx returns a context bounding a single storage operation. It caps both the
// pooled-connection acquire wait and total query time Go-side; statement_timeout
// and lock_timeout provide the matching server-side bounds. Callers must defer
// the returned cancel.
func (s *Postgres) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), postgresOpTimeout)
}

func (s *Postgres) CreateSession(params CreateSessionParams) error {
	params.Config = types.NormalizeSessionConfig(params.Config, len(params.Group))
	configJSON, err := json.Marshal(params.Config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	groupJSON, err := json.Marshal(params.Group)
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}
	requestedVersion, err := requireSessionVersion(params.Version)
	if err != nil {
		return err
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, params.EpochID); err != nil {
		return err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var indexedEpoch uint64
	indexErr := tx.QueryRow(ctx,
		`SELECT epoch_id FROM devshard_session_index WHERE escrow_id = $1`,
		params.EscrowID,
	).Scan(&indexedEpoch)
	if indexErr == nil {
		if indexedEpoch != params.EpochID {
			return fmt.Errorf("%w: escrow %s exists in epoch %d, requested epoch %d",
				ErrSessionEpochConflict, params.EscrowID, indexedEpoch, params.EpochID)
		}
	} else if errors.Is(indexErr, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx,
			`INSERT INTO devshard_session_index (escrow_id, epoch_id)
			 VALUES ($1, $2)
			 ON CONFLICT (escrow_id) DO NOTHING`,
			params.EscrowID, params.EpochID,
		); err != nil {
			return fmt.Errorf("insert session index: %w", err)
		}
		if err := tx.QueryRow(ctx,
			`SELECT epoch_id FROM devshard_session_index WHERE escrow_id = $1`,
			params.EscrowID,
		).Scan(&indexedEpoch); err != nil {
			return fmt.Errorf("read session index: %w", err)
		}
		if indexedEpoch != params.EpochID {
			return fmt.Errorf("%w: escrow %s exists in epoch %d, requested epoch %d",
				ErrSessionEpochConflict, params.EscrowID, indexedEpoch, params.EpochID)
		}
	} else {
		return fmt.Errorf("read session index: %w", indexErr)
	}

	_, err = tx.Exec(ctx,
		`INSERT INTO devshard_sessions
		    (epoch_id, escrow_id, version, creator_addr, config_json, group_json, initial_balance)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (epoch_id, escrow_id) DO NOTHING`,
		params.EpochID, params.EscrowID, requestedVersion,
		params.CreatorAddr, string(configJSON), string(groupJSON), params.InitialBalance,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	var storedVersion *string
	if err := tx.QueryRow(ctx,
		`SELECT version FROM devshard_sessions WHERE epoch_id = $1 AND escrow_id = $2`,
		params.EpochID, params.EscrowID,
	).Scan(&storedVersion); err != nil {
		return fmt.Errorf("read session version: %w", err)
	}
	stored := ""
	if storedVersion != nil {
		stored = *storedVersion
	}
	if strings.TrimSpace(stored) == "" {
		return fmt.Errorf("%w: %s", ErrSessionVersionRequired, params.EscrowID)
	}
	if stored != requestedVersion {
		return fmt.Errorf("%w: escrow %s exists with version %s, requested %s",
			ErrSessionVersionConflict, params.EscrowID, stored, requestedVersion)
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}

	s.mu.Lock()
	s.escrowIdx[params.EscrowID] = params.EpochID
	s.mu.Unlock()
	return nil
}

func (s *Postgres) MarkSettled(escrowID string) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	tag, err := s.pool.Exec(ctx,
		`UPDATE devshard_sessions SET status = 'settled', settled_at = $1
		 WHERE epoch_id = $2 AND escrow_id = $3`,
		time.Now().Unix(), epochID, escrowID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found", escrowID)
	}
	return nil
}

func (s *Postgres) ListActiveSessions() ([]ActiveSession, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT epoch_id, escrow_id FROM devshard_sessions WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ActiveSession
	for rows.Next() {
		var epochID uint64
		var escrowID string
		if err := rows.Scan(&epochID, &escrowID); err != nil {
			return nil, err
		}
		result = append(result, ActiveSession{EscrowID: escrowID, EpochID: epochID})
	}
	return result, rows.Err()
}

func (s *Postgres) AppendDiff(escrowID string, rec types.DiffRecord) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}

	txsProto, err := marshalTxs(rec.Txs)
	if err != nil {
		return err
	}

	var warmJSON *string
	if len(rec.WarmKeyDelta) > 0 {
		b, err := json.Marshal(rec.WarmKeyDelta)
		if err != nil {
			return fmt.Errorf("marshal warm keys: %w", err)
		}
		str := string(b)
		warmJSON = &str
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.Exec(ctx,
		`INSERT INTO devshard_diffs
		    (epoch_id, escrow_id, nonce, txs_proto, user_sig, post_state_root, state_hash, warm_keys_json, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		epochID, escrowID, rec.Nonce, txsProto, rec.UserSig, rec.PostStateRoot, rec.StateHash, warmJSON, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert diff: %w", err)
	}

	for slotID, sig := range rec.Signatures {
		_, err = tx.Exec(ctx,
			`INSERT INTO devshard_signatures (epoch_id, escrow_id, nonce, slot_id, sig)
			 VALUES ($1, $2, $3, $4, $5)
			 ON CONFLICT (epoch_id, escrow_id, nonce, slot_id) DO UPDATE SET sig = EXCLUDED.sig`,
			epochID, escrowID, rec.Nonce, slotID, sig,
		)
		if err != nil {
			return fmt.Errorf("insert sig: %w", err)
		}
	}

	_, err = tx.Exec(ctx,
		`UPDATE devshard_sessions SET latest_nonce = GREATEST(latest_nonce, $1)
		 WHERE epoch_id = $2 AND escrow_id = $3`,
		rec.Nonce, epochID, escrowID,
	)
	if err != nil {
		return fmt.Errorf("update latest_nonce: %w", err)
	}

	return tx.Commit(ctx)
}

// AppendDiffs inserts many diffs for one escrow in a single transaction using
// COPY for diffs and signatures, then one latest_nonce update. Empty input is a
// no-op. Callers must only pass new nonces (no duplicates with existing rows).
func (s *Postgres) AppendDiffs(escrowID string, diffs []types.DiffRecord) error {
	if len(diffs) == 0 {
		return nil
	}
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}

	type preparedDiff struct {
		rec      types.DiffRecord
		txsProto []byte
		warmJSON *string
	}
	prepared := make([]preparedDiff, 0, len(diffs))
	var maxNonce uint64
	sigCount := 0
	for _, rec := range diffs {
		txsProto, err := marshalTxs(rec.Txs)
		if err != nil {
			return err
		}
		var warmJSON *string
		if len(rec.WarmKeyDelta) > 0 {
			b, err := json.Marshal(rec.WarmKeyDelta)
			if err != nil {
				return fmt.Errorf("marshal warm keys nonce %d: %w", rec.Nonce, err)
			}
			str := string(b)
			warmJSON = &str
		}
		prepared = append(prepared, preparedDiff{rec: rec, txsProto: txsProto, warmJSON: warmJSON})
		sigCount += len(rec.Signatures)
		if rec.Nonce > maxNonce {
			maxNonce = rec.Nonce
		}
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	_, err = tx.CopyFrom(ctx,
		pgx.Identifier{pgDiffsParent},
		[]string{"epoch_id", "escrow_id", "nonce", "txs_proto", "user_sig", "post_state_root", "state_hash", "warm_keys_json", "created_at"},
		pgx.CopyFromSlice(len(prepared), func(i int) ([]any, error) {
			p := prepared[i]
			return []any{
				epochID,
				escrowID,
				p.rec.Nonce,
				p.txsProto,
				p.rec.UserSig,
				p.rec.PostStateRoot,
				p.rec.StateHash,
				p.warmJSON,
				p.rec.CreatedAt,
			}, nil
		}),
	)
	if err != nil {
		return fmt.Errorf("copy diffs: %w", err)
	}

	if sigCount > 0 {
		sigRows := make([][]any, 0, sigCount)
		for _, p := range prepared {
			for slotID, sig := range p.rec.Signatures {
				sigRows = append(sigRows, []any{epochID, escrowID, p.rec.Nonce, slotID, sig})
			}
		}
		_, err = tx.CopyFrom(ctx,
			pgx.Identifier{pgSignaturesParent},
			[]string{"epoch_id", "escrow_id", "nonce", "slot_id", "sig"},
			pgx.CopyFromRows(sigRows),
		)
		if err != nil {
			return fmt.Errorf("copy signatures: %w", err)
		}
	}

	_, err = tx.Exec(ctx,
		`UPDATE devshard_sessions SET latest_nonce = GREATEST(latest_nonce, $1)
		 WHERE epoch_id = $2 AND escrow_id = $3`,
		maxNonce, epochID, escrowID,
	)
	if err != nil {
		return fmt.Errorf("update latest_nonce: %w", err)
	}
	return tx.Commit(ctx)
}

// ImportValidationObs upserts live and sealed validation-obs rows with exact
// counters. Used by HA SQLite→Postgres migrate.
func (s *Postgres) ImportValidationObs(escrowID string, live, sealed []ValidationObsRow) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	importRows := func(table string, rows []ValidationObsRow) error {
		if len(rows) == 0 {
			return nil
		}
		for _, r := range rows {
			_, err := tx.Exec(ctx, fmt.Sprintf(
				`INSERT INTO %s (epoch_id, escrow_id, inference_id, slot_id, required_validations, completed_validations)
				 VALUES ($1, $2, $3, $4, $5, $6)
				 ON CONFLICT (epoch_id, escrow_id, inference_id, slot_id) DO UPDATE SET
				   required_validations = EXCLUDED.required_validations,
				   completed_validations = EXCLUDED.completed_validations`, table),
				epochID, escrowID, r.InferenceID, r.SlotID, r.Required, r.Completed,
			)
			if err != nil {
				return fmt.Errorf("import %s: %w", table, err)
			}
		}
		return nil
	}
	if err := importRows(pgInferenceValidationObsParent, live); err != nil {
		return err
	}
	if err := importRows(pgSealedValidationObsParent, sealed); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Postgres) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO devshard_signatures (epoch_id, escrow_id, nonce, slot_id, sig)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (epoch_id, escrow_id, nonce, slot_id) DO UPDATE SET sig = EXCLUDED.sig`,
		epochID, escrowID, nonce, slotID, sig,
	)
	return err
}

func (s *Postgres) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT slot_id, sig FROM devshard_signatures
		 WHERE epoch_id = $1 AND escrow_id = $2 AND nonce = $3`,
		epochID, escrowID, nonce,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[uint32][]byte)
	for rows.Next() {
		var slotID uint32
		var sig []byte
		if err := rows.Scan(&slotID, &sig); err != nil {
			return nil, err
		}
		result[slotID] = sig
	}
	return result, rows.Err()
}

func (s *Postgres) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	row := s.pool.QueryRow(ctx,
		`SELECT escrow_id, version, creator_addr, config_json, group_json,
		        initial_balance, latest_nonce, last_finalized, status
		 FROM devshard_sessions
		 WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	)
	var meta SessionMeta
	var version *string
	var configJSON, groupJSON string
	scanErr := row.Scan(
		&meta.EscrowID, &version, &meta.CreatorAddr, &configJSON, &groupJSON,
		&meta.InitialBalance, &meta.LatestNonce, &meta.LastFinalized, &meta.Status,
	)
	if scanErr != nil {
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
		}
		return nil, scanErr
	}
	if err := json.Unmarshal([]byte(configJSON), &meta.Config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := json.Unmarshal([]byte(groupJSON), &meta.Group); err != nil {
		return nil, fmt.Errorf("unmarshal group: %w", err)
	}
	if version != nil {
		meta.Version = *version
	}
	meta.EpochID = epochID
	if err := finalizeSessionMeta(&meta); err != nil {
		return nil, err
	}
	return &meta, nil
}

func (s *Postgres) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT d.nonce, d.txs_proto, d.user_sig, d.post_state_root, d.state_hash,
		        d.warm_keys_json, d.created_at, s.slot_id, s.sig
		 FROM devshard_diffs d
		 LEFT JOIN devshard_signatures s
		        ON d.epoch_id = s.epoch_id AND d.escrow_id = s.escrow_id AND d.nonce = s.nonce
		 WHERE d.epoch_id = $1 AND d.escrow_id = $2 AND d.nonce >= $3 AND d.nonce <= $4
		 ORDER BY d.nonce, s.slot_id`,
		epochID, escrowID, fromNonce, toNonce,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []types.DiffRecord
	var current *types.DiffRecord
	var currentNonce uint64

	for rows.Next() {
		var nonce uint64
		var txsProto []byte
		var userSig, postStateRoot, stateHash []byte
		var warmJSON *string
		var createdAt int64
		var slotID *uint32
		var sig []byte

		if err := rows.Scan(&nonce, &txsProto, &userSig, &postStateRoot, &stateHash, &warmJSON, &createdAt, &slotID, &sig); err != nil {
			return nil, err
		}

		if current == nil || nonce != currentNonce {
			if current != nil {
				result = append(result, *current)
			}

			txs, err := unmarshalTxs(txsProto)
			if err != nil {
				return nil, err
			}

			rec := types.DiffRecord{
				Diff: types.Diff{
					Nonce:         nonce,
					Txs:           txs,
					UserSig:       userSig,
					PostStateRoot: postStateRoot,
				},
				StateHash: stateHash,
				CreatedAt: createdAt,
			}
			if warmJSON != nil {
				wk := make(map[uint32]string)
				if err := json.Unmarshal([]byte(*warmJSON), &wk); err != nil {
					return nil, fmt.Errorf("unmarshal warm keys: %w", err)
				}
				rec.WarmKeyDelta = wk
			}
			current = &rec
			currentNonce = nonce
		}

		if slotID != nil && sig != nil {
			if current.Signatures == nil {
				current.Signatures = make(map[uint32][]byte)
			}
			current.Signatures[*slotID] = sig
		}
	}

	if current != nil {
		result = append(result, *current)
	}

	return result, rows.Err()
}

func (s *Postgres) MarkFinalized(escrowID string, nonce uint64) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	tag, err := s.pool.Exec(ctx,
		`UPDATE devshard_sessions SET last_finalized = GREATEST(last_finalized, $1)
		 WHERE epoch_id = $2 AND escrow_id = $3`,
		nonce, epochID, escrowID,
	)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session %s not found", escrowID)
	}
	return nil
}

func (s *Postgres) LastFinalized(escrowID string) (uint64, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return 0, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	row := s.pool.QueryRow(ctx,
		`SELECT last_finalized FROM devshard_sessions WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	)
	var nonce uint64
	if err := row.Scan(&nonce); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, fmt.Errorf("session %s not found", escrowID)
		}
		return 0, err
	}
	return nonce, nil
}

func (s *Postgres) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO devshard_snapshots (epoch_id, escrow_id, nonce, state_data, created_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (epoch_id, escrow_id) DO UPDATE
		 SET nonce = EXCLUDED.nonce, state_data = EXCLUDED.state_data, created_at = EXCLUDED.created_at
		 WHERE devshard_snapshots.nonce <= EXCLUDED.nonce`,
		epochID, escrowID, nonce, data, time.Now().Unix(),
	)
	return err
}

func (s *Postgres) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return 0, nil, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	row := s.pool.QueryRow(ctx,
		`SELECT nonce, state_data FROM devshard_snapshots WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	)
	var nonce uint64
	var data []byte
	if err := row.Scan(&nonce, &data); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil, ErrSnapshotNotFound
		}
		return 0, nil, err
	}
	return nonce, data, nil
}

func (s *Postgres) InsertSealedInference(escrowID string, row InferenceRow) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	_, err = s.pool.Exec(ctx,
		`INSERT INTO devshard_sealed_inferences (
			epoch_id, escrow_id, inference_id, sealed_nonce,
			obs_present, sealed_status, sealed_executor_slot,
			sealed_votes_valid, sealed_votes_invalid, sealed_validated_by,
			sealed_model, sealed_prompt_hash, sealed_response_hash,
			sealed_input_length, sealed_max_tokens,
			sealed_input_tokens, sealed_output_tokens,
			sealed_reserved_cost, sealed_actual_cost,
			sealed_started_at, sealed_confirmed_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
		ON CONFLICT (epoch_id, escrow_id, inference_id) DO UPDATE SET
			sealed_nonce = EXCLUDED.sealed_nonce,
			obs_present = EXCLUDED.obs_present,
			sealed_status = EXCLUDED.sealed_status,
			sealed_executor_slot = EXCLUDED.sealed_executor_slot,
			sealed_votes_valid = EXCLUDED.sealed_votes_valid,
			sealed_votes_invalid = EXCLUDED.sealed_votes_invalid,
			sealed_validated_by = EXCLUDED.sealed_validated_by,
			sealed_model = EXCLUDED.sealed_model,
			sealed_prompt_hash = EXCLUDED.sealed_prompt_hash,
			sealed_response_hash = EXCLUDED.sealed_response_hash,
			sealed_input_length = EXCLUDED.sealed_input_length,
			sealed_max_tokens = EXCLUDED.sealed_max_tokens,
			sealed_input_tokens = EXCLUDED.sealed_input_tokens,
			sealed_output_tokens = EXCLUDED.sealed_output_tokens,
			sealed_reserved_cost = EXCLUDED.sealed_reserved_cost,
			sealed_actual_cost = EXCLUDED.sealed_actual_cost,
			sealed_started_at = EXCLUDED.sealed_started_at,
			sealed_confirmed_at = EXCLUDED.sealed_confirmed_at`,
		epochID, escrowID, row.InferenceID, row.SealedNonce, row.ObsPresent,
		row.SealedStatus, row.SealedExecutorSlot,
		row.SealedVotesValid, row.SealedVotesInvalid, row.SealedValidatedBy,
		row.SealedModel, row.SealedPromptHash, row.SealedResponseHash,
		row.SealedInputLength, row.SealedMaxTokens,
		row.SealedInputTokens, row.SealedOutputTokens,
		row.SealedReservedCost, row.SealedActualCost,
		row.SealedStartedAt, row.SealedConfirmedAt,
	)
	if err != nil {
		return fmt.Errorf("insert sealed inference: %w", err)
	}
	return nil
}

func (s *Postgres) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return InferenceRow{}, false, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	row := s.pool.QueryRow(ctx,
		`SELECT sealed_nonce, obs_present, sealed_status, sealed_executor_slot,
		        sealed_votes_valid, sealed_votes_invalid, sealed_validated_by,
		        sealed_model, sealed_prompt_hash, sealed_response_hash,
		        sealed_input_length, sealed_max_tokens,
		        sealed_input_tokens, sealed_output_tokens,
		        sealed_reserved_cost, sealed_actual_cost,
		        sealed_started_at, sealed_confirmed_at
		   FROM devshard_sealed_inferences
		  WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		epochID, escrowID, inferenceID,
	)
	out := InferenceRow{InferenceID: inferenceID}
	if err := row.Scan(
		&out.SealedNonce, &out.ObsPresent, &out.SealedStatus, &out.SealedExecutorSlot,
		&out.SealedVotesValid, &out.SealedVotesInvalid, &out.SealedValidatedBy,
		&out.SealedModel, &out.SealedPromptHash, &out.SealedResponseHash,
		&out.SealedInputLength, &out.SealedMaxTokens,
		&out.SealedInputTokens, &out.SealedOutputTokens,
		&out.SealedReservedCost, &out.SealedActualCost,
		&out.SealedStartedAt, &out.SealedConfirmedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return InferenceRow{}, false, nil
		}
		return InferenceRow{}, false, err
	}
	return out, true, nil
}

func (s *Postgres) DeleteSealedInferences(escrowID string) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_sealed_inferences WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	); err != nil {
		return fmt.Errorf("delete sealed inferences: %w", err)
	}
	return nil
}

func (s *Postgres) ClearValidationObs(escrowID string) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_inference_validation_obs WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	); err != nil {
		return fmt.Errorf("clear inference validation obs: %w", err)
	}
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM devshard_sealed_validation_obs WHERE epoch_id = $1 AND escrow_id = $2`,
		epochID, escrowID,
	); err != nil {
		return fmt.Errorf("clear sealed validation obs: %w", err)
	}
	return nil
}

func (s *Postgres) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	if len(entries) == 0 {
		return nil
	}
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	inferenceIDs := make([]int64, len(entries))
	slotIDs := make([]int32, len(entries))
	for i, e := range entries {
		inferenceIDs[i] = int64(e.InferenceID)
		slotIDs[i] = int32(e.SlotID)
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO devshard_inference_validation_obs (epoch_id, escrow_id, inference_id, slot_id, required_validations, completed_validations)
		 SELECT $1, $2, t.inference_id, t.slot_id, 1, 1
		 FROM unnest($3::bigint[], $4::int[]) AS t(inference_id, slot_id)
		 ON CONFLICT (epoch_id, escrow_id, inference_id, slot_id) DO NOTHING`,
		epochID, escrowID, inferenceIDs, slotIDs,
	)
	if err != nil {
		return fmt.Errorf("record validations applied once: %w", err)
	}
	return nil
}

func (s *Postgres) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	if err := s.ensurePartition(ctx, epochID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("drain inference validation obs begin: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT slot_id, required_validations, completed_validations
		   FROM devshard_inference_validation_obs
		  WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		epochID, escrowID, inferenceID,
	)
	if err != nil {
		return fmt.Errorf("drain inference validation obs select: %w", err)
	}
	type row struct {
		slotID              uint32
		required, completed uint32
	}
	var live []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.slotID, &r.required, &r.completed); err != nil {
			rows.Close()
			return err
		}
		live = append(live, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, r := range live {
		if _, err := tx.Exec(ctx,
			`INSERT INTO devshard_sealed_validation_obs (epoch_id, escrow_id, inference_id, slot_id, required_validations, completed_validations)
			 VALUES ($1, $2, $3, $4, $5, $6)
			 ON CONFLICT (epoch_id, escrow_id, inference_id, slot_id) DO UPDATE SET
			   required_validations = devshard_sealed_validation_obs.required_validations + EXCLUDED.required_validations,
			   completed_validations = devshard_sealed_validation_obs.completed_validations + EXCLUDED.completed_validations`,
			epochID, escrowID, inferenceID, r.slotID, r.required, r.completed,
		); err != nil {
			return fmt.Errorf("drain inference validation obs insert: %w", err)
		}
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM devshard_inference_validation_obs WHERE epoch_id = $1 AND escrow_id = $2 AND inference_id = $3`,
		epochID, escrowID, inferenceID,
	); err != nil {
		return fmt.Errorf("drain inference validation obs delete: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Postgres) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx,
		`SELECT slot_id,
		        SUM(required_validations)::int AS required_validations,
		        SUM(completed_validations)::int AS completed_validations
		   FROM (
		     SELECT slot_id, required_validations, completed_validations
		       FROM devshard_inference_validation_obs
		      WHERE epoch_id = $1 AND escrow_id = $2
		     UNION ALL
		     SELECT slot_id, required_validations, completed_validations
		       FROM devshard_sealed_validation_obs
		      WHERE epoch_id = $1 AND escrow_id = $2
		   ) AS combined
		  GROUP BY slot_id
		  ORDER BY slot_id`,
		epochID, escrowID,
	)
	if err != nil {
		return nil, fmt.Errorf("get validation observability: %w", err)
	}
	defer rows.Close()

	var out []SlotValidationObs
	for rows.Next() {
		var obs SlotValidationObs
		if err := rows.Scan(&obs.SlotID, &obs.RequiredValidations, &obs.CompletedValidations); err != nil {
			return nil, err
		}
		out = append(out, obs)
	}
	return out, rows.Err()
}

// PruneEpoch drops all per-epoch partitions for epochID and forgets every
// escrow index entry that pointed at it. Other epochs are not touched.
// No-op if the partitions do not exist.
func (s *Postgres) PruneEpoch(epochID uint64) error {
	ctx, cancel := s.opCtx()
	defer cancel()
	for _, partition := range []string{
		pgDiffsPartition(epochID),
		pgSignaturesPartition(epochID),
		pgSnapshotsPartition(epochID),
		pgInferencesPartition(epochID),
		pgValidationObsPartition(epochID),
		pgInferenceValidationObsPartition(epochID),
		pgSealedValidationObsPartition(epochID),
		pgSealedValidationObsPartition(epochID),
		pgValidationLeasesPartition(epochID),
		pgSessionsPartition(epochID),
	} {
		_, err := s.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, partition))
		if err != nil {
			return fmt.Errorf("drop %s: %w", partition, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM devshard_session_index WHERE epoch_id = $1`, epochID); err != nil {
		return fmt.Errorf("prune session index for epoch %d: %w", epochID, err)
	}

	s.mu.Lock()
	delete(s.knownEpochs, epochID)
	for esc, ep := range s.escrowIdx {
		if ep == epochID {
			delete(s.escrowIdx, esc)
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *Postgres) pruneBefore(cutoff uint64) error {
	if cutoff == 0 {
		return nil
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.pool.Query(ctx, `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname IN ('devshard_sessions', 'devshard_diffs', 'devshard_signatures', 'devshard_snapshots', 'devshard_sealed_inferences', 'devshard_validation_leases', 'devshard_slot_validation_obs', 'devshard_inference_validation_obs', 'devshard_sealed_validation_obs')
	`)
	if err != nil {
		return fmt.Errorf("list devshard partitions: %w", err)
	}
	var partitions []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		if epochID, ok := pgPartitionEpoch(name); ok && epochID < cutoff {
			partitions = append(partitions, name)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, partition := range partitions {
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, partition)); err != nil {
			return fmt.Errorf("drop %s: %w", partition, err)
		}
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM devshard_session_index WHERE epoch_id < $1`, cutoff); err != nil {
		return fmt.Errorf("prune session index before epoch %d: %w", cutoff, err)
	}

	s.mu.Lock()
	for epochID := range s.knownEpochs {
		if epochID < cutoff {
			delete(s.knownEpochs, epochID)
		}
	}
	for esc, ep := range s.escrowIdx {
		if ep < cutoff {
			delete(s.escrowIdx, esc)
		}
	}
	s.mu.Unlock()
	return nil
}

func pgPartitionEpoch(name string) (uint64, bool) {
	for _, parent := range []string{pgSessionsParent, pgDiffsParent, pgSignaturesParent, pgSnapshotsParent, pgInferencesParent, pgValidationLeasesParent, pgValidationObsParent, pgInferenceValidationObsParent, pgSealedValidationObsParent} {
		prefix := parent + "_epoch_"
		if strings.HasPrefix(name, prefix) {
			epochID, err := strconv.ParseUint(strings.TrimPrefix(name, prefix), 10, 64)
			return epochID, err == nil
		}
	}
	return 0, false
}

var _ Storage = (*Postgres)(nil)
