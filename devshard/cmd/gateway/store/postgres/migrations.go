package postgres

const createAPIKeysTable = `CREATE TABLE IF NOT EXISTS api_keys (
		id TEXT COLLATE "C" PRIMARY KEY,
		key TEXT NOT NULL,
		label TEXT NOT NULL DEFAULT '',
		created_at TEXT COLLATE "C" NOT NULL,
		revoked_at TEXT NOT NULL DEFAULT ''
	);`

var migrations = []string{
	`CREATE TABLE IF NOT EXISTS config_overrides (
		id BIGINT PRIMARY KEY CHECK (id = 1),
		overrides_json TEXT NOT NULL,
		updated_at TEXT NOT NULL DEFAULT (` + nowExpression + `)
	);
	CREATE TABLE IF NOT EXISTS devshards (
		escrow_id TEXT COLLATE "C" PRIMARY KEY,
		private_key_env TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL,
		active BIGINT NOT NULL DEFAULT 1,
		rotation_role TEXT NOT NULL DEFAULT 'regular',
		rotation_epoch BIGINT NOT NULL DEFAULT 0,
		settlement_pending BIGINT NOT NULL DEFAULT 0,
		settle_tx_hash TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT (` + nowExpression + `),
		updated_at TEXT NOT NULL DEFAULT (` + nowExpression + `)
	);`,
	`CREATE TABLE IF NOT EXISTS escrow_rotation_commitments (
		tx_hash TEXT COLLATE "C" PRIMARY KEY,
		model TEXT NOT NULL,
		role TEXT NOT NULL DEFAULT '',
		private_key_env TEXT NOT NULL DEFAULT '',
		epoch BIGINT NOT NULL DEFAULT 0,
		block_height BIGINT NOT NULL DEFAULT 0,
		created_at TEXT COLLATE "C" NOT NULL
	);
	CREATE TABLE IF NOT EXISTS gateway_rotation_status (
		model TEXT COLLATE "C" NOT NULL,
		role TEXT COLLATE "C" NOT NULL DEFAULT '',
		stage TEXT NOT NULL DEFAULT '',
		epoch BIGINT NOT NULL DEFAULT 0,
		completed BIGINT NOT NULL DEFAULT 0,
		create_error TEXT NOT NULL DEFAULT '',
		updated_at TEXT NOT NULL,
		PRIMARY KEY (model, role)
	);`,
	`CREATE TABLE IF NOT EXISTS request_accounting (
		request_id TEXT COLLATE "C" PRIMARY KEY,
		escrow_id TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		outcome TEXT NOT NULL DEFAULT '',
		decision TEXT NOT NULL DEFAULT '',
		stream BIGINT NOT NULL DEFAULT 0,
		winner_nonce BIGINT NOT NULL DEFAULT 0,
		winner_participant TEXT NOT NULL DEFAULT '',
		winner_host TEXT NOT NULL DEFAULT '',
		winner_host_idx BIGINT NOT NULL DEFAULT 0,
		attempts BIGINT NOT NULL DEFAULT 0,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		winner_output_tokens BIGINT NOT NULL DEFAULT 0,
		total_output_tokens BIGINT NOT NULL DEFAULT 0,
		escrow_missing BIGINT NOT NULL DEFAULT 0,
		balance_exhausted BIGINT NOT NULL DEFAULT 0,
		started_at TEXT NOT NULL DEFAULT '',
		completed_at TEXT NOT NULL DEFAULT '',
		first_token_ms BIGINT NOT NULL DEFAULT 0,
		duration_ms BIGINT NOT NULL DEFAULT 0,
		recorded_at TEXT COLLATE "C" NOT NULL
	);
	CREATE INDEX IF NOT EXISTS request_accounting_recorded_at ON request_accounting (recorded_at);`,
	`CREATE TABLE IF NOT EXISTS suspicious_hosts (
		participant_key TEXT COLLATE "C" PRIMARY KEY,
		created_at TEXT NOT NULL DEFAULT (` + nowExpression + `)
	);`,
	`ALTER TABLE devshards ADD COLUMN IF NOT EXISTS route_prefix TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE devshards ADD COLUMN IF NOT EXISTS settle_tx_at TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE request_accounting ADD COLUMN IF NOT EXISTS prompt_tokens BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN IF NOT EXISTS chain_epoch BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN IF NOT EXISTS amount BIGINT NOT NULL DEFAULT 0`,
	`ALTER TABLE devshards ADD COLUMN IF NOT EXISTS gone_from_chain BIGINT NOT NULL DEFAULT 0`,
	`CREATE TABLE IF NOT EXISTS model_filters (
		model TEXT COLLATE "C" PRIMARY KEY,
		preset TEXT NOT NULL,
		filters_json TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);`,
	`ALTER TABLE request_accounting ADD COLUMN IF NOT EXISTS input_tokens_source TEXT NOT NULL DEFAULT 'bytes_estimate'`,
	`CREATE TABLE IF NOT EXISTS owed_refusal_votes (
		escrow_id TEXT COLLATE "C" NOT NULL,
		nonce BIGINT NOT NULL,
		started_at TEXT NOT NULL,
		request_id TEXT NOT NULL DEFAULT '',
		participant TEXT NOT NULL DEFAULT '',
		model TEXT NOT NULL DEFAULT '',
		prompt BYTEA NOT NULL,
		PRIMARY KEY (escrow_id, nonce)
	);`,
	createAPIKeysTable,
	`CREATE TABLE IF NOT EXISTS inference_completions (
		escrow_id TEXT NOT NULL,
		nonce BIGINT NOT NULL,
		entry_json BYTEA NOT NULL,
		PRIMARY KEY (escrow_id, nonce)
	);`,
	createAPIKeysTable,
}
