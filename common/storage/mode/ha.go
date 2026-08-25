package mode

import (
	"fmt"
	"net/http"
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

// ParseDevshardHAHeader reports whether the request was marked as
// multi-instance HA by the router. Absence means non-HA. A present malformed or
// repeated value is an error so a typo cannot silently disable the storage
// guard.
func ParseDevshardHAHeader(h http.Header) (bool, error) {
	if h == nil {
		return false, nil
	}
	vals := h.Values(HeaderDevshardHA)
	if len(vals) == 0 {
		return false, nil
	}
	if len(vals) != 1 {
		return false, fmt.Errorf("%s must have exactly one value", HeaderDevshardHA)
	}
	raw := vals[0]
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "t", "true", "yes", "on":
		return true, nil
	case "0", "f", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf(
			"%s=%q is not a boolean; use empty/1/t/true/yes/on or 0/f/false/no/off",
			HeaderDevshardHA,
			raw,
		)
	}
}

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
