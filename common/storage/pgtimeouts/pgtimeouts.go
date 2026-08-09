// Package pgtimeouts is the single source of truth for Postgres client and
// server timeout defaults shared by session storage, payload storage, the
// gateway store, and epoch accounting.
//
// Env-tunable (Go durations, e.g. "2s", "500ms"):
//
//	PG_CONNECT_TIMEOUT   — dial/auth for a new connection (default 2s)
//	PG_OPERATION_TIMEOUT — per-call Go context deadline (default 2s; 0 disables)
//	PG_IMPORT_TIMEOUT    — boot-time SQLite→Postgres import budget (default 5m)
//
// Server-side bounds applied on every pooled connection (not env-tunable):
//
//	statement_timeout = 5s  — abort one SQL statement that runs too long
//	lock_timeout      = 3s  — abort a statement waiting too long for a lock
//
// statement_timeout / lock_timeout are the last-resort guards when
// PG_OPERATION_TIMEOUT=0 disables the Go deadline, and when a plane has no
// SQLite fallback to absorb a stalled backend.
package pgtimeouts

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	EnvConnectTimeout   = "PG_CONNECT_TIMEOUT"
	EnvOperationTimeout = "PG_OPERATION_TIMEOUT"
	EnvImportTimeout    = "PG_IMPORT_TIMEOUT"
)

const (
	DefaultConnectTimeout   = 2 * time.Second
	DefaultOperationTimeout = 2 * time.Second
	// DefaultStatementTimeout aborts a single query server-side.
	DefaultStatementTimeout = 5 * time.Second
	// DefaultLockTimeout bounds waits on row/table locks server-side.
	DefaultLockTimeout = 3 * time.Second
	// DefaultImportTimeout bounds one-shot SQLite→Postgres imports at boot.
	DefaultImportTimeout = 5 * time.Minute
)

// ConnectTimeout reads PG_CONNECT_TIMEOUT. Invalid or non-positive values use
// DefaultConnectTimeout.
func ConnectTimeout() time.Duration {
	return positiveDuration(os.Getenv(EnvConnectTimeout), DefaultConnectTimeout)
}

// OperationTimeout reads PG_OPERATION_TIMEOUT. An invalid or unset value uses
// DefaultOperationTimeout; 0 disables per-operation Go deadlines.
func OperationTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv(EnvOperationTimeout))
	if v == "" {
		return DefaultOperationTimeout
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return DefaultOperationTimeout
	}
	return d
}

// ImportTimeout reads PG_IMPORT_TIMEOUT. Invalid or non-positive values use
// DefaultImportTimeout.
func ImportTimeout() time.Duration {
	return positiveDuration(os.Getenv(EnvImportTimeout), DefaultImportTimeout)
}

// StatementTimeout is the server-side per-statement bound applied to pooled
// connections. It is not env-tunable; change DefaultStatementTimeout to adjust.
func StatementTimeout() time.Duration { return DefaultStatementTimeout }

// LockTimeout is the server-side wait-for-lock bound applied to pooled
// connections. It is not env-tunable; change DefaultLockTimeout to adjust.
func LockTimeout() time.Duration { return DefaultLockTimeout }

// ApplyRuntimeParams sets statement_timeout and lock_timeout on a pgx ConnConfig
// RuntimeParams map (creating it if needed). Call after ParseConfig and before
// NewWithConfig so every pooled connection inherits the bounds.
func ApplyRuntimeParams(params *map[string]string) {
	if params == nil {
		return
	}
	if *params == nil {
		*params = make(map[string]string)
	}
	(*params)["statement_timeout"] = strconv.FormatInt(StatementTimeout().Milliseconds(), 10)
	(*params)["lock_timeout"] = strconv.FormatInt(LockTimeout().Milliseconds(), 10)
}

// ApplyConnConfig sets ConnectTimeout from ConnectTimeout() and the server-side
// statement/lock bounds on cfg. Callers that want a different connect deadline
// (e.g. session storage's longer boot dial) should set ConnectTimeout themselves
// and only call ApplyRuntimeParams.
func ApplyConnConfig(cfg *pgx.ConnConfig) {
	if cfg == nil {
		return
	}
	cfg.ConnectTimeout = ConnectTimeout()
	ApplyRuntimeParams(&cfg.RuntimeParams)
}

func positiveDuration(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}
