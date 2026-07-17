// Package mode selects how devshard session and payload storage is backed.
//
// DEVSHARD_STORAGE_MODE is the single mode knob:
//
//	sqlite   — local only (SQLite sessions / file payloads)
//	hybrid   — Postgres primary with local fallback (requires PGHOST)
//	postgres — Postgres-only, fail-closed (requires PGHOST; HA / multi-instance)
//	auto     — default; derive from PGHOST (see Resolve)
//
// PGHOST / PG* remain connection settings and are not mode bits, except under
// auto where a non-empty PGHOST selects hybrid.
package mode

import (
	"fmt"
	"os"
	"strings"
)

// EnvStorageMode is the environment variable that selects storage mode.
const EnvStorageMode = "DEVSHARD_STORAGE_MODE"

// Mode is an effective storage backend policy. Resolve never returns Auto.
type Mode string

const (
	// Auto is only accepted as an input to Resolve (default when unset).
	Auto Mode = "auto"
	// SQLite is local-only storage (sessions SQLite / payloads files).
	SQLite Mode = "sqlite"
	// Hybrid is Postgres primary with local fallback when Postgres is down.
	Hybrid Mode = "hybrid"
	// Postgres is Postgres-only: boot fails if Postgres is unreachable, and
	// local artifacts are migrated then quarantined (multi-instance / HA).
	Postgres Mode = "postgres"
)

// Resolve reads DEVSHARD_STORAGE_MODE (default auto) and returns an effective
// mode (sqlite, hybrid, or postgres).
//
// Auto resolution (also when the env is unset or empty):
//  1. if PGHOST is set → hybrid
//  2. else → sqlite
//
// Multi-instance / fail-closed deployments must set
// DEVSHARD_STORAGE_MODE=postgres explicitly. VERSIOND_FORCE never implies
// postgres. See ConfiguredForHA / RequireConfiguredForHA for the check used
// when versiond-router sets the Devshard-Ha request header.
func Resolve() (Mode, error) {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvStorageMode)))
	if raw == "" {
		raw = string(Auto)
	}

	switch Mode(raw) {
	case Auto:
		return resolveAuto(), nil
	case SQLite, Hybrid, Postgres:
		return Mode(raw), nil
	default:
		return "", fmt.Errorf(
			"invalid %s %q (want sqlite, hybrid, postgres, or auto)",
			EnvStorageMode, raw,
		)
	}
}

func resolveAuto() Mode {
	if strings.TrimSpace(os.Getenv("PGHOST")) != "" {
		return Hybrid
	}
	return SQLite
}

// RequiresPGHOST reports whether the mode needs a Postgres connection config.
func (m Mode) RequiresPGHOST() bool {
	return m == Hybrid || m == Postgres
}

// FailClosed reports whether Postgres must be reachable at boot with no local fallback.
func (m Mode) FailClosed() bool {
	return m == Postgres
}
