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

// HasDevshardHAHeader reports whether the request was marked as multi-instance HA
// by the router. Accepts value "true" / "1" / "yes" (case-insensitive) or an
// empty value when the header is present.
func HasDevshardHAHeader(h http.Header) bool {
	if h == nil {
		return false
	}
	vals, ok := h[http.CanonicalHeaderKey(HeaderDevshardHA)]
	if !ok || len(vals) == 0 {
		return false
	}
	v := strings.TrimSpace(strings.ToLower(vals[0]))
	return v == "" || v == "true" || v == "1" || v == "yes"
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
// deployment.
func HADeployment() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvHADeployment))) {
	case "true", "1", "yes":
		return true
	default:
		return false
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
	if !HADeployment() {
		return nil
	}
	if err := RequireConfiguredForHA(); err != nil {
		return fmt.Errorf("%s=true: %w", EnvHADeployment, err)
	}
	return nil
}
