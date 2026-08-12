package config

import (
	"os"
	"regexp"
	"strconv"
	"strings"
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
	if cfg.DrainKillGrace != DefaultDrainKillGrace {
		t.Errorf("DrainKillGrace = %v, want %v", cfg.DrainKillGrace, DefaultDrainKillGrace)
	}
	if cfg.HostShutdownBudget != DefaultHostShutdownBudget {
		t.Errorf(
			"HostShutdownBudget = %v, want %v",
			cfg.HostShutdownBudget,
			DefaultHostShutdownBudget,
		)
	}
	if cfg.DrainAnnounce != DefaultDrainAnnounce {
		t.Errorf(
			"DrainAnnounce = %v, want %v",
			cfg.DrainAnnounce,
			DefaultDrainAnnounce,
		)
	}
}

func TestLoad_RequiresCompleteConsensusVerifierConfiguration(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle.test/versions")
	t.Setenv("VERSIOND_CONSENSUS_PARAMS_URL", "http://node.test/params")
	t.Setenv("VERSIOND_CONSENSUS_STATUS_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted only one consensus verifier endpoint")
	}
}

func TestLoad_HARequiresConsensusVerifierConfiguration(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle.test/versions")
	t.Setenv("GONKA_HA", "true")
	t.Setenv("VERSIOND_CONSENSUS_PARAMS_URL", "")
	t.Setenv("VERSIOND_CONSENSUS_STATUS_URL", "")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted HA mode without local consensus verification")
	}

	t.Setenv("VERSIOND_CONSENSUS_PARAMS_URL", "http://node.test/params")
	t.Setenv("VERSIOND_CONSENSUS_STATUS_URL", "http://node.test/status")
	if _, err := Load(); err != nil {
		t.Fatalf("Load rejected complete HA consensus configuration: %v", err)
	}
}

func TestLoad_HARejectsLocalArtifactOverrides(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle.test/versions")
	t.Setenv("GONKA_HA", "true")
	t.Setenv("VERSIOND_CONSENSUS_PARAMS_URL", "http://node.test/params")
	t.Setenv("VERSIOND_CONSENSUS_STATUS_URL", "http://node.test/status")
	t.Setenv("VERSIOND_OVERRIDE_v5", "/tmp/devshardd-v5")

	if _, err := Load(); err == nil {
		t.Fatal("Load accepted a local artifact override in HA mode")
	}
}

func TestLoad_CustomValues(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://custom:9090/v")
	t.Setenv("VERSIOND_POLL_INTERVAL", "10s")
	t.Setenv("VERSIOND_BIN_DIR", "/tmp/bin")
	t.Setenv("VERSIOND_DATA_DIR", "/tmp/data")
	t.Setenv("VERSIOND_BINARY_NAME", "myapp")
	t.Setenv("VERSIOND_HOST_SHUTDOWN_BUDGET", "12m")
	t.Setenv("VERSIOND_DRAIN_ANNOUNCE", "9s")

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
	if cfg.HostShutdownBudget != 12*time.Minute {
		t.Errorf("HostShutdownBudget = %v, want %v", cfg.HostShutdownBudget, 12*time.Minute)
	}
	if cfg.DrainAnnounce != 9*time.Second {
		t.Errorf("DrainAnnounce = %v, want %v", cfg.DrainAnnounce, 9*time.Second)
	}
}

// A malformed duration refuses to boot rather than silently borrowing the
// default: a typo in a drain or shutdown budget would otherwise surface during
// the outage it was meant to bound.
func TestLoad_MalformedDurationsRefuseToBoot(t *testing.T) {
	for _, key := range []string{
		"VERSIOND_POLL_INTERVAL",
		"VERSIOND_HOST_SHUTDOWN_BUDGET",
		"VERSIOND_DRAIN_ANNOUNCE",
	} {
		t.Run(key, func(t *testing.T) {
			t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
			t.Setenv(key, "notaduration")
			if _, err := Load(); err == nil {
				t.Fatalf("%s=notaduration was silently accepted", key)
			}
		})
	}
}

// Zero means "no ticker" for an interval, which panics, so only the announce
// window — where zero is the explicit "no balancer in front" — may be zero.
func TestLoad_ZeroDurations(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")
	t.Setenv("VERSIOND_POLL_INTERVAL", "0s")
	if _, err := Load(); err == nil {
		t.Fatal("a zero poll interval was accepted; NewTicker(0) panics")
	}

	t.Setenv("VERSIOND_POLL_INTERVAL", "")
	t.Setenv("VERSIOND_DRAIN_ANNOUNCE", "0s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("zero announce must mean no balancer, got: %v", err)
	}
	if cfg.DrainAnnounce != 0 {
		t.Fatalf("DrainAnnounce = %v, want 0", cfg.DrainAnnounce)
	}
}

// The announce window has a floor and a ceiling: the balancer probes once a
// second, so a shorter window can fall entirely between probes; and the window
// is part of the shutdown budget, so it cannot swallow it.
func TestLoad_DrainAnnounceBounds(t *testing.T) {
	t.Setenv("VERSIOND_ORACLE_URL", "http://oracle:8080/versions")

	for _, tooShort := range []string{"500ms", "2s", "4s"} {
		t.Setenv("VERSIOND_HOST_SHUTDOWN_BUDGET", "")
		t.Setenv("VERSIOND_DRAIN_ANNOUNCE", tooShort)
		if _, err := Load(); err == nil {
			t.Fatalf("announce %s was accepted; the router needs ~4s to observe the failing check", tooShort)
		}
	}

	t.Setenv("VERSIOND_DRAIN_ANNOUNCE", "10s")
	t.Setenv("VERSIOND_HOST_SHUTDOWN_BUDGET", "10s")
	if _, err := Load(); err == nil {
		t.Fatal("an announce window as long as the whole budget was accepted")
	}

	t.Setenv("VERSIOND_DRAIN_ANNOUNCE", "5s")
	t.Setenv("VERSIOND_HOST_SHUTDOWN_BUDGET", "90s")
	if _, err := Load(); err != nil {
		t.Fatalf("valid announce/budget pair refused: %v", err)
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

// The floor is derived from the router's health-check settings, which live in
// config templates no compiler connects to this package — so this test reads
// the real templates rather than restating their numbers. Every backend that
// health-checks versiond counts: the per-version pools and the host-level pool
// share one server-template, but versiond_legacy carries its own check
// directive, and legacy-pinned traffic deserves the same announce guarantee.
// The worst case is therefore the maximum over every checked server.
//
// Editing any `inter`, `fall`, or `timeout check` without revisiting
// MinDrainAnnounce fails here; so does a checked server that stops declaring
// either health-check parameter (HAProxy would silently substitute its own
// default), and so does moving or renaming a template — loudly, because a
// skipped contract is a disabled one.
func TestMinDrainAnnounceCoversTheRouterDetectionWorstCase(t *testing.T) {
	templates := []string{
		"../../../versiond-router/haproxy.cfg.template",
		"../../../versiond-router/pool-backend.cfg.template",
	}

	detectionInterval := maxCheckedServerDetectionInterval(t, templates)
	checkTimeout := maxTemplateDuration(t, templates,
		regexp.MustCompile(`(?m)^[\t ]*timeout check ([0-9]+m?s)`))

	// Worst case: a probe answered "ready" just before the flip legally
	// concludes checkTimeout later, followed by every failed check required to
	// mark the server down.
	const margin = time.Second
	worstDetection := checkTimeout + detectionInterval
	if MinDrainAnnounce < worstDetection+margin {
		t.Fatalf(
			"MinDrainAnnounce=%s does not cover the router's worst-case detection %s (timeout check %s + inter*fall %s) plus %s margin",
			MinDrainAnnounce, worstDetection, checkTimeout, detectionInterval, margin)
	}
}

// Anchored to directives at line start: the templates' comments mention these
// settings by name, and a comment must no more supply a contract value than
// satisfy a render assert.
var (
	serverLine  = regexp.MustCompile(`(?m)^[\t ]*server(-template)? [^\n]*`)
	serverInter = regexp.MustCompile(` inter ([0-9]+m?s)`)
	serverFall  = regexp.MustCompile(` fall ([0-9]+)`)
)

func maxCheckedServerDetectionInterval(t *testing.T, paths []string) time.Duration {
	t.Helper()
	var max time.Duration
	found := 0
	for _, path := range paths {
		for _, line := range serverLine.FindAllString(readTemplate(t, path), -1) {
			if !strings.Contains(line, " check") {
				continue
			}
			m := serverInter.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("%s: a checked server does not declare `inter`; HAProxy would silently use its own default:\n%s",
					path, line)
			}
			fall := serverFall.FindStringSubmatch(line)
			if fall == nil {
				t.Fatalf("%s: a checked server does not declare `fall`; HAProxy would silently use its own default:\n%s",
					path, line)
			}
			failures, err := strconv.Atoi(fall[1])
			if err != nil {
				t.Fatalf("%s: invalid fall count %q: %v", path, fall[1], err)
			}
			found++
			if d := parseTemplateDuration(t, path, m[1]) * time.Duration(failures); d > max {
				max = d
			}
		}
	}
	if found == 0 {
		t.Fatal("no checked servers found in the router templates; the MinDrainAnnounce derivation has lost its source")
	}
	return max
}

func maxTemplateDuration(t *testing.T, paths []string, directive *regexp.Regexp) time.Duration {
	t.Helper()
	var max time.Duration
	found := 0
	for _, path := range paths {
		for _, m := range directive.FindAllStringSubmatch(readTemplate(t, path), -1) {
			found++
			if d := parseTemplateDuration(t, path, m[1]); d > max {
				max = d
			}
		}
	}
	if found == 0 {
		t.Fatalf("no template contains %v; the MinDrainAnnounce derivation has lost its source", directive)
	}
	return max
}

func readTemplate(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the router template this contract is derived from is unreadable: %v", err)
	}
	return string(body)
}

func parseTemplateDuration(t *testing.T, path, raw string) time.Duration {
	t.Helper()
	d, err := time.ParseDuration(raw)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	return d
}
