package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	"devshard/types"

	_ "modernc.org/sqlite"
)

// SQLite implements Storage with one DB file per epoch under a base directory,
// plus a small _meta.db sidecar that holds the escrow_id -> epoch_id mapping.
//
// Layout:
//
//	<baseDir>/_meta.db           shared escrow_id -> epoch_id index
//	<baseDir>/epoch_<id>.db      per-epoch sessions/diffs/signatures
//
// Per-epoch files give us O(1) pruning (close handles + os.Remove) without
// touching any other epoch's pages, and they cap the active SQLite file count
// at the retention horizon.
//
// _meta.db is the explicit, persistent home for the escrow_id -> epoch_id
// mapping. NewSQLite reads it first, then runs an eager reconcile over existing
// epoch_*.db files to repair crash leftovers and detect split escrows.
//
// Each epoch file still holds the three-table schema: sessions, diffs,
// signatures. The session row carries the same metadata it always did
// (config, group, balance, ...) -- _meta.db only has the routing key.
type SQLite struct {
	baseDir string
	metaDB  *sql.DB

	mu        sync.RWMutex
	createMu  sync.Mutex
	pools     map[uint64]*epochPool
	escrowIdx map[string]uint64
}

// epochPool holds one writer + one reader pool against a single epoch file.
// Mirrors the original SQLite design: WAL mode allows readers to proceed
// without blocking on an active writer transaction.
type epochPool struct {
	writeDB *sql.DB
	readDB  *sql.DB
}

const epochFilePrefix = "epoch_"
const epochFileSuffix = ".db"
const metaDBFile = "_meta.db"

var epochFileRegex = regexp.MustCompile(`^epoch_(\d+)\.db$`)

// NewSQLite opens (or creates) a per-epoch SQLite store under baseDir. Reads
// the escrow_id -> epoch_id index from _meta.db so per-epoch DBs do not need
// to be opened until a request actually touches them.
//
// The reconcile pass at the end is a defense against partial writes: if a
// crash dropped the _meta row but the per-epoch row landed, the meta is
// repaired from on-disk epoch_*.db files. Going forward, writes update both
// in the same code path.
func NewSQLite(baseDir string) (*SQLite, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir %s: %w", baseDir, err)
	}

	metaDB, err := openMetaDB(filepath.Join(baseDir, metaDBFile))
	if err != nil {
		return nil, fmt.Errorf("open meta db: %w", err)
	}

	s := &SQLite{
		baseDir:   baseDir,
		metaDB:    metaDB,
		pools:     make(map[uint64]*epochPool),
		escrowIdx: make(map[string]uint64),
	}

	if err := s.loadIndexFromMeta(); err != nil {
		metaDB.Close()
		return nil, fmt.Errorf("load index: %w", err)
	}
	if err := s.reconcileMetaFromEpochFiles(); err != nil {
		s.closeAll()
		return nil, fmt.Errorf("reconcile meta: %w", err)
	}

	return s, nil
}

// openMetaDB opens (or creates) the small _meta.db sidecar that holds the
// escrow_id -> epoch_id index. Single connection: this DB is touched only
// at boot, on CreateSession, and on PruneEpoch -- so write contention is a
// non-issue and one connection keeps the operational model simple.
func openMetaDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	pragmas := []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s on meta db: %w", p, err)
		}
	}
	if err := MigrateMeta(context.Background(), db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

func (s *SQLite) loadIndexFromMeta() error {
	rows, err := s.metaDB.Query(`SELECT escrow_id, epoch_id FROM escrow_epoch`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var escrowID string
		var epochID uint64
		if err := rows.Scan(&escrowID, &epochID); err != nil {
			return err
		}
		s.escrowIdx[escrowID] = epochID
	}
	return rows.Err()
}

// reconcileMetaFromEpochFiles is the conservative recovery path for partial
// writes. _meta.db remains only a routing index: startup verifies it against
// real session rows, removes proven-stale mappings, and adds mappings for
// sessions that exist on disk but are missing from _meta.db.
func (s *SQLite) reconcileMetaFromEpochFiles() error {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return fmt.Errorf("read base dir %s: %w", s.baseDir, err)
	}
	sessionsOnDisk := make(map[string]uint64)
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		m := epochFileRegex.FindStringSubmatch(ent.Name())
		if m == nil {
			continue
		}
		epochID, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		if err := s.collectEpochSessions(epochID, sessionsOnDisk); err != nil {
			return fmt.Errorf("reconcile epoch %d: %w", epochID, err)
		}
	}

	for escrowID, mappedEpoch := range s.escrowIdx {
		if diskEpoch, ok := sessionsOnDisk[escrowID]; ok && diskEpoch == mappedEpoch {
			continue
		}
		if _, err := s.metaDB.Exec(
			`DELETE FROM escrow_epoch WHERE escrow_id = ? AND epoch_id = ?`,
			escrowID, mappedEpoch,
		); err != nil {
			return fmt.Errorf("remove stale meta for %s: %w", escrowID, err)
		}
		delete(s.escrowIdx, escrowID)
	}

	for escrowID, epochID := range sessionsOnDisk {
		if mappedEpoch, ok := s.escrowIdx[escrowID]; ok {
			if mappedEpoch != epochID {
				return fmt.Errorf("%w: escrow %s meta epoch %d, disk epoch %d",
					ErrSessionEpochConflict, escrowID, mappedEpoch, epochID)
			}
			continue
		}
		if _, err := s.metaDB.Exec(
			`INSERT INTO escrow_epoch (escrow_id, epoch_id) VALUES (?, ?)`,
			escrowID, epochID,
		); err != nil {
			return fmt.Errorf("repair meta for %s: %w", escrowID, err)
		}
		s.escrowIdx[escrowID] = epochID
	}

	return nil
}

func (s *SQLite) collectEpochSessions(epochID uint64, sessionsOnDisk map[string]uint64) error {
	p, err := s.openOrLoadPool(epochID)
	if err != nil {
		return err
	}
	rows, err := p.readDB.Query(`SELECT escrow_id FROM sessions`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var escrowID string
		if err := rows.Scan(&escrowID); err != nil {
			return err
		}
		if existingEpoch, ok := sessionsOnDisk[escrowID]; ok && existingEpoch != epochID {
			return fmt.Errorf("%w: escrow %s exists in epochs %d and %d",
				ErrSessionEpochConflict, escrowID, existingEpoch, epochID)
		}
		sessionsOnDisk[escrowID] = epochID
	}
	return rows.Err()
}

func (s *SQLite) epochFilePath(epochID uint64) string {
	return filepath.Join(s.baseDir, fmt.Sprintf("%s%d%s", epochFilePrefix, epochID, epochFileSuffix))
}

// openOrLoadPool returns the pool for epochID, opening it lazily on miss.
// Caller need not hold s.mu; this method takes the write lock.
func (s *SQLite) openOrLoadPool(epochID uint64) (*epochPool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.pools[epochID]; ok {
		return p, nil
	}
	p, err := openEpochPool(s.epochFilePath(epochID))
	if err != nil {
		return nil, err
	}
	s.pools[epochID] = p
	return p, nil
}

// HasEscrow reports whether escrowID is present in the in-memory routing index
// (rebuilt at boot from _meta.db). It lets the hybrid router resolve which
// backend owns an escrow without a disk round trip.
func (s *SQLite) HasEscrow(escrowID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.escrowIdx[escrowID]
	return ok
}

// HasAnySessions reports whether SQLite still holds any session after startup
// reconciliation. HybridStorage uses it to decide whether SQLite must stay
// attached while Postgres handles new escrows.
func (s *SQLite) HasAnySessions() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.escrowIdx) > 0
}

// EscrowIDs returns a snapshot of escrows in the in-memory routing index.
func (s *SQLite) EscrowIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := make([]string, 0, len(s.escrowIdx))
	for id := range s.escrowIdx {
		ids = append(ids, id)
	}
	return ids
}

// poolFor returns the pool for the epoch this escrow belongs to, opening it
// lazily on first access. The escrow_id -> epoch_id lookup is in-memory
// (rebuilt at boot from _meta.db); the pool itself is opened on demand so a
// host that only touches a couple of escrows doesn't pay for opening every
// epoch_*.db on disk.
func (s *SQLite) poolFor(escrowID string) (*epochPool, uint64, error) {
	s.mu.RLock()
	epochID, ok := s.escrowIdx[escrowID]
	if !ok {
		s.mu.RUnlock()
		return nil, 0, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
	}
	p, poolOpen := s.pools[epochID]
	s.mu.RUnlock()
	if !poolOpen {
		opened, err := s.openOrLoadPool(epochID)
		if err != nil {
			return nil, 0, fmt.Errorf("open epoch %d for escrow %s: %w", epochID, escrowID, err)
		}
		return opened, epochID, nil
	}
	return p, epochID, nil
}

func openEpochPool(dbPath string) (*epochPool, error) {
	openAndConfigure := func(maxConns int) (*sql.DB, error) {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
		}
		db.SetMaxOpenConns(maxConns)
		pragmas := []string{
			"PRAGMA journal_mode=WAL",
			"PRAGMA synchronous=NORMAL",
			"PRAGMA busy_timeout=5000",
			"PRAGMA foreign_keys=ON",
		}
		for _, p := range pragmas {
			if _, err := db.Exec(p); err != nil {
				db.Close()
				return nil, fmt.Errorf("exec %s: %w", p, err)
			}
		}
		return db, nil
	}

	writeDB, err := openAndConfigure(1)
	if err != nil {
		return nil, fmt.Errorf("write pool: %w", err)
	}
	readDB, err := openAndConfigure(10)
	if err != nil {
		writeDB.Close()
		return nil, fmt.Errorf("read pool: %w", err)
	}

	if err := MigrateEpochPool(context.Background(), writeDB); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, err
	}

	return &epochPool{writeDB: writeDB, readDB: readDB}, nil
}

func (p *epochPool) close() error {
	var wErr error
	if p.writeDB == nil {
		wErr = fmt.Errorf("write db is nil")
	} else {
		wErr = p.writeDB.Close()
	}
	var rErr error
	if p.readDB == nil {
		rErr = fmt.Errorf("read db is nil")
	} else {
		rErr = p.readDB.Close()
	}
	if wErr != nil {
		return wErr
	}
	return rErr
}

// Close closes every per-epoch pool. Best-effort: returns the first error if
// any pool fails to close, but always tries every pool.
func (s *SQLite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.closeAllLocked()
	if metaErr := s.metaDB.Close(); metaErr != nil && err == nil {
		err = metaErr
	}
	return err
}

func (s *SQLite) closeAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = s.closeAllLocked()
	_ = s.metaDB.Close()
}

func (s *SQLite) closeAllLocked() error {
	var firstErr error
	for epochID, p := range s.pools {
		if err := p.close(); err != nil && firstErr == nil {
			firstErr = err
		}
		delete(s.pools, epochID)
	}
	return firstErr
}

func (s *SQLite) CreateSession(params CreateSessionParams) error {
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

	s.createMu.Lock()
	defer s.createMu.Unlock()

	s.mu.RLock()
	mappedEpoch, mapped := s.escrowIdx[params.EscrowID]
	s.mu.RUnlock()
	if mapped {
		if mappedEpoch != params.EpochID {
			return fmt.Errorf("%w: escrow %s exists in epoch %d, requested epoch %d",
				ErrSessionEpochConflict, params.EscrowID, mappedEpoch, params.EpochID)
		}
	} else if diskEpoch, ok, err := s.findSessionEpoch(params.EscrowID); err != nil {
		return err
	} else if ok {
		if diskEpoch != params.EpochID {
			return fmt.Errorf("%w: escrow %s exists in epoch %d, requested epoch %d",
				ErrSessionEpochConflict, params.EscrowID, diskEpoch, params.EpochID)
		}
	}

	p, err := s.openOrLoadPool(params.EpochID)
	if err != nil {
		return err
	}
	if mapped || s.sessionExists(p, params.EscrowID) {
		version, err := s.sessionVersion(p, params.EscrowID)
		if err != nil {
			return err
		}
		if version != requestedVersion {
			return fmt.Errorf("%w: escrow %s exists with version %s, requested %s",
				ErrSessionVersionConflict, params.EscrowID, version, requestedVersion)
		}
	}

	_, err = p.writeDB.Exec(
		`INSERT OR IGNORE INTO sessions (escrow_id, version, creator_addr, config_json, group_json, initial_balance)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		params.EscrowID, requestedVersion, params.CreatorAddr, string(configJSON), string(groupJSON), params.InitialBalance,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}

	if !mapped {
		if _, err := s.metaDB.Exec(
			`INSERT INTO escrow_epoch (escrow_id, epoch_id) VALUES (?, ?)`,
			params.EscrowID, params.EpochID,
		); err != nil {
			return fmt.Errorf("insert meta index: %w", err)
		}
	}

	s.mu.Lock()
	s.escrowIdx[params.EscrowID] = params.EpochID
	s.mu.Unlock()
	return nil
}

func (s *SQLite) sessionExists(p *epochPool, escrowID string) bool {
	var exists int
	err := p.readDB.QueryRow(`SELECT 1 FROM sessions WHERE escrow_id = ?`, escrowID).Scan(&exists)
	return err == nil
}

func (s *SQLite) sessionVersion(p *epochPool, escrowID string) (string, error) {
	var version sql.NullString
	err := p.readDB.QueryRow(`SELECT version FROM sessions WHERE escrow_id = ?`, escrowID).Scan(&version)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
		}
		return "", err
	}
	if !version.Valid || strings.TrimSpace(version.String) == "" {
		return "", fmt.Errorf("%w: %s", ErrSessionVersionRequired, escrowID)
	}
	return version.String, nil
}

func (s *SQLite) findSessionEpoch(escrowID string) (uint64, bool, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return 0, false, fmt.Errorf("read base dir %s: %w", s.baseDir, err)
	}
	var foundEpoch uint64
	found := false
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		m := epochFileRegex.FindStringSubmatch(ent.Name())
		if m == nil {
			continue
		}
		epochID, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil {
			continue
		}
		p, err := s.openOrLoadPool(epochID)
		if err != nil {
			return 0, false, err
		}
		var exists int
		err = p.readDB.QueryRow(`SELECT 1 FROM sessions WHERE escrow_id = ?`, escrowID).Scan(&exists)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return 0, false, fmt.Errorf("check session %s in epoch %d: %w", escrowID, epochID, err)
		}
		if found && foundEpoch != epochID {
			return 0, false, fmt.Errorf("%w: escrow %s exists in epochs %d and %d",
				ErrSessionEpochConflict, escrowID, foundEpoch, epochID)
		}
		foundEpoch = epochID
		found = true
	}
	return foundEpoch, found, nil
}

func (s *SQLite) MarkSettled(escrowID string) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	res, err := p.writeDB.Exec(
		`UPDATE sessions SET status = 'settled', settled_at = ? WHERE escrow_id = ?`,
		time.Now().Unix(), escrowID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found", escrowID)
	}
	return nil
}

// ListActiveSessions iterates every epoch known to the meta index and
// queries that epoch's sessions table for rows still in the 'active' state.
// Lazy-opens per-epoch DBs as needed (boot is the typical caller, where
// every active epoch will be opened anyway for diff replay).
func (s *SQLite) ListActiveSessions() ([]ActiveSession, error) {
	rows, err := s.metaDB.Query(`SELECT DISTINCT epoch_id FROM escrow_epoch`)
	if err != nil {
		return nil, fmt.Errorf("read meta epochs: %w", err)
	}
	var epochs []uint64
	for rows.Next() {
		var e uint64
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return nil, err
		}
		epochs = append(epochs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	var result []ActiveSession
	for _, epochID := range epochs {
		p, err := s.openOrLoadPool(epochID)
		if err != nil {
			return nil, fmt.Errorf("open epoch %d for list: %w", epochID, err)
		}
		sessRows, err := p.readDB.Query(`SELECT escrow_id FROM sessions WHERE status = 'active'`)
		if err != nil {
			return nil, err
		}
		for sessRows.Next() {
			var id string
			if err := sessRows.Scan(&id); err != nil {
				sessRows.Close()
				return nil, err
			}
			result = append(result, ActiveSession{EscrowID: id, EpochID: epochID})
		}
		if err := sessRows.Err(); err != nil {
			sessRows.Close()
			return nil, err
		}
		sessRows.Close()
	}
	return result, nil
}

func (s *SQLite) AppendDiff(escrowID string, rec types.DiffRecord) error {
	p, _, err := s.poolFor(escrowID)
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

	tx, err := p.writeDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		`INSERT INTO diffs (escrow_id, nonce, txs_proto, user_sig, post_state_root, state_hash, warm_keys_json, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		escrowID, rec.Nonce, txsProto, rec.UserSig, rec.PostStateRoot, rec.StateHash, warmJSON, rec.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert diff: %w", err)
	}

	for slotID, sig := range rec.Signatures {
		_, err = tx.Exec(
			`INSERT OR REPLACE INTO signatures (escrow_id, nonce, slot_id, sig) VALUES (?, ?, ?, ?)`,
			escrowID, rec.Nonce, slotID, sig,
		)
		if err != nil {
			return fmt.Errorf("insert sig: %w", err)
		}
	}

	_, err = tx.Exec(
		`UPDATE sessions SET latest_nonce = MAX(latest_nonce, ?) WHERE escrow_id = ?`,
		rec.Nonce, escrowID,
	)
	if err != nil {
		return fmt.Errorf("update latest_nonce: %w", err)
	}

	return tx.Commit()
}

func (s *SQLite) AddSignature(escrowID string, nonce uint64, slotID uint32, sig []byte) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	_, err = p.writeDB.Exec(
		`INSERT OR REPLACE INTO signatures (escrow_id, nonce, slot_id, sig) VALUES (?, ?, ?, ?)`,
		escrowID, nonce, slotID, sig,
	)
	return err
}

func (s *SQLite) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	rows, err := p.readDB.Query(
		`SELECT slot_id, sig FROM signatures WHERE escrow_id = ? AND nonce = ?`,
		escrowID, nonce,
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

func (s *SQLite) GetSessionMeta(escrowID string) (*SessionMeta, error) {
	p, epochID, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}

	row := p.readDB.QueryRow(
		`SELECT escrow_id, version, creator_addr, config_json, group_json, initial_balance, latest_nonce, last_finalized, status
		 FROM sessions WHERE escrow_id = ?`,
		escrowID,
	)

	var meta SessionMeta
	var version sql.NullString
	var configJSON, groupJSON string
	scanErr := row.Scan(
		&meta.EscrowID, &version, &meta.CreatorAddr, &configJSON, &groupJSON,
		&meta.InitialBalance, &meta.LatestNonce, &meta.LastFinalized, &meta.Status,
	)
	if scanErr != nil {
		if scanErr == sql.ErrNoRows {
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
	if version.Valid {
		meta.Version = version.String
	}
	meta.EpochID = epochID
	if err := finalizeSessionMeta(&meta); err != nil {
		return nil, err
	}

	return &meta, nil
}

func (s *SQLite) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}

	rows, err := p.readDB.Query(
		`SELECT d.nonce, d.txs_proto, d.user_sig, d.post_state_root, d.state_hash, d.warm_keys_json, d.created_at,
		        s.slot_id, s.sig
		 FROM diffs d
		 LEFT JOIN signatures s ON d.escrow_id = s.escrow_id AND d.nonce = s.nonce
		 WHERE d.escrow_id = ? AND d.nonce >= ? AND d.nonce <= ?
		 ORDER BY d.nonce, s.slot_id`,
		escrowID, fromNonce, toNonce,
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

func (s *SQLite) MarkFinalized(escrowID string, nonce uint64) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	res, err := p.writeDB.Exec(
		`UPDATE sessions SET last_finalized = MAX(last_finalized, ?) WHERE escrow_id = ?`,
		nonce, escrowID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("session %s not found", escrowID)
	}
	return nil
}

func (s *SQLite) LastFinalized(escrowID string) (uint64, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return 0, err
	}
	row := p.readDB.QueryRow(
		`SELECT last_finalized FROM sessions WHERE escrow_id = ?`, escrowID,
	)
	var nonce uint64
	if err := row.Scan(&nonce); err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("session %s not found", escrowID)
		}
		return 0, err
	}
	return nonce, nil
}

func (s *SQLite) SaveSnapshot(escrowID string, nonce uint64, data []byte) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	_, err = p.writeDB.Exec(
		`INSERT INTO snapshots (escrow_id, nonce, state_data, created_at)
		 VALUES (?, ?, ?, strftime('%s','now'))
		 ON CONFLICT(escrow_id) DO UPDATE SET nonce = excluded.nonce, state_data = excluded.state_data, created_at = excluded.created_at
		 WHERE snapshots.nonce <= excluded.nonce`,
		escrowID, nonce, data,
	)
	return err
}

func (s *SQLite) LoadSnapshot(escrowID string) (uint64, []byte, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return 0, nil, err
	}
	row := p.readDB.QueryRow(`SELECT nonce, state_data FROM snapshots WHERE escrow_id = ?`, escrowID)
	var nonce uint64
	var data []byte
	if err := row.Scan(&nonce, &data); err != nil {
		if err == sql.ErrNoRows {
			return 0, nil, ErrSnapshotNotFound
		}
		return 0, nil, err
	}
	return nonce, data, nil
}

func (s *SQLite) InsertSealedInference(escrowID string, row InferenceRow) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	obsPresent := 0
	if row.ObsPresent {
		obsPresent = 1
	}
	_, err = p.writeDB.Exec(
		`INSERT INTO sealed_inferences (
			escrow_id, inference_id, sealed_nonce,
			obs_present, sealed_status, sealed_executor_slot,
			sealed_votes_valid, sealed_votes_invalid, sealed_validated_by,
			sealed_model, sealed_prompt_hash, sealed_response_hash,
			sealed_input_length, sealed_max_tokens,
			sealed_input_tokens, sealed_output_tokens,
			sealed_reserved_cost, sealed_actual_cost,
			sealed_started_at, sealed_confirmed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(escrow_id, inference_id) DO UPDATE SET
			sealed_nonce = excluded.sealed_nonce,
			obs_present = excluded.obs_present,
			sealed_status = excluded.sealed_status,
			sealed_executor_slot = excluded.sealed_executor_slot,
			sealed_votes_valid = excluded.sealed_votes_valid,
			sealed_votes_invalid = excluded.sealed_votes_invalid,
			sealed_validated_by = excluded.sealed_validated_by,
			sealed_model = excluded.sealed_model,
			sealed_prompt_hash = excluded.sealed_prompt_hash,
			sealed_response_hash = excluded.sealed_response_hash,
			sealed_input_length = excluded.sealed_input_length,
			sealed_max_tokens = excluded.sealed_max_tokens,
			sealed_input_tokens = excluded.sealed_input_tokens,
			sealed_output_tokens = excluded.sealed_output_tokens,
			sealed_reserved_cost = excluded.sealed_reserved_cost,
			sealed_actual_cost = excluded.sealed_actual_cost,
			sealed_started_at = excluded.sealed_started_at,
			sealed_confirmed_at = excluded.sealed_confirmed_at`,
		escrowID, row.InferenceID, row.SealedNonce,
		obsPresent, row.SealedStatus, row.SealedExecutorSlot,
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

func (s *SQLite) GetSealedInference(escrowID string, inferenceID uint64) (InferenceRow, bool, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return InferenceRow{}, false, err
	}
	row := p.readDB.QueryRow(
		`SELECT sealed_nonce, obs_present, sealed_status, sealed_executor_slot,
		        sealed_votes_valid, sealed_votes_invalid, sealed_validated_by,
		        sealed_model, sealed_prompt_hash, sealed_response_hash,
		        sealed_input_length, sealed_max_tokens,
		        sealed_input_tokens, sealed_output_tokens,
		        sealed_reserved_cost, sealed_actual_cost,
		        sealed_started_at, sealed_confirmed_at
		   FROM sealed_inferences
		  WHERE escrow_id = ? AND inference_id = ?`,
		escrowID, inferenceID,
	)
	out := InferenceRow{InferenceID: inferenceID}
	var obsPresent int
	if err := row.Scan(
		&out.SealedNonce, &obsPresent, &out.SealedStatus, &out.SealedExecutorSlot,
		&out.SealedVotesValid, &out.SealedVotesInvalid, &out.SealedValidatedBy,
		&out.SealedModel, &out.SealedPromptHash, &out.SealedResponseHash,
		&out.SealedInputLength, &out.SealedMaxTokens,
		&out.SealedInputTokens, &out.SealedOutputTokens,
		&out.SealedReservedCost, &out.SealedActualCost,
		&out.SealedStartedAt, &out.SealedConfirmedAt,
	); err != nil {
		if err == sql.ErrNoRows {
			return InferenceRow{}, false, nil
		}
		return InferenceRow{}, false, err
	}
	out.ObsPresent = obsPresent != 0
	return out, true, nil
}

func (s *SQLite) DeleteSealedInferences(escrowID string) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	if _, err := p.writeDB.Exec(`DELETE FROM sealed_inferences WHERE escrow_id = ?`, escrowID); err != nil {
		return fmt.Errorf("delete sealed inferences: %w", err)
	}
	if _, err := p.writeDB.Exec(`DELETE FROM sealed_validation_obs WHERE escrow_id = ?`, escrowID); err != nil {
		return fmt.Errorf("delete sealed validation obs: %w", err)
	}
	return nil
}

const sqliteValidationObsBatchChunk = 100

func (s *SQLite) RecordValidationsAppliedOnce(escrowID string, entries []ValidationObsEntry) error {
	if len(entries) == 0 {
		return nil
	}
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	for start := 0; start < len(entries); start += sqliteValidationObsBatchChunk {
		end := start + sqliteValidationObsBatchChunk
		if end > len(entries) {
			end = len(entries)
		}
		chunk := entries[start:end]
		query := `INSERT INTO inference_validation_obs (escrow_id, inference_id, slot_id, required_validations, completed_validations) VALUES `
		args := make([]any, 0, len(chunk)*3)
		for i, e := range chunk {
			if i > 0 {
				query += ", "
			}
			query += "(?, ?, ?, 1, 1)"
			args = append(args, escrowID, e.InferenceID, e.SlotID)
		}
		query += " ON CONFLICT(escrow_id, inference_id, slot_id) DO NOTHING"
		if _, err := p.writeDB.Exec(query, args...); err != nil {
			return fmt.Errorf("record validations applied once: %w", err)
		}
	}
	return nil
}

func (s *SQLite) DrainInferenceValidationObs(escrowID string, inferenceID uint64) error {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return err
	}
	tx, err := p.writeDB.Begin()
	if err != nil {
		return fmt.Errorf("drain inference validation obs begin: %w", err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(
		`SELECT slot_id, required_validations, completed_validations
		   FROM inference_validation_obs
		  WHERE escrow_id = ? AND inference_id = ?`,
		escrowID, inferenceID,
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
		if _, err := tx.Exec(
			`INSERT INTO sealed_validation_obs (escrow_id, inference_id, slot_id, required_validations, completed_validations)
			 VALUES (?, ?, ?, ?, ?)
			 ON CONFLICT(escrow_id, inference_id, slot_id) DO UPDATE SET
			   required_validations = sealed_validation_obs.required_validations + excluded.required_validations,
			   completed_validations = sealed_validation_obs.completed_validations + excluded.completed_validations`,
			escrowID, inferenceID, r.slotID, r.required, r.completed,
		); err != nil {
			return fmt.Errorf("drain inference validation obs insert: %w", err)
		}
	}
	if _, err := tx.Exec(
		`DELETE FROM inference_validation_obs WHERE escrow_id = ? AND inference_id = ?`,
		escrowID, inferenceID,
	); err != nil {
		return fmt.Errorf("drain inference validation obs delete: %w", err)
	}
	return tx.Commit()
}

func (s *SQLite) GetValidationObservability(escrowID string) ([]SlotValidationObs, error) {
	p, _, err := s.poolFor(escrowID)
	if err != nil {
		return nil, err
	}
	rows, err := p.readDB.Query(
		`SELECT slot_id,
		        SUM(required_validations) AS required_validations,
		        SUM(completed_validations) AS completed_validations
		   FROM (
		     SELECT slot_id, required_validations, completed_validations
		       FROM inference_validation_obs
		      WHERE escrow_id = ?
		     UNION ALL
		     SELECT slot_id, required_validations, completed_validations
		       FROM sealed_validation_obs
		      WHERE escrow_id = ?
		   )
		  GROUP BY slot_id
		  ORDER BY slot_id`,
		escrowID, escrowID,
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

// PruneEpoch closes the pool for epochID, removes the database file and its
// WAL/shm sidecars, drops every escrow_id index entry that pointed at it
// from the in-memory cache, and deletes the matching rows from _meta.db.
// No-op if the epoch is unknown.
func (s *SQLite) PruneEpoch(epochID uint64) error {
	s.mu.Lock()
	p, ok := s.pools[epochID]
	if ok {
		delete(s.pools, epochID)
	}
	s.mu.Unlock()

	if ok {
		if err := p.close(); err != nil {
			return fmt.Errorf("close epoch %d pool: %w", epochID, err)
		}
	}

	dbPath := s.epochFilePath(epochID)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		path := dbPath + suffix
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	if _, err := s.metaDB.Exec(`DELETE FROM escrow_epoch WHERE epoch_id = ?`, epochID); err != nil {
		return fmt.Errorf("prune meta index for epoch %d: %w", epochID, err)
	}
	s.mu.Lock()
	for esc, ep := range s.escrowIdx {
		if ep == epochID {
			delete(s.escrowIdx, esc)
		}
	}
	s.mu.Unlock()
	return nil
}

func (s *SQLite) pruneBefore(cutoff uint64) error {
	if cutoff == 0 {
		return nil
	}

	type poolToClose struct {
		epochID uint64
		pool    *epochPool
	}
	var pools []poolToClose

	s.mu.Lock()
	for epochID, p := range s.pools {
		if epochID < cutoff {
			pools = append(pools, poolToClose{epochID: epochID, pool: p})
			delete(s.pools, epochID)
		}
	}
	s.mu.Unlock()

	epochs := make(map[uint64]struct{}, len(pools))
	var firstCloseErr error
	for _, item := range pools {
		epochs[item.epochID] = struct{}{}
		if err := item.pool.close(); err != nil {
			if firstCloseErr == nil {
				firstCloseErr = fmt.Errorf("close epoch %d pool: %w", item.epochID, err)
			}
		}
	}

	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		return fmt.Errorf("read base dir %s: %w", s.baseDir, err)
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		m := epochFileRegex.FindStringSubmatch(ent.Name())
		if m == nil {
			continue
		}
		epochID, err := strconv.ParseUint(m[1], 10, 64)
		if err != nil || epochID >= cutoff {
			continue
		}
		epochs[epochID] = struct{}{}
	}

	for epochID := range epochs {
		dbPath := s.epochFilePath(epochID)
		for _, suffix := range []string{"", "-wal", "-shm"} {
			path := dbPath + suffix
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", path, err)
			}
		}
	}

	if _, err := s.metaDB.Exec(`DELETE FROM escrow_epoch WHERE epoch_id < ?`, cutoff); err != nil {
		return fmt.Errorf("prune meta index before epoch %d: %w", cutoff, err)
	}
	s.mu.Lock()
	for esc, ep := range s.escrowIdx {
		if ep < cutoff {
			delete(s.escrowIdx, esc)
		}
	}
	s.mu.Unlock()
	if firstCloseErr != nil {
		return firstCloseErr
	}
	return nil
}

// marshalTxs serializes a slice of DevshardTx into a single proto blob
// by wrapping them in DiffContent (reusing the existing proto message).
func marshalTxs(txs []*types.DevshardTx) ([]byte, error) {
	wrapper := &types.DiffContent{Txs: txs}
	data, err := proto.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal txs: %w", err)
	}
	return data, nil
}

// unmarshalTxs deserializes a proto blob back into DevshardTx slice.
func unmarshalTxs(data []byte) ([]*types.DevshardTx, error) {
	if len(data) == 0 {
		return nil, nil
	}
	wrapper := &types.DiffContent{}
	if err := proto.Unmarshal(data, wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal txs: %w", err)
	}
	return wrapper.Txs, nil
}
