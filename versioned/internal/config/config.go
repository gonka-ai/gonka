package config

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultDrainKillGrace     = 10 * time.Minute
	DefaultHostShutdownBudget = 25 * time.Minute
	DefaultAdminListenAddr    = "127.0.0.1:8081"
)

type Config struct {
	OracleURL          string
	PollInterval       time.Duration
	BinDir             string
	DataDir            string
	BinaryName         string
	BasePort           int
	ReadyPath          string
	ReadyTimeout       time.Duration
	DrainPath          string
	DrainStatusPath    string
	DrainTimeout       time.Duration
	DrainPollInterval  time.Duration
	DrainKillGrace     time.Duration
	HostShutdownBudget time.Duration
	AdminListenAddr    string
	Overrides          map[string]string // version name -> local binary path
	ForceVersions      []string          // version names that must run regardless of oracle
}

func Load() (Config, error) {
	oracleURL := os.Getenv("VERSIOND_ORACLE_URL")
	if oracleURL == "" {
		return Config{}, fmt.Errorf("VERSIOND_ORACLE_URL is required")
	}

	cfg := Config{
		OracleURL:         oracleURL,
		PollInterval:      parseDuration("VERSIOND_POLL_INTERVAL", 30*time.Second),
		BinDir:            envOrDefault("VERSIOND_BIN_DIR", "/opt/versiond/bin"),
		DataDir:           envOrDefault("VERSIOND_DATA_DIR", "/opt/versiond/data"),
		BinaryName:        envOrDefault("VERSIOND_BINARY_NAME", "devshard"),
		BasePort:          5000,
		ReadyPath:         envOrDefault("VERSIOND_READY_PATH", "/ready"),
		ReadyTimeout:      parseDuration("VERSIOND_READY_TIMEOUT", 60*time.Second),
		DrainPath:         envOrDefault("VERSIOND_DRAIN_PATH", "/drain"),
		DrainStatusPath:   envOrDefault("VERSIOND_DRAIN_STATUS_PATH", "/drain/status"),
		DrainTimeout:      parseDuration("VERSIOND_DRAIN_TIMEOUT", 15*time.Minute),
		DrainPollInterval: parseDuration("VERSIOND_DRAIN_POLL_INTERVAL", time.Second),
		DrainKillGrace:    parseDuration("VERSIOND_DRAIN_KILL_GRACE", DefaultDrainKillGrace),
		HostShutdownBudget: parseDuration(
			"VERSIOND_HOST_SHUTDOWN_BUDGET",
			DefaultHostShutdownBudget,
		),
		AdminListenAddr: envOrDefault(
			"VERSIOND_ADMIN_LISTEN_ADDR",
			DefaultAdminListenAddr,
		),
		Overrides:     loadOverrides(),
		ForceVersions: loadForceVersions(),
	}
	if err := validateAdminListenAddr(cfg.AdminListenAddr); err != nil {
		return Config{}, err
	}

	slog.Info(
		"versiond config loaded",
		"oracle_url", cfg.OracleURL,
		"binary_name", cfg.BinaryName,
		"admin_listen_addr", cfg.AdminListenAddr,
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

func validateAdminListenAddr(addr string) error {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf(
			"VERSIOND_ADMIN_LISTEN_ADDR must be a host:port address: %w",
			err,
		)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 || port > 65535 {
		return fmt.Errorf(
			"VERSIOND_ADMIN_LISTEN_ADDR has invalid port %q",
			portText,
		)
	}
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf(
			"VERSIOND_ADMIN_LISTEN_ADDR must use a loopback host, got %q",
			host,
		)
	}
	return nil
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

func parseDuration(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

func sortedOverrideKeys(overrides map[string]string) []string {
	out := make([]string, 0, len(overrides))
	for k := range overrides {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
