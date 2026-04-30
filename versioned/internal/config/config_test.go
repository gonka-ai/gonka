package config

import (
	"testing"
	"time"
)

func TestLoad_MissingOracleURL(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when VERSIOND_ORACLE_URL is missing")
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.OracleURL != "http://oracle:8080/versions" {
		t.Errorf("OracleURL = %q, want %q", cfg.OracleURL, "http://oracle:8080/versions")
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 30*time.Second)
	}
	if cfg.BinDir != "/opt/versiond/bin" {
		t.Errorf("BinDir = %q, want %q", cfg.BinDir, "/opt/versiond/bin")
	}
	if cfg.DataDir != "/opt/versiond/data" {
		t.Errorf("DataDir = %q, want %q", cfg.DataDir, "/opt/versiond/data")
	}
	if cfg.BinaryName != "devshard" {
		t.Errorf("BinaryName = %q, want %q", cfg.BinaryName, "devshard")
	}
	if cfg.BasePort != 5000 {
		t.Errorf("BasePort = %d, want %d", cfg.BasePort, 5000)
	}
}

func TestLoad_CustomValues(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://custom:9090/v")
	t.Setenv("VERSIOND_POLL_INTERVAL", "10s")
	t.Setenv("VERSIOND_BIN_DIR", "/tmp/bin")
	t.Setenv("VERSIOND_DATA_DIR", "/tmp/data")
	t.Setenv("VERSIOND_BINARY_NAME", "myapp")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 10*time.Second {
		t.Errorf("PollInterval = %v, want %v", cfg.PollInterval, 10*time.Second)
	}
	if cfg.BinDir != "/tmp/bin" {
		t.Errorf("BinDir = %q, want %q", cfg.BinDir, "/tmp/bin")
	}
	if cfg.BinaryName != "myapp" {
		t.Errorf("BinaryName = %q, want %q", cfg.BinaryName, "myapp")
	}
}

func TestLoad_InvalidPollInterval(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_POLL_INTERVAL", "notaduration")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v, want fallback %v", cfg.PollInterval, 30*time.Second)
	}
}

func TestListenAddr(t *testing.T) {
	if got := ListenAddr(); got != ":8080" {
		t.Errorf("ListenAddr() = %q, want %q", got, ":8080")
	}
}

func TestLoad_ForceVersions(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_FORCE", "v1,v2,v3")
	t.Setenv("VERSIOND_OVERRIDE_v1", "/path/to/v1")
	t.Setenv("VERSIOND_OVERRIDE_v2", "/path/to/v2")
	t.Setenv("VERSIOND_OVERRIDE_v3", "/path/to/v3")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ForceVersions) != 3 {
		t.Fatalf("ForceVersions length = %d, want 3", len(cfg.ForceVersions))
	}
	if cfg.ForceVersions[0] != "v1" || cfg.ForceVersions[1] != "v2" || cfg.ForceVersions[2] != "v3" {
		t.Errorf("ForceVersions = %v, want [v1 v2 v3]", cfg.ForceVersions)
	}
}

func TestLoadOverrides_UnderscoresToDots(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_OVERRIDE_v0_2_11", "/path/to/v0.2.11")
	t.Setenv("VERSIOND_OVERRIDE_v0_2_12", "/path/to/v0.2.12")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p, ok := cfg.Overrides["v0.2.11"]; !ok || p != "/path/to/v0.2.11" {
		t.Errorf("Overrides[v0.2.11] = %q (ok=%v), want /path/to/v0.2.11", p, ok)
	}
	if p, ok := cfg.Overrides["v0.2.12"]; !ok || p != "/path/to/v0.2.12" {
		t.Errorf("Overrides[v0.2.12] = %q (ok=%v), want /path/to/v0.2.12", p, ok)
	}
	if _, ok := cfg.Overrides["v0_2_11"]; ok {
		t.Error("Overrides should not contain raw underscore key v0_2_11")
	}
}

func TestLoad_ForceVersionsEmpty(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ForceVersions) != 0 {
		t.Errorf("ForceVersions should be empty, got %v", cfg.ForceVersions)
	}
}

func TestLoad_ForceVersionsTrimsSpaces(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_FORCE", " v1 , v2 , ")
	t.Setenv("VERSIOND_OVERRIDE_v1", "/path/to/v1")
	t.Setenv("VERSIOND_OVERRIDE_v2", "/path/to/v2")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.ForceVersions) != 2 {
		t.Fatalf("ForceVersions length = %d, want 2", len(cfg.ForceVersions))
	}
	if cfg.ForceVersions[0] != "v1" || cfg.ForceVersions[1] != "v2" {
		t.Errorf("ForceVersions = %v, want [v1 v2]", cfg.ForceVersions)
	}
}

func TestLoad_ForceVersionsDottedWithOverride(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_FORCE", "v0.2.11")
	t.Setenv("VERSIOND_OVERRIDE_v0_2_11", "/path/to/devshard")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := cfg.Overrides["v0.2.11"]; !ok {
		t.Fatal("forced version v0.2.11 should match override set via VERSIOND_OVERRIDE_v0_2_11")
	}
}
