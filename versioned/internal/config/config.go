package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

const (
	DefaultDrainKillGrace        = 10 * time.Minute
	DefaultDevshardShutdownGrace = 10 * time.Minute
	DefaultHostShutdownBudget    = 25 * time.Minute
	// DefaultDrainAnnounce must exceed the load balancer's health-check
	// detection time (interval x fall) so no request is refused before the
	// balancer has withdrawn this upstream.
	DefaultDrainAnnounce = 5 * time.Second
)

type Config struct {
	OracleURL         string
	PollInterval      time.Duration
	BinDir            string
	DataDir           string
	BinaryName        string
	BasePort          int
	ReadyPath         string
	ReadyTimeout      time.Duration
	ReadyMaxWait      time.Duration
	DrainPath         string
	DrainStatusPath   string
	DrainTimeout      time.Duration
	DrainPollInterval time.Duration
	DrainKillGrace    time.Duration
	// ChildShutdownGrace is the effective max of the supervisor kill grace and
	// the supervised binary's own SIGTERM shutdown budget.
	ChildShutdownGrace time.Duration
	HostShutdownBudget time.Duration
	DrainAnnounce      time.Duration
	Overrides          map[string]string // version name -> local binary path
	ForceVersions      []string          // version names that must run regardless of oracle
}

func Load() (Config, error) {
	oracleURL := os.Getenv("VERSIOND_ORACLE_URL")
	if oracleURL == "" {
		return Config{}, fmt.Errorf("VERSIOND_ORACLE_URL is required")
	}

	cfg := Config{
		OracleURL:       oracleURL,
		BinDir:          envOrDefault("VERSIOND_BIN_DIR", "/opt/versiond/bin"),
		DataDir:         envOrDefault("VERSIOND_DATA_DIR", "/opt/versiond/data"),
		BinaryName:      envOrDefault("VERSIOND_BINARY_NAME", "devshard"),
		BasePort:        5000,
		ReadyPath:       envOrDefault("VERSIOND_READY_PATH", "/ready"),
		DrainPath:       envOrDefault("VERSIOND_DRAIN_PATH", "/drain"),
		DrainStatusPath: envOrDefault("VERSIOND_DRAIN_STATUS_PATH", "/drain/status"),
		Overrides:       loadOverrides(),
		ForceVersions:   loadForceVersions(),
	}
	for _, d := range []struct {
		dst      *time.Duration
		key      string
		fallback time.Duration
		// allowZero: only the announce window may be zero — the explicit "no
		// balancer in front". A zero interval or timeout is never meaningful
		// (time.NewTicker(0) panics), so everything else stays strictly positive.
		allowZero bool
	}{
		{&cfg.PollInterval, "VERSIOND_POLL_INTERVAL", 30 * time.Second, false},
		{&cfg.ReadyTimeout, "VERSIOND_READY_TIMEOUT", 60 * time.Second, false},
		{&cfg.ReadyMaxWait, "VERSIOND_READY_MAX_WAIT", 32 * time.Minute, false},
		{&cfg.DrainTimeout, "VERSIOND_DRAIN_TIMEOUT", 15 * time.Minute, false},
		{&cfg.DrainPollInterval, "VERSIOND_DRAIN_POLL_INTERVAL", time.Second, false},
		{&cfg.DrainKillGrace, "VERSIOND_DRAIN_KILL_GRACE", DefaultDrainKillGrace, false},
		{&cfg.HostShutdownBudget, "VERSIOND_HOST_SHUTDOWN_BUDGET", DefaultHostShutdownBudget, false},
		{&cfg.DrainAnnounce, "VERSIOND_DRAIN_ANNOUNCE", DefaultDrainAnnounce, true},
	} {
		value, err := parseDuration(d.key, d.fallback, d.allowZero)
		if err != nil {
			return Config{}, err
		}
		*d.dst = value
	}
	if cfg.ReadyMaxWait < cfg.ReadyTimeout {
		cfg.ReadyMaxWait = cfg.ReadyTimeout
	}
	cfg.ChildShutdownGrace = cfg.DrainKillGrace
	if isDevshardBinary(cfg.BinaryName) {
		devshardGrace, err := parseDuration(
			"DEVSHARD_SHUTDOWN_GRACE",
			DefaultDevshardShutdownGrace,
			false,
		)
		if err != nil {
			return Config{}, err
		}
		if devshardGrace > cfg.ChildShutdownGrace {
			cfg.ChildShutdownGrace = devshardGrace
		}
	}
	if err := validateShutdownBudget(
		cfg.DrainAnnounce,
		cfg.ChildShutdownGrace,
		cfg.HostShutdownBudget,
	); err != nil {
		return Config{}, err
	}

	slog.Info(
		"versiond config loaded",
		"oracle_url", cfg.OracleURL,
		"binary_name", cfg.BinaryName,
		"drain_announce", cfg.DrainAnnounce,
		"child_shutdown_grace", cfg.ChildShutdownGrace,
		"host_shutdown_budget", cfg.HostShutdownBudget,
		"force_versions", cfg.ForceVersions,
		"override_versions", sortedOverrideKeys(cfg.Overrides),
	)

	// Validate: forced versions must have a corresponding override.
	for _, name := range cfg.ForceVersions {
		if _, ok := cfg.Overrides[name]; !ok {
			slog.Error("forced version has no override, will be skipped during reconcile",
				"version", name,
				"expected_env_key", fmt.Sprintf("VERSIOND_OVERRIDE_%s", versionToEnvSuffix(name)),
				"hint", fmt.Sprintf("set VERSIOND_OVERRIDE_%s=/path/to/binary", versionToEnvSuffix(name)))
		}
	}

	return cfg, nil
}

// ListenAddr returns the hardcoded listen address.
func ListenAddr() string {
	return ":8080"
}

const overridePrefix = "VERSIOND_OVERRIDE_"

// loadOverrides scans env vars for VERSIOND_OVERRIDE_<name>=<path>.
// Underscores in the env var suffix are converted back to dots so that
// VERSIOND_OVERRIDE_v0_2_11 maps to version name "v0.2.11".
func loadOverrides() map[string]string {
	overrides := make(map[string]string)
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, overridePrefix) {
			continue
		}
		idx := strings.IndexByte(e, '=')
		if idx < 0 {
			continue
		}
		suffix := e[len(overridePrefix):idx]
		name := envSuffixToVersion(suffix)
		path := e[idx+1:]
		if name != "" && path != "" {
			overrides[name] = path
		}
	}
	return overrides
}

// envSuffixToVersion converts an env var suffix back to a version name
// by replacing underscores with dots (e.g. "v0_2_11" -> "v0.2.11").
func envSuffixToVersion(suffix string) string {
	return strings.ReplaceAll(suffix, "_", ".")
}

// versionToEnvSuffix converts a version name to an env var suffix
// by replacing dots with underscores (e.g. "v0.2.11" -> "v0_2_11").
func versionToEnvSuffix(name string) string {
	return strings.ReplaceAll(name, ".", "_")
}

// loadForceVersions parses VERSIOND_FORCE env var (comma-separated version names).
func loadForceVersions() []string {
	v := os.Getenv("VERSIOND_FORCE")
	if v == "" {
		return nil
	}
	var result []string
	for _, name := range strings.Split(v, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result = append(result, name)
		}
	}
	return result
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func isDevshardBinary(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "devshard", "devshardd":
		return true
	default:
		return false
	}
}

// MinDrainAnnounce is the shortest announce window that guarantees the load
// balancer observes the failing check before admission closes. The router
// (versiond-router/haproxy.cfg.template) probes with `inter 1s` and grants each
// probe `timeout check 3s`, so the worst case is a probe answered "ready" just
// before the flip that legally completes 3s later, plus 1s until the next probe
// begins and fails: ~4s until the host is out of rotation, and one more second
// of margin. A shorter window means versiond stops accepting while the router
// still routes here, and the requests in between are refused.
const MinDrainAnnounce = 5 * time.Second

// validateShutdownBudget accepts a zero announce window — the explicit "no
// balancer in front" — but reserves both the announce window and the slowest
// child's SIGTERM grace inside the one absolute host shutdown budget.
func validateShutdownBudget(announce, childGrace, budget time.Duration) error {
	if announce != 0 && announce < MinDrainAnnounce {
		return fmt.Errorf(
			"VERSIOND_DRAIN_ANNOUNCE=%s is below %s: the balancer needs up to ~4s to observe the failing check (inter 1s, timeout check 3s), and a shorter window closes admission while traffic still arrives; use 0 to declare there is no balancer",
			announce, MinDrainAnnounce)
	}
	if announce >= budget {
		return fmt.Errorf(
			"VERSIOND_DRAIN_ANNOUNCE=%s must be below VERSIOND_HOST_SHUTDOWN_BUDGET=%s: the announce window is part of the budget, not in addition to it",
			announce, budget)
	}
	remainingAfterAnnounce := budget - announce
	if childGrace >= remainingAfterAnnounce {
		return fmt.Errorf(
			"effective child shutdown grace=%s plus VERSIOND_DRAIN_ANNOUNCE=%s must be below VERSIOND_HOST_SHUTDOWN_BUDGET=%s: reserve time for request and child drain before SIGTERM",
			childGrace, announce, budget)
	}
	return nil
}

// parseDuration rejects a malformed or non-positive value instead of silently
// substituting the default: a typo in a drain or shutdown budget would
// otherwise be discovered during the outage it was meant to bound.
func parseDuration(key string, fallback time.Duration, allowZero bool) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s=%q: %w", key, v, err)
	}
	if d < 0 || (d == 0 && !allowZero) {
		return 0, fmt.Errorf("%s=%q must be positive", key, v)
	}
	return d, nil
}

func sortedOverrideKeys(overrides map[string]string) []string {
	out := make([]string, 0, len(overrides))
	for k := range overrides {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
