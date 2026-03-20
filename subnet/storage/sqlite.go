package storage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"google.golang.org/protobuf/proto"

	"subnet/types"

	_ "modernc.org/sqlite"
)

// SQLite implements Storage using a SQLite database.
// It maintains separate connection pools for reads and writes so that
// concurrent readers never block on a write (WAL mode allows this).
type SQLite struct {
	writeDB *sql.DB // MaxOpenConns=1, serializes writes
	readDB  *sql.DB // MaxOpenConns=N, parallel reads via WAL
}

// NewSQLite opens (or creates) a SQLite database at dbPath and initializes the schema.
// Two connection pools are created on the same file: one for writes (single conn)
// and one for reads (up to 10 conns). WAL mode allows readers to proceed without
// blocking on an active write transaction.
func NewSQLite(dbPath string) (*SQLite, error) {
	openAndConfigure := func(maxConns int) (*sql.DB, error) {
		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
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

	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		escrow_id       TEXT PRIMARY KEY,
		creator_addr    TEXT NOT NULL,
		config_json     TEXT NOT NULL,
		group_json      TEXT NOT NULL,
		initial_balance INTEGER NOT NULL,
		latest_nonce    INTEGER NOT NULL DEFAULT 0,
		last_finalized  INTEGER NOT NULL DEFAULT 0,
		status          TEXT NOT NULL DEFAULT 'active',
		settled_at      INTEGER
	);

	CREATE TABLE IF NOT EXISTS diffs (
		escrow_id       TEXT NOT NULL,
		nonce           INTEGER NOT NULL,
		txs_proto       BLOB NOT NULL,
		user_sig        BLOB,
		post_state_root BLOB,
		state_hash      BLOB,
		warm_keys_json  TEXT,
		created_at      INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (escrow_id, nonce)
	);

	CREATE TABLE IF NOT EXISTS signatures (
		escrow_id TEXT NOT NULL,
		nonce     INTEGER NOT NULL,
		slot_id   INTEGER NOT NULL,
		sig       BLOB NOT NULL,
		PRIMARY KEY (escrow_id, nonce, slot_id)
	);
	`
	if _, err := writeDB.Exec(schema); err != nil {
		writeDB.Close()
		readDB.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	return &SQLite{writeDB: writeDB, readDB: readDB}, nil
}

// Close closes both connection pools.
func (s *SQLite) Close() error {
	wErr := s.writeDB.Close()
	rErr := s.readDB.Close()
	if wErr != nil {
		return wErr
	}
	return rErr
}

func (s *SQLite) CreateSession(params CreateSessionParams) error {
	configJSON, err := json.Marshal(params.Config)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	groupJSON, err := json.Marshal(params.Group)
	if err != nil {
		return fmt.Errorf("marshal group: %w", err)
	}

	_, err = s.writeDB.Exec(
		`INSERT OR IGNORE INTO sessions (escrow_id, creator_addr, config_json, group_json, initial_balance)
		 VALUES (?, ?, ?, ?, ?)`,
		params.EscrowID, params.CreatorAddr, string(configJSON), string(groupJSON), params.InitialBalance,
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

func (s *SQLite) MarkSettled(escrowID string) error {
	res, err := s.writeDB.Exec(
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

func (s *SQLite) ListActiveSessions() ([]string, error) {
	rows, err := s.readDB.Query(`SELECT escrow_id FROM sessions WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		result = append(result, id)
	}
	return result, rows.Err()
}

func (s *SQLite) AppendDiff(escrowID string, rec types.DiffRecord) error {
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

	tx, err := s.writeDB.Begin()
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

	// Insert signatures.
	for slotID, sig := range rec.Signatures {
		_, err = tx.Exec(
			`INSERT OR REPLACE INTO signatures (escrow_id, nonce, slot_id, sig) VALUES (?, ?, ?, ?)`,
			escrowID, rec.Nonce, slotID, sig,
		)
		if err != nil {
			return fmt.Errorf("insert sig: %w", err)
		}
	}

	// Update latest_nonce.
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
	_, err := s.writeDB.Exec(
		`INSERT OR REPLACE INTO signatures (escrow_id, nonce, slot_id, sig) VALUES (?, ?, ?, ?)`,
		escrowID, nonce, slotID, sig,
	)
	return err
}

func (s *SQLite) GetSignatures(escrowID string, nonce uint64) (map[uint32][]byte, error) {
	rows, err := s.readDB.Query(
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
	row := s.readDB.QueryRow(
		`SELECT escrow_id, creator_addr, config_json, group_json, initial_balance, latest_nonce, last_finalized, status
		 FROM sessions WHERE escrow_id = ?`,
		escrowID,
	)

	var meta SessionMeta
	var configJSON, groupJSON string
	err := row.Scan(
		&meta.EscrowID, &meta.CreatorAddr, &configJSON, &groupJSON,
		&meta.InitialBalance, &meta.LatestNonce, &meta.LastFinalized, &meta.Status,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("%w: %s", ErrSessionNotFound, escrowID)
		}
		return nil, err
	}

	if err := json.Unmarshal([]byte(configJSON), &meta.Config); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	if err := json.Unmarshal([]byte(groupJSON), &meta.Group); err != nil {
		return nil, fmt.Errorf("unmarshal group: %w", err)
	}

	return &meta, nil
}

func (s *SQLite) GetDiffs(escrowID string, fromNonce, toNonce uint64) ([]types.DiffRecord, error) {
	rows, err := s.readDB.Query(
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

	// Group rows by nonce.
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
			// Flush previous record.
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

		// Add signature if present (LEFT JOIN may produce NULL).
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
	res, err := s.writeDB.Exec(
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
	row := s.readDB.QueryRow(
		`SELECT last_finalized FROM sessions WHERE escrow_id = ?`, escrowID,
	)
	var nonce uint64
	err := row.Scan(&nonce)
	if err != nil {
		if err == sql.ErrNoRows {
			return 0, fmt.Errorf("session %s not found", escrowID)
		}
		return 0, err
	}
	return nonce, nil
}

// marshalTxs serializes a slice of SubnetTx into a single proto blob
// by wrapping them in DiffContent (reusing the existing proto message).
func marshalTxs(txs []*types.SubnetTx) ([]byte, error) {
	wrapper := &types.DiffContent{Txs: txs}
	data, err := proto.Marshal(wrapper)
	if err != nil {
		return nil, fmt.Errorf("marshal txs: %w", err)
	}
	return data, nil
}

// unmarshalTxs deserializes a proto blob back into SubnetTx slice.
func unmarshalTxs(data []byte) ([]*types.SubnetTx, error) {
	if len(data) == 0 {
		return nil, nil
	}
	wrapper := &types.DiffContent{}
	if err := proto.Unmarshal(data, wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal txs: %w", err)
	}
	return wrapper.Txs, nil
}
