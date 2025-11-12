package apiconfig

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

// SqliteConfig holds configuration for an embedded SQLite DB
type SqliteConfig struct {
	Path string // e.g., gonka.db
}

type SqlDatabase interface {
	BootstrapLocal(ctx context.Context) error
	GetDb() *sql.DB
}

type SqliteDb struct {
	config SqliteConfig
	db     *sql.DB
}

func NewSQLiteDb(cfg SqliteConfig) *SqliteDb {
	return &SqliteDb{config: cfg}
}

func (d *SqliteDb) BootstrapLocal(ctx context.Context) error {
	db, err := OpenSQLite(d.config)
	if err != nil {
		return err
	}
	if err := EnsureSchema(ctx, db); err != nil {
		_ = db.Close()
		return err
	}
	d.db = db
	return nil
}

func (d *SqliteDb) GetDb() *sql.DB { return d.db }

// OpenSQLite opens an embedded SQLite database (in process)
func OpenSQLite(cfg SqliteConfig) (*sql.DB, error) {
	if cfg.Path == "" {
		return nil, errors.New("sqlite path is empty")
	}
	db, err := sql.Open("sqlite", cfg.Path)
	if err != nil {
		return nil, err
	}
	// Reasonable pool defaults for sqlite
	db.SetMaxOpenConns(1) // SQLite is single-writer
	db.SetConnMaxLifetime(0)
	db.SetMaxIdleConns(1)

	// Improve durability and reduce lock errors in long-running process
	// Enable WAL; if it fails, return error (not optional for our usage)
	if _, err := db.ExecContext(context.Background(), "PRAGMA journal_mode=WAL;"); err != nil {
		return nil, err
	}
	// Reasonable defaults; ignore failure as they are optional
	_, _ = db.ExecContext(context.Background(), "PRAGMA synchronous=NORMAL;")
	_, _ = db.ExecContext(context.Background(), "PRAGMA busy_timeout=5000;")
	return db, nil
}

// EnsureSchema creates the minimal tables for storing dynamic config: inference nodes.
func EnsureSchema(ctx context.Context, db *sql.DB) error {
	stmt := `
CREATE TABLE IF NOT EXISTS inference_nodes (
  id TEXT PRIMARY KEY,
  host TEXT NOT NULL,
  inference_segment TEXT NOT NULL,
  inference_port INTEGER NOT NULL,
  poc_segment TEXT NOT NULL,
  poc_port INTEGER NOT NULL,
  max_concurrent INTEGER NOT NULL,
  models_json TEXT NOT NULL,
  hardware_json TEXT NOT NULL,
  base_url TEXT NOT NULL DEFAULT '',
  auth_token TEXT NOT NULL DEFAULT '',
  updated_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now')),
  created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS kv_config (
  key TEXT PRIMARY KEY,
  value_json TEXT NOT NULL,
  updated_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now')),
  created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
);

CREATE TABLE IF NOT EXISTS seed_info (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  type TEXT NOT NULL, -- 'current', 'previous', 'upcoming'
  seed INTEGER NOT NULL,
  epoch_index INTEGER NOT NULL,
  signature TEXT NOT NULL,
  claimed BOOLEAN NOT NULL DEFAULT 0,
  is_active BOOLEAN NOT NULL DEFAULT 1,
  created_at DATETIME NOT NULL DEFAULT (STRFTIME('%Y-%m-%d %H:%M:%f','now'))
);`
	if _, err := db.ExecContext(ctx, stmt); err != nil {
		return err
	}

	// Migrate existing tables: add base_url and auth_token columns if they don't exist
	return migrateInferenceNodesTable(ctx, db)
}

// migrateInferenceNodesTable adds base_url and auth_token columns to existing inference_nodes table
func migrateInferenceNodesTable(ctx context.Context, db *sql.DB) error {
	// Check if base_url column exists
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(inference_nodes)")
	if err != nil {
		return err
	}
	defer rows.Close()

	var hasBaseURL, hasAuthToken bool
	for rows.Next() {
		var (
			cid      int
			name     string
			declType string
			notnull  int
			dflt     sql.NullString
			pk       int
		)
		if err := rows.Scan(&cid, &name, &declType, &notnull, &dflt, &pk); err != nil {
			return err
		}
		if name == "base_url" {
			hasBaseURL = true
		}
		if name == "auth_token" {
			hasAuthToken = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	// Add base_url column if it doesn't exist
	if !hasBaseURL {
		if _, err := db.ExecContext(ctx, "ALTER TABLE inference_nodes ADD COLUMN base_url TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add base_url column: %w", err)
		}
	}

	// Add auth_token column if it doesn't exist
	if !hasAuthToken {
		if _, err := db.ExecContext(ctx, "ALTER TABLE inference_nodes ADD COLUMN auth_token TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("failed to add auth_token column: %w", err)
		}
	}

	return nil
}

// UpsertInferenceNodes replaces or inserts the given nodes by id.
func UpsertInferenceNodes(ctx context.Context, db *sql.DB, nodes []InferenceNodeConfig) error {
	if len(nodes) == 0 {
		return nil
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := `
INSERT INTO inference_nodes (
  id, host, inference_segment, inference_port, poc_segment, poc_port, max_concurrent, models_json, hardware_json, base_url, auth_token
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
  host = excluded.host,
  inference_segment = excluded.inference_segment,
  inference_port = excluded.inference_port,
  poc_segment = excluded.poc_segment,
  poc_port = excluded.poc_port,
  max_concurrent = excluded.max_concurrent,
  models_json = excluded.models_json,
  hardware_json = excluded.hardware_json,
  base_url = excluded.base_url,
  auth_token = excluded.auth_token,
  updated_at = (STRFTIME('%Y-%m-%d %H:%M:%f','now'))`

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, n := range nodes {
		modelsJSON, err := json.Marshal(n.Models)
		if err != nil {
			return err
		}
		hardwareJSON, err := json.Marshal(n.Hardware)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(
			ctx,
			n.Id,
			n.Host,
			n.InferenceSegment,
			n.InferencePort,
			n.PoCSegment,
			n.PoCPort,
			n.MaxConcurrent,
			string(modelsJSON),
			string(hardwareJSON),
			n.BaseURL,
			n.AuthToken,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// WriteNodes is a convenience wrapper for UpsertInferenceNodes.
func WriteNodes(ctx context.Context, db *sql.DB, nodes []InferenceNodeConfig) error {
	return UpsertInferenceNodes(ctx, db, nodes)
}

// ReadNodes reads all nodes from the database and reconstructs InferenceNodeConfig entries.
func ReadNodes(ctx context.Context, db *sql.DB) ([]InferenceNodeConfig, error) {
	rows, err := db.QueryContext(ctx, `
SELECT id, host, inference_segment, inference_port, poc_segment, poc_port, max_concurrent, models_json, hardware_json, base_url, auth_token
FROM inference_nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []InferenceNodeConfig
	for rows.Next() {
		var (
			id          string
			host        string
			infSeg      string
			infPort     int
			pocSeg      string
			pocPort     int
			maxConc     int
			modelsRaw   []byte
			hardwareRaw []byte
			baseURL     string
			authToken   string
		)
		if err := rows.Scan(&id, &host, &infSeg, &infPort, &pocSeg, &pocPort, &maxConc, &modelsRaw, &hardwareRaw, &baseURL, &authToken); err != nil {
			return nil, err
		}
		var models map[string]ModelConfig
		if len(modelsRaw) > 0 {
			if err := json.Unmarshal(modelsRaw, &models); err != nil {
				return nil, err
			}
		}
		var hardware []Hardware
		if len(hardwareRaw) > 0 {
			if err := json.Unmarshal(hardwareRaw, &hardware); err != nil {
				return nil, err
			}
		}
		out = append(out, InferenceNodeConfig{
			Host:             host,
			InferenceSegment: infSeg,
			InferencePort:    infPort,
			PoCSegment:       pocSeg,
			PoCPort:          pocPort,
			BaseURL:          baseURL,
			AuthToken:        authToken,
			Models:           models,
			Id:               id,
			MaxConcurrent:    maxConc,
			Hardware:         hardware,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReplaceInferenceNodes deletes all nodes and inserts the given list atomically.
func ReplaceInferenceNodes(ctx context.Context, db *sql.DB, nodes []InferenceNodeConfig) error {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM inference_nodes`); err != nil {
		return err
	}

	if len(nodes) == 0 {
		return tx.Commit()
	}

	q := `
INSERT INTO inference_nodes (
  id, host, inference_segment, inference_port, poc_segment, poc_port, max_concurrent, models_json, hardware_json, base_url, auth_token
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, n := range nodes {
		modelsJSON, err := json.Marshal(n.Models)
		if err != nil {
			return err
		}
		hardwareJSON, err := json.Marshal(n.Hardware)
		if err != nil {
			return err
		}
		if _, err := stmt.ExecContext(
			ctx,
			n.Id,
			n.Host,
			n.InferenceSegment,
			n.InferencePort,
			n.PoCSegment,
			n.PoCPort,
			n.MaxConcurrent,
			string(modelsJSON),
			string(hardwareJSON),
			n.BaseURL,
			n.AuthToken,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SeedInfo typed accessors

// SetActiveSeed deactivates previous active seed of given type and inserts a new active row.
func SetActiveSeed(ctx context.Context, db *sql.DB, seedType string, info SeedInfo) error {
	if db == nil {
		return errors.New("db is nil")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `UPDATE seed_info SET is_active = 0 WHERE type = ? AND is_active = 1`, seedType); err != nil {
		return err
	}
	q := `INSERT INTO seed_info(type, seed, epoch_index, signature, claimed, is_active) VALUES(?, ?, ?, ?, ?, 1)`
	if _, err := tx.ExecContext(ctx, q, seedType, info.Seed, info.EpochIndex, info.Signature, info.Claimed); err != nil {
		return err
	}
	return tx.Commit()
}

// GetActiveSeed returns the active seed for type; ok=false if none.
func GetActiveSeed(ctx context.Context, db *sql.DB, seedType string) (SeedInfo, bool, error) {
	if db == nil {
		return SeedInfo{}, false, errors.New("db is nil")
	}
	row := db.QueryRowContext(ctx, `SELECT seed, epoch_index, signature, claimed FROM seed_info WHERE type = ? AND is_active = 1 ORDER BY id DESC LIMIT 1`, seedType)
	var s SeedInfo
	if err := row.Scan(&s.Seed, &s.EpochIndex, &s.Signature, &s.Claimed); err != nil {
		if err == sql.ErrNoRows {
			return SeedInfo{}, false, nil
		}
		return SeedInfo{}, false, err
	}
	return s, true, nil
}

// MarkSeedClaimed sets claimed=true for current active seed of given type. ok=false if none.
func MarkSeedClaimed(ctx context.Context, db *sql.DB, seedType string) (ok bool, err error) {
	if db == nil {
		return false, errors.New("db is nil")
	}
	res, err := db.ExecContext(ctx, `UPDATE seed_info SET claimed = 1 WHERE type = ? AND is_active = 1`, seedType)
	if err != nil {
		return false, err
	}
	affected, _ := res.RowsAffected()
	return affected > 0, nil
}

// IsSeedClaimed reads claimed for active seed of given type. ok=false if none.
func IsSeedClaimed(ctx context.Context, db *sql.DB, seedType string) (claimed bool, ok bool, err error) {
	if db == nil {
		return false, false, errors.New("db is nil")
	}
	row := db.QueryRowContext(ctx, `SELECT claimed FROM seed_info WHERE type = ? AND is_active = 1 ORDER BY id DESC LIMIT 1`, seedType)
	var c bool
	if err := row.Scan(&c); err != nil {
		if err == sql.ErrNoRows {
			return false, false, nil
		}
		return false, false, err
	}
	return c, true, nil
}

// ExportAllDb returns a JSON-friendly dump of all user tables in the database.
// It introspects table schemas and converts values into Go primitives suitable for JSON encoding.
func ExportAllDb(ctx context.Context, db *sql.DB) (map[string]any, error) {
	tables, err := listUserTables(ctx, db)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(tables))
	for _, t := range tables {
		rows, err := dumpTable(ctx, db, t)
		if err != nil {
			return nil, fmt.Errorf("dump table %s: %w", t, err)
		}
		out[t] = rows
	}
	return out, nil
}

func listUserTables(ctx context.Context, db *sql.DB) ([]string, error) {
	q := `SELECT name FROM sqlite_schema WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`
	var out []string
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type columnInfo struct {
	name     string
	declType string
}

func getTableColumns(ctx context.Context, db *sql.DB, table string) ([]columnInfo, error) {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var cols []columnInfo
	// pragma table_info returns: cid, name, type, notnull, dflt_value, pk
	for rows.Next() {
		var (
			cid      int
			name     string
			declType string
			notnull  int
			dflt     sql.NullString
			pk       int
		)
		if err := rows.Scan(&cid, &name, &declType, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		cols = append(cols, columnInfo{name: name, declType: declType})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return cols, nil
}

func dumpTable(ctx context.Context, db *sql.DB, table string) ([]map[string]any, error) {
	cols, err := getTableColumns(ctx, db, table)
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return []map[string]any{}, nil
	}
	// Build SELECT with explicit column list for stable order
	colNames := make([]string, len(cols))
	for i, c := range cols {
		colNames[i] = c.name
	}
	q := "SELECT " + strings.Join(colNames, ",") + " FROM " + table
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Prepare scanners based on decl types
	results := make([]map[string]any, 0, 64)
	for rows.Next() {
		// Allocate stable per-column holders to avoid slice reallocation pointer invalidation
		scanHolders := make([]any, len(cols))
		holderKinds := make([]string, len(cols))
		for i, c := range cols {
			t := strings.ToUpper(c.declType)
			switch {
			case strings.Contains(t, "INT") || strings.Contains(t, "BOOL"):
				h := new(sql.NullInt64)
				scanHolders[i] = h
				holderKinds[i] = "int"
			case strings.Contains(t, "REAL") || strings.Contains(t, "FLOA") || strings.Contains(t, "DOUB"):
				h := new(sql.NullFloat64)
				scanHolders[i] = h
				holderKinds[i] = "float"
			case strings.Contains(t, "BLOB"):
				var b []byte
				scanHolders[i] = &b
				holderKinds[i] = "blob"
			default:
				h := new(sql.NullString)
				scanHolders[i] = h
				holderKinds[i] = "text"
			}
		}
		if err := rows.Scan(scanHolders...); err != nil {
			return nil, err
		}
		// reconstruct per row map
		rowMap := make(map[string]any, len(cols))
		for i, c := range cols {
			kind := holderKinds[i]
			switch kind {
			case "int":
				v := scanHolders[i].(*sql.NullInt64)
				if !v.Valid {
					rowMap[c.name] = nil
					break
				}
				// if declared as BOOL* return true/false
				if strings.Contains(strings.ToUpper(c.declType), "BOOL") {
					rowMap[c.name] = v.Int64 != 0
				} else {
					rowMap[c.name] = v.Int64
				}
			case "float":
				v := scanHolders[i].(*sql.NullFloat64)
				if !v.Valid {
					rowMap[c.name] = nil
					break
				}
				rowMap[c.name] = v.Float64
			case "blob":
				b := *(scanHolders[i].(*[]byte))
				if b == nil {
					rowMap[c.name] = nil
					break
				}
				rowMap[c.name] = b // will JSON-encode as base64
			default: // text
				v := scanHolders[i].(*sql.NullString)
				if !v.Valid {
					rowMap[c.name] = nil
					break
				}
				// Special-case kv_config.value_json to decode JSON payload
				if table == "kv_config" && c.name == "value_json" {
					var parsed any
					if err := json.Unmarshal([]byte(v.String), &parsed); err == nil {
						rowMap[c.name] = parsed
						continue
					}
				}
				rowMap[c.name] = v.String
			}
		}
		results = append(results, rowMap)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return results, nil
}

// KV helpers for dynamic config

// KVSetJSON upserts an arbitrary Go value encoded as JSON at the given key.
func KVSetJSON(ctx context.Context, db *sql.DB, key string, value any) error {
	if db == nil {
		return errors.New("db is nil")
	}
	bytes, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	q := `INSERT INTO kv_config(key, value_json) VALUES(?, ?)
ON CONFLICT(key) DO UPDATE SET value_json = excluded.value_json, updated_at = (STRFTIME('%Y-%m-%d %H:%M:%f','now'))`
	if _, err := tx.ExecContext(ctx, q, key, string(bytes)); err != nil {
		return err
	}
	return tx.Commit()
}

// KVGetJSON loads a key and unmarshals JSON into destPtr.
// If key not found, ok=false and no error is returned.
func KVGetJSON(ctx context.Context, db *sql.DB, key string, destPtr any) (ok bool, err error) {
	if db == nil {
		return false, errors.New("db is nil")
	}
	var raw string
	err = db.QueryRowContext(ctx, `SELECT value_json FROM kv_config WHERE key = ?`, key).Scan(&raw)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), destPtr); err != nil {
		return false, fmt.Errorf("unmarshal json for key %s: %w", key, err)
	}
	return true, nil
}

// KVSetInt64 stores an int64 under key.
func KVSetInt64(ctx context.Context, db *sql.DB, key string, v int64) error {
	return KVSetJSON(ctx, db, key, v)
}

// KVGetInt64 retrieves an int64. If missing, returns ok=false.
func KVGetInt64(ctx context.Context, db *sql.DB, key string) (val int64, ok bool, err error) {
	var tmp int64
	ok, err = KVGetJSON(ctx, db, key, &tmp)
	if !ok || err != nil {
		return 0, ok, err
	}
	return tmp, true, nil
}

// KVSetString stores a string under key.
func KVSetString(ctx context.Context, db *sql.DB, key string, v string) error {
	return KVSetJSON(ctx, db, key, v)
}

// KVGetString retrieves a string. If missing, returns ok=false.
func KVGetString(ctx context.Context, db *sql.DB, key string) (val string, ok bool, err error) {
	var tmp string
	ok, err = KVGetJSON(ctx, db, key, &tmp)
	if !ok || err != nil {
		return "", ok, err
	}
	return tmp, true, nil
}
