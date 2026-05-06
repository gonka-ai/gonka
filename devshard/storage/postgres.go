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
// Three parent tables -- devshard_sessions, devshard_diffs, devshard_signatures
// -- each PARTITION BY RANGE (epoch_id). One partition per epoch is created
// lazily on first write. PruneEpoch is a single DROP TABLE per parent, so it is
// O(1) and never touches other epochs' pages.
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
	pgSessionsParent   = "devshard_sessions"
	pgDiffsParent      = "devshard_diffs"
	pgSignaturesParent = "devshard_signatures"
	pgSnapshotsParent  = "devshard_snapshots"
	pgSessionIndex     = "devshard_session_index"
)

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

const pgCreateParents = `
CREATE TABLE IF NOT EXISTS devshard_session_index (
    escrow_id TEXT   PRIMARY KEY,
    epoch_id  BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS devshard_session_index_by_epoch ON devshard_session_index(epoch_id);

CREATE TABLE IF NOT EXISTS devshard_sessions (
    epoch_id        BIGINT NOT NULL,
    escrow_id       TEXT   NOT NULL,
    version         TEXT,
    creator_addr    TEXT   NOT NULL,
    config_json     TEXT   NOT NULL,
    group_json      TEXT   NOT NULL,
    initial_balance BIGINT NOT NULL,
    latest_nonce    BIGINT NOT NULL DEFAULT 0,
    last_finalized  BIGINT NOT NULL DEFAULT 0,
    status          TEXT   NOT NULL DEFAULT 'active',
    settled_at      BIGINT,
    PRIMARY KEY (epoch_id, escrow_id)
) PARTITION BY RANGE (epoch_id);

CREATE TABLE IF NOT EXISTS devshard_diffs (
    epoch_id        BIGINT NOT NULL,
    escrow_id       TEXT   NOT NULL,
    nonce           BIGINT NOT NULL,
    txs_proto       BYTEA  NOT NULL,
    user_sig        BYTEA,
    post_state_root BYTEA,
    state_hash      BYTEA,
    warm_keys_json  TEXT,
    created_at      BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id, nonce)
) PARTITION BY RANGE (epoch_id);

CREATE TABLE IF NOT EXISTS devshard_signatures (
    epoch_id  BIGINT NOT NULL,
    escrow_id TEXT   NOT NULL,
    nonce     BIGINT NOT NULL,
    slot_id   BIGINT NOT NULL,
    sig       BYTEA  NOT NULL,
    PRIMARY KEY (epoch_id, escrow_id, nonce, slot_id)
) PARTITION BY RANGE (epoch_id);

CREATE TABLE IF NOT EXISTS devshard_snapshots (
    epoch_id   BIGINT NOT NULL,
    escrow_id  TEXT   NOT NULL,
    nonce      BIGINT NOT NULL,
    state_data BYTEA  NOT NULL,
    created_at BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (epoch_id, escrow_id)
) PARTITION BY RANGE (epoch_id);
`

// NewPostgres opens a Postgres-backed Storage using the standard libpq env
// vars (PGHOST, PGPORT, PGDATABASE, PGUSER, PGPASSWORD). Schema is created
// idempotently and the escrow index is rebuilt by scanning devshard_sessions.
func NewPostgres(ctx context.Context) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	if _, err := pool.Exec(ctx, pgCreateParents); err != nil {
		pool.Close()
		return nil, fmt.Errorf("create parents: %w", err)
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

// ensurePartition creates per-epoch partitions for all three parents on first
// touch. The check + create is racy across multiple writers, but PG returns
// 42P07 (table already exists) which we swallow.
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

func (s *Postgres) CreateSession(params CreateSessionParams) error {
	configJSON, err := json.Marshal(params.Config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	groupJSON, err := json.Marshal(params.Group)
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}
	requestedVersion := types.NormalizeSessionVersion(params.Version)

	ctx := context.Background()
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
	normalizedStoredVersion := types.LegacySessionVersion
	if storedVersion != nil {
		normalizedStoredVersion = types.NormalizeSessionVersion(*storedVersion)
	}
	if normalizedStoredVersion != requestedVersion {
		return fmt.Errorf("%w: escrow %s exists with version %s, requested %s",
			ErrSessionVersionConflict, params.EscrowID, normalizedStoredVersion, requestedVersion)
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
	tag, err := s.pool.Exec(context.Background(),
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
	rows, err := s.pool.Query(context.Background(),
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

	ctx := context.Background()
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

func (s *Postgres) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(context.Background(),
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
	rows, err := s.pool.Query(context.Background(),
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
	row := s.pool.QueryRow(context.Background(),
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
	return &meta, nil
}

func (s *Postgres) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	epochID, err := s.lookupEpoch(escrowID)
	if err != nil {
		return nil, err
	}

	rows, err := s.pool.Query(context.Background(),
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
	tag, err := s.pool.Exec(context.Background(),
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
	row := s.pool.QueryRow(context.Background(),
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
	if _, err := s.pool.Exec(context.Background(),
		fmt.Sprintf(
			`CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM (%d) TO (%d)`,
			pgSnapshotsPartition(epochID), pgSnapshotsParent, epochID, epochID+1,
		),
	); err != nil {
		return fmt.Errorf("create snapshot partition: %w", err)
	}
	_, err = s.pool.Exec(context.Background(),
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
	row := s.pool.QueryRow(context.Background(),
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

// PruneEpoch drops all per-epoch partitions for epochID and forgets every
// escrow index entry that pointed at it. Other epochs are not touched.
// No-op if the partitions do not exist.
func (s *Postgres) PruneEpoch(epochID uint64) error {
	ctx := context.Background()
	for _, partition := range []string{
		pgDiffsPartition(epochID),
		pgSignaturesPartition(epochID),
		pgSnapshotsPartition(epochID),
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

	rows, err := s.pool.Query(context.Background(), `
		SELECT c.relname
		FROM pg_class c
		JOIN pg_inherits i ON i.inhrelid = c.oid
		JOIN pg_class p ON p.oid = i.inhparent
		WHERE p.relname IN ('devshard_sessions', 'devshard_diffs', 'devshard_signatures', 'devshard_snapshots')
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

	ctx := context.Background()
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
	for _, parent := range []string{pgSessionsParent, pgDiffsParent, pgSignaturesParent, pgSnapshotsParent} {
		prefix := parent + "_epoch_"
		if strings.HasPrefix(name, prefix) {
			epochID, err := strconv.ParseUint(strings.TrimPrefix(name, prefix), 10, 64)
			return epochID, err == nil
		}
	}
	return 0, false
}

var _ Storage = (*Postgres)(nil)
