package main

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type GatewaySettings struct {
	ChainREST               string `json:"chain_rest"`
	PublicAPI               string `json:"public_api"`
	DefaultModel            string `json:"default_model"`
	DefaultRequestMaxTokens uint64 `json:"default_request_max_tokens"`
	MaxConcurrentRequests   int64  `json:"max_concurrent_requests"`
	MaxInputTokensInFlight  int64  `json:"max_input_tokens_in_flight"`
}

type GatewaySubnetState struct {
	RuntimeConfig
	Active    bool   `json:"active"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type GatewayState struct {
	Settings GatewaySettings      `json:"settings"`
	Subnets  []GatewaySubnetState `json:"subnets"`
}

type GatewayStore struct {
	db *sql.DB
}

func NewGatewayStore(path string) (*GatewayStore, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open gateway store: %w", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS gateway_settings (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			chain_rest TEXT NOT NULL,
			public_api TEXT NOT NULL DEFAULT '',
			default_model TEXT NOT NULL,
			default_request_max_tokens INTEGER NOT NULL,
			max_concurrent_requests INTEGER NOT NULL,
			max_input_tokens_in_flight INTEGER NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS gateway_subnets (
			id TEXT PRIMARY KEY,
			private_key_hex TEXT NOT NULL,
			private_key_env TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			storage_path TEXT NOT NULL DEFAULT '',
			active INTEGER NOT NULL DEFAULT 1,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			db.Close()
			return nil, fmt.Errorf("init gateway store: %w", err)
		}
	}
	if err := ensureGatewaySettingsColumn(db, "public_api", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway store: %w", err)
	}
	if err := ensureGatewaySubnetsColumn(db, "protocol_version", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate gateway subnets: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS participant_throttle_state (
		participant_key TEXT PRIMARY KEY,
		tokens REAL NOT NULL DEFAULT 0,
		last_refill_at TEXT NOT NULL,
		last_throttle_status INTEGER NOT NULL DEFAULT 0,
		empty_stream_streak INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("init participant throttle table: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "quarantine_until_utc", "TEXT NOT NULL DEFAULT ''"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle: %w", err)
	}
	if err := ensureColumn(db, "participant_throttle_state", "empty_stream_streak", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate participant throttle streak: %w", err)
	}

	return &GatewayStore{db: db}, nil
}

func (s *GatewayStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *GatewayStore) LoadState() (GatewayState, bool, error) {
	var state GatewayState
	row := s.db.QueryRow(`
		SELECT chain_rest, public_api, default_model, default_request_max_tokens,
		       max_concurrent_requests, max_input_tokens_in_flight
		FROM gateway_settings
		WHERE id = 1`)
	err := row.Scan(
		&state.Settings.ChainREST,
		&state.Settings.PublicAPI,
		&state.Settings.DefaultModel,
		&state.Settings.DefaultRequestMaxTokens,
		&state.Settings.MaxConcurrentRequests,
		&state.Settings.MaxInputTokensInFlight,
	)
	if err == sql.ErrNoRows {
		return GatewayState{}, false, nil
	}
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway settings: %w", err)
	}

	rows, err := s.db.Query(`
		SELECT id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version
		FROM gateway_subnets
		ORDER BY id`)
	if err != nil {
		return GatewayState{}, false, fmt.Errorf("load gateway subnets: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var subnet GatewaySubnetState
		var active int
		if err := rows.Scan(
			&subnet.ID,
			&subnet.PrivateKeyHex,
			&subnet.PrivateKeyEnv,
			&subnet.Model,
			&subnet.StoragePath,
			&active,
			&subnet.CreatedAt,
			&subnet.UpdatedAt,
			&subnet.ProtocolVersion,
		); err != nil {
			return GatewayState{}, false, fmt.Errorf("scan gateway subnet: %w", err)
		}
		subnet.Active = active != 0
		state.Subnets = append(state.Subnets, subnet)
	}
	if err := rows.Err(); err != nil {
		return GatewayState{}, false, fmt.Errorf("iterate gateway subnets: %w", err)
	}
	return state, true, nil
}

func (s *GatewayStore) Initialize(settings GatewaySettings, subnets []GatewaySubnetState) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin gateway init: %w", err)
	}
	defer tx.Rollback()

	var count int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM gateway_settings WHERE id = 1`).Scan(&count); err != nil {
		return fmt.Errorf("count gateway settings: %w", err)
	}
	if count > 0 {
		return nil
	}

	if _, err := tx.Exec(`
		INSERT INTO gateway_settings (
			id, chain_rest, public_api, default_model, default_request_max_tokens,
			max_concurrent_requests, max_input_tokens_in_flight, updated_at
		) VALUES (1, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.MaxConcurrentRequests,
		settings.MaxInputTokensInFlight,
		now,
	); err != nil {
		return fmt.Errorf("insert gateway settings: %w", err)
	}

	for _, subnet := range subnets {
		if err := s.upsertSubnetTx(tx, subnet, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *GatewayStore) UpdateSettings(settings GatewaySettings) error {
	res, err := s.db.Exec(`
		UPDATE gateway_settings
		SET chain_rest = ?,
		    public_api = ?,
		    default_model = ?,
		    default_request_max_tokens = ?,
		    max_concurrent_requests = ?,
		    max_input_tokens_in_flight = ?,
		    updated_at = ?
		WHERE id = 1`,
		strings.TrimSpace(settings.ChainREST),
		strings.TrimSpace(settings.PublicAPI),
		strings.TrimSpace(settings.DefaultModel),
		settings.DefaultRequestMaxTokens,
		settings.MaxConcurrentRequests,
		settings.MaxInputTokensInFlight,
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("update gateway settings: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for gateway settings update: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("gateway settings not initialized")
	}
	return nil
}

func (s *GatewayStore) UpsertSubnet(subnet GatewaySubnetState) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin subnet upsert: %w", err)
	}
	defer tx.Rollback()
	if err := s.upsertSubnetTx(tx, subnet, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *GatewayStore) upsertSubnetTx(tx *sql.Tx, subnet GatewaySubnetState, now string) error {
	createdAt := now
	_ = tx.QueryRow(`SELECT created_at FROM gateway_subnets WHERE id = ?`, subnet.ID).Scan(&createdAt)
	if _, err := tx.Exec(`
		INSERT OR REPLACE INTO gateway_subnets (
			id, private_key_hex, private_key_env, model, storage_path, active, created_at, updated_at, protocol_version
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(subnet.ID),
		strings.TrimSpace(subnet.PrivateKeyHex),
		strings.TrimSpace(subnet.PrivateKeyEnv),
		strings.TrimSpace(subnet.Model),
		strings.TrimSpace(subnet.StoragePath),
		gatewayBoolToInt(subnet.Active),
		createdAt,
		now,
		strings.TrimSpace(subnet.ProtocolVersion),
	); err != nil {
		return fmt.Errorf("upsert gateway subnet %s: %w", subnet.ID, err)
	}
	return nil
}

func (s *GatewayStore) SetSubnetActive(id string, active bool) error {
	res, err := s.db.Exec(`
		UPDATE gateway_subnets
		SET active = ?, updated_at = ?
		WHERE id = ?`,
		gatewayBoolToInt(active),
		time.Now().UTC().Format(time.RFC3339Nano),
		strings.TrimSpace(id),
	)
	if err != nil {
		return fmt.Errorf("update subnet %s active=%t: %w", id, active, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for subnet %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("subnet %s not found", id)
	}
	return nil
}

func (s *GatewayStore) DeleteSubnet(id string) error {
	res, err := s.db.Exec(`DELETE FROM gateway_subnets WHERE id = ?`, strings.TrimSpace(id))
	if err != nil {
		return fmt.Errorf("delete subnet %s: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected for delete subnet %s: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("subnet %s not found", id)
	}
	return nil
}

// ParticipantThrottleRow represents a persisted reactive throttle state for one host.
type ParticipantThrottleRow struct {
	Key               string
	Tokens            float64
	LastRefillAt      time.Time
	Status            int
	QuarantineUntil   time.Time // wall-clock end of unified quarantine; zero if unset
	EmptyStreamStreak int
}

func (s *GatewayStore) SaveParticipantThrottle(key string, tokens float64, lastRefillAt time.Time, status int, quarantineUntil time.Time, emptyStreamStreak int) error {
	if s == nil || s.db == nil {
		return nil
	}
	quarStr := ""
	if !quarantineUntil.IsZero() {
		quarStr = quarantineUntil.UTC().Format(time.RFC3339Nano)
	}
	_, err := s.db.Exec(`
		INSERT OR REPLACE INTO participant_throttle_state
			(participant_key, tokens, last_refill_at, last_throttle_status, quarantine_until_utc, empty_stream_streak)
		VALUES (?, ?, ?, ?, ?, ?)`,
		key, tokens, lastRefillAt.UTC().Format(time.RFC3339Nano), status, quarStr, emptyStreamStreak)
	if err != nil {
		return fmt.Errorf("save participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *GatewayStore) DeleteParticipantThrottle(key string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM participant_throttle_state WHERE participant_key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete participant throttle %s: %w", key, err)
	}
	return nil
}

func (s *GatewayStore) LoadParticipantThrottles() ([]ParticipantThrottleRow, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT participant_key, tokens, last_refill_at, last_throttle_status,
		       IFNULL(empty_stream_streak, 0) AS empty_stream_streak,
		       IFNULL(quarantine_until_utc, '') AS quarantine_until_utc
		FROM participant_throttle_state`)
	if err != nil {
		return nil, fmt.Errorf("load participant throttles: %w", err)
	}
	defer rows.Close()

	var result []ParticipantThrottleRow
	for rows.Next() {
		var row ParticipantThrottleRow
		var lastRefillStr, quarantineStr string
		if err := rows.Scan(&row.Key, &row.Tokens, &lastRefillStr, &row.Status, &row.EmptyStreamStreak, &quarantineStr); err != nil {
			return nil, fmt.Errorf("scan participant throttle: %w", err)
		}
		row.LastRefillAt, err = time.Parse(time.RFC3339Nano, lastRefillStr)
		if err != nil {
			return nil, fmt.Errorf("parse last_refill_at for %s: %w", row.Key, err)
		}
		if strings.TrimSpace(quarantineStr) != "" {
			row.QuarantineUntil, err = time.Parse(time.RFC3339Nano, quarantineStr)
			if err != nil {
				return nil, fmt.Errorf("parse quarantine_until_utc for %s: %w", row.Key, err)
			}
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func gatewayBoolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func ensureGatewaySettingsColumn(db *sql.DB, columnName, columnDDL string) error {
	return ensureColumn(db, "gateway_settings", columnName, columnDDL)
}

func ensureGatewaySubnetsColumn(db *sql.DB, columnName, columnDDL string) error {
	return ensureColumn(db, "gateway_subnets", columnName, columnDDL)
}

func ensureColumn(db *sql.DB, table, columnName, columnDDL string) error {
	rows, err := db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == columnName {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	_, err = db.Exec(fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, columnName, columnDDL))
	return err
}
