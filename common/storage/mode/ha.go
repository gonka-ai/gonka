package mode

import (
	"fmt"
	"net/http"
	"os"
	"strings"
)

// HeaderDevshardHA is set by versiond-router on requests that sticky-hash across
// the HA pool (version not in VERSIOND_NON_HA_VERSIONS) when the deployment
// declares itself HA.
const HeaderDevshardHA = "Devshard-Ha"

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
	case "", "1", "true", "yes":
		return true, nil
	case "0", "false", "no":
		return false, nil
	default:
		return false, fmt.Errorf(
			"%s=%q is not a boolean; use 1/true/yes or 0/false/no",
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

// EnvHADeployment marks a deployment where several devshard instances may be
// routed the same escrow. It is set by the HA compose overlay, not by operators,
// so it cannot be forgotten when the overlay is in use and cannot be set by
// accident in a single-instance stack.
const EnvHADeployment = "GONKA_HA"

// HADeployment reports whether this process runs as part of a multi-instance
// deployment. The boolean grammar is shared with the router's entrypoint —
// 1/true/yes on, empty/0/false/no off — and anything else is an error rather
// than a guess. GONKA_HA gates a storage-safety boot guard, and the previous
// parser read a typo as "off": exactly the value that silently disables the
// guard it was meant to enable.
func HADeployment() (bool, error) {
	raw := os.Getenv(EnvHADeployment)
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes":
		return true, nil
	case "", "0", "false", "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s=%q is not a boolean; use 1/true/yes or 0/false/no",
			EnvHADeployment, raw)
	}
}

// RequireHADeploymentStorage fails fast at startup when this process is part of
// an HA deployment but its storage is not fail-closed Postgres.
//
// The per-request Devshard-Ha guard stays as a second line of defence for a
// single-instance host that is joined into a pool without a restart. This check
// turns the far more common case — a misconfigured HA rollout — from a runtime
// 503 that a user discovers into a boot failure that the operator sees.
func RequireHADeploymentStorage() error {
	ha, err := HADeployment()
	if err != nil {
		return err
	}
	if !ha {
		return nil
	}
	if err := RequireConfiguredForHA(); err != nil {
		return fmt.Errorf("%s=true: %w", EnvHADeployment, err)
	}
	return nil
}
