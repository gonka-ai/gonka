package mode

import (
	"fmt"
	"os"
	"strings"
)

// HeaderDevshardHA is set by versiond-router on requests that sticky-hash across
// the HA pool (version not in VERSIOND_NON_HA_VERSIONS) when the deployment
// declares itself HA or the selected backend has more than one usable server.
const HeaderDevshardHA = "Devshard-Ha"

// EnvHADeployment declares that multiple devshard instances may serve the same
// escrow and therefore require fail-closed shared storage.
const EnvHADeployment = "GONKA_HA"

// ConfiguredForHA reports whether process env is explicitly fail-closed Postgres
// suitable for multi-instance routing: DEVSHARD_STORAGE_MODE must be the literal
// "postgres" (not auto/sqlite/hybrid) and PGHOST must be set.
//
// Resolve() alone is insufficient: auto+PGHOST yields hybrid, which still has
// local fallback and must not serve HA-routed traffic.
func ConfiguredForHA() bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvStorageMode)))
	if raw != string(Postgres) {
		return false
	}
	return strings.TrimSpace(os.Getenv("PGHOST")) != ""
}

// RequireConfiguredForHA returns nil only when ConfiguredForHA is true.
func RequireConfiguredForHA() error {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(EnvStorageMode)))
	if raw == "" {
		raw = string(Auto)
	}
	pgHostSet := strings.TrimSpace(os.Getenv("PGHOST")) != ""
	if raw != string(Postgres) {
		return fmt.Errorf(
			"HA routing requires %s=%s (got %q); auto/sqlite/hybrid are not fail-closed multi-instance",
			EnvStorageMode, Postgres, raw,
		)
	}
	if !pgHostSet {
		return fmt.Errorf("HA routing requires PGHOST to be set with %s=%s", EnvStorageMode, Postgres)
	}
	return nil
}
