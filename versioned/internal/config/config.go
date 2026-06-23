package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
)

type Config struct {
	OracleURL     string
	PollInterval  time.Duration
	BinDir        string
	DataDir       string
	BinaryName    string
	BasePort      int
	Overrides     map[string]string // version name -> local binary path
	ForceVersions []string          // version names that must run regardless of oracle
}

func Load() (Config, error) {
	oracleURL := os.Getenv("VERSIOND_ORACLE_URL")
	if oracleURL == "" {
		return Config{}, fmt.Errorf("VERSIOND_ORACLE_URL is required")
	}

	cfg := Config{
		OracleURL:     oracleURL,
		PollInterval:  parseDuration("VERSIOND_POLL_INTERVAL", 30*time.Second),
		BinDir:        envOrDefault("VERSIOND_BIN_DIR", "/opt/versiond/bin"),
		DataDir:       envOrDefault("VERSIOND_DATA_DIR", "/opt/versiond/data"),
		BinaryName:    envOrDefault("VERSIOND_BINARY_NAME", "devshard"),
		BasePort:      5000,
		Overrides:     loadOverrides(),
		ForceVersions: loadForceVersions(),
	}

	slog.Info(
		"versiond config loaded",
		"oracle_url", cfg.OracleURL,
		"binary_name", cfg.BinaryName,
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
