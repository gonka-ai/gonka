package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"devshard/signing"
	"devshard/testenv/config"
)

// TestIsPlaceholderKey covers the handful of placeholder strings
// gencompose must overwrite; anything else is treated as operator
// intent and preserved.
func TestIsPlaceholderKey(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", true},
		{"   ", true},
		{"TODO(phase-10): host 0", true},
		{"todo", true},
		{"CHANGEME", true},
		{"ChangeMe please", true},
		{"deadbeef", false},
		{"0xabcd", false},
	}
	for _, tc := range cases {
		if got := isPlaceholderKey(tc.in); got != tc.want {
			t.Errorf("isPlaceholderKey(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestGenerateHosts_FillsMissingFields verifies every missing-ish host
// field is populated: key, address, id, port. A pre-populated key is
// kept, and its address is derived if missing.
func TestGenerateHosts_FillsMissingFields(t *testing.T) {
	seed, err := signing.GenerateKey()
	if err != nil {
		t.Fatalf("generate seed key: %v", err)
	}

	cfg := &config.Config{
		Hosts: []config.HostCfg{
			{}, // fully empty
			{ID: "custom-host-1"},
			{PrivateKeyHex: seed.PrivateKeyHex()}, // key but no address
		},
	}
	if err := generateHosts(cfg); err != nil {
		t.Fatalf("generateHosts: %v", err)
	}

	if cfg.Hosts[0].PrivateKeyHex == "" || cfg.Hosts[0].Address == "" {
		t.Fatalf("host 0: key/address not generated: %+v", cfg.Hosts[0])
	}
	if cfg.Hosts[0].ID != "devshardd-testenv-0" {
		t.Errorf("host 0 id = %q, want auto", cfg.Hosts[0].ID)
	}
	if cfg.Hosts[0].Port != config.DefaultHostPort {
		t.Errorf("host 0 port = %d, want default", cfg.Hosts[0].Port)
	}
	if cfg.Hosts[1].ID != "custom-host-1" {
		t.Errorf("host 1 id overwritten: %q", cfg.Hosts[1].ID)
	}
	if cfg.Hosts[2].PrivateKeyHex != seed.PrivateKeyHex() {
		t.Errorf("host 2 key overwritten")
	}
	if cfg.Hosts[2].Address != seed.Address() {
		t.Errorf("host 2 address = %q, want %q", cfg.Hosts[2].Address, seed.Address())
	}
}

// TestGenerateHosts_RejectsMalformedKey ensures operator-provided
// garbage is surfaced as an error rather than silently regenerated —
// otherwise misconfigured keyrings would stay broken for another run.
func TestGenerateHosts_RejectsMalformedKey(t *testing.T) {
	cfg := &config.Config{
		Hosts: []config.HostCfg{
			{PrivateKeyHex: "not-a-hex-key"},
		},
	}
	if err := generateHosts(cfg); err == nil {
		t.Fatal("expected error for malformed key")
	}
}

func TestGenerateUser_GeneratesWhenMissing(t *testing.T) {
	cfg := &config.Config{}
	if err := generateUser(cfg); err != nil {
		t.Fatalf("generateUser: %v", err)
	}
	if cfg.User.PrivateKeyHex == "" || cfg.User.Address == "" {
		t.Fatalf("user not generated: %+v", cfg.User)
	}
	if cfg.User.Port != config.DefaultUserPort {
		t.Errorf("user port = %d, want default", cfg.User.Port)
	}
}

func TestGenerateUser_PreservesExistingKey(t *testing.T) {
	seed, err := signing.GenerateKey()
	if err != nil {
		t.Fatalf("generate seed: %v", err)
	}
	cfg := &config.Config{
		User: config.UserCfg{PrivateKeyHex: seed.PrivateKeyHex()},
	}
	if err := generateUser(cfg); err != nil {
		t.Fatalf("generateUser: %v", err)
	}
	if cfg.User.PrivateKeyHex != seed.PrivateKeyHex() {
		t.Errorf("key overwritten")
	}
	if cfg.User.Address != seed.Address() {
		t.Errorf("address mismatch: %q vs %q", cfg.User.Address, seed.Address())
	}
}

// TestGenerateValidators_FillsPlaceholdersAndDefaults walks a mix of
// empty / TODO / real entries and asserts the fill rules hold without
// changing the count.
func TestGenerateValidators_FillsPlaceholdersAndDefaults(t *testing.T) {
	seed, err := signing.GenerateKey()
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := &config.Config{}
	cfg.HeightSync.Validators = []config.HeightSyncValidatorCfg{
		{PrivateKeyHex: "TODO", Power: 0},
		{PrivateKeyHex: "", Power: 5},
		{PrivateKeyHex: seed.PrivateKeyHex(), Power: 3},
	}
	if err := generateValidators(cfg); err != nil {
		t.Fatalf("generateValidators: %v", err)
	}
	if len(cfg.HeightSync.Validators) != 3 {
		t.Fatalf("count changed: %d", len(cfg.HeightSync.Validators))
	}
	for i, v := range cfg.HeightSync.Validators {
		if v.PrivateKeyHex == "" || isPlaceholderKey(v.PrivateKeyHex) {
			t.Errorf("validator %d still placeholder: %q", i, v.PrivateKeyHex)
		}
		if v.Power <= 0 {
			t.Errorf("validator %d power = %d", i, v.Power)
		}
	}
	if got := cfg.HeightSync.Validators[2].PrivateKeyHex; got != seed.PrivateKeyHex() {
		t.Errorf("validator 2 key overwritten: %q", got)
	}
}

func TestAssignSlots_RoundRobin(t *testing.T) {
	cfg := &config.Config{
		Hosts: []config.HostCfg{
			// pretend previous assignment exists; must be cleared.
			{ID: "h0", SlotIDs: []int{99}},
			{ID: "h1"},
			{ID: "h2"},
		},
		Escrow: config.EscrowCfg{Slots: 7},
	}
	assignSlots(cfg)

	want := [][]int{
		{0, 3, 6},
		{1, 4},
		{2, 5},
	}
	for i, w := range want {
		if !intSliceEq(cfg.Hosts[i].SlotIDs, w) {
			t.Errorf("host %d slots = %v, want %v", i, cfg.Hosts[i].SlotIDs, w)
		}
	}
}

// TestAssignSlots_NoHostsNoPanic guards a trivial corner so
// gencompose fails later in Validate with a readable error rather than
// panicking here.
func TestAssignSlots_NoHostsNoPanic(t *testing.T) {
	cfg := &config.Config{Escrow: config.EscrowCfg{Slots: 4}}
	assignSlots(cfg)
}

func TestFillNetworkDefaults_IPsAndURLs(t *testing.T) {
	cfg := &config.Config{
		Hosts: []config.HostCfg{
			{ID: "devshardd-testenv-0", Port: 8080},
			{ID: "devshardd-testenv-1", Port: 8090, IP: "10.0.0.5"},
		},
		Network: config.NetworkCfg{BaseIP: "192.168.5"},
	}
	fillNetworkDefaults(cfg)

	if cfg.Hosts[0].IP != "192.168.5.10" {
		t.Errorf("host 0 IP = %q", cfg.Hosts[0].IP)
	}
	if cfg.Hosts[0].URL != "http://devshardd-testenv-0:8080" {
		t.Errorf("host 0 URL = %q", cfg.Hosts[0].URL)
	}
	if cfg.Hosts[1].IP != "10.0.0.5" {
		t.Errorf("host 1 IP overwritten: %q", cfg.Hosts[1].IP)
	}
	if cfg.Hosts[1].URL != "http://devshardd-testenv-1:8090" {
		t.Errorf("host 1 URL = %q", cfg.Hosts[1].URL)
	}
}

// TestDefaultConfig_IsValid asserts a zero-arg run that bootstraps
// from the built-in default passes Validate.
func TestDefaultConfig_IsValid(t *testing.T) {
	cfg := defaultConfig()
	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(cfg.Hosts) != 4 {
		t.Errorf("hosts = %d, want 4", len(cfg.Hosts))
	}
	if len(cfg.HeightSync.Validators) != config.DefaultHeightSyncValidators {
		t.Errorf("validators = %d, want %d",
			len(cfg.HeightSync.Validators), config.DefaultHeightSyncValidators)
	}
	if cfg.Escrow.CreatorAddress != cfg.User.Address {
		t.Errorf("creator=%q user=%q (should inherit)", cfg.Escrow.CreatorAddress, cfg.User.Address)
	}
}

// TestWriteCompose_EndToEnd renders compose + config, then re-parses
// them to assert service blocks and env vars landed as intended. A
// round-trip through config.Load is the most robust check that the
// refreshed config.yaml is still valid input for the next gencompose
// run.
func TestWriteCompose_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	outPath := filepath.Join(dir, "docker-compose.yml")

	cfg := defaultConfig()
	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig: %v", err)
	}
	if err := writeCompose(cfg, outPath, filepath.Join(dir, "missing.yaml")); err != nil {
		t.Fatalf("writeCompose: %v", err)
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("Save: %v", err)
	}

	composeBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	compose := string(composeBytes)

	mustContain := []string{
		"networks:",
		"services:",
		"mock-chain:",
		"height-sync:",
		"devshardd-testenv-0:",
		"devshardd-testenv-3:",
		"devshardctl:",
		`- "8081:8080"`, // host user.port → container devshardctl default listen
		`MOCK_CHAIN_URL: "mock-chain:9090"`,
		`HEIGHT_SYNC_URL: "http://height-sync:9100"`,
		`CHAIN_ID: "gonka-testenv-1"`,
		`ESCROW_ID: "1"`,
		`DEVSHARD_ROUTE_PREFIX: "/v1/devshard"`,
		"ipv4_address: 172.30.0.2",  // mock-chain
		"ipv4_address: 172.30.0.3",  // height-sync
		"ipv4_address: 172.30.0.9",  // devshardctl
		"ipv4_address: 172.30.0.10", // host 0
		`EXPORT_METRICS: "1"`,
		`METRICS_PORT: "9600"`,
	}
	for _, needle := range mustContain {
		if !strings.Contains(compose, needle) {
			t.Errorf("compose missing %q", needle)
		}
	}

	var parsed map[string]any
	if err := yaml.Unmarshal(composeBytes, &parsed); err != nil {
		t.Fatalf("compose not valid yaml: %v", err)
	}
	services, _ := parsed["services"].(map[string]any)
	if _, ok := services["devshardd-testenv-2"]; !ok {
		t.Errorf("devshardd-testenv-2 service missing")
	}

	// Re-loading the written config should not fail and should carry
	// real keys (no TODOs left).
	restored, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	for i, v := range restored.HeightSync.Validators {
		if strings.HasPrefix(strings.ToUpper(v.PrivateKeyHex), "TODO") {
			t.Errorf("validator %d still TODO", i)
		}
	}
}

// TestWriteCompose_ObservabilityFragment verifies the optional overlay
// is spliced in with the bare `services:` key stripped so compose
// parses a single top-level mapping.
func TestWriteCompose_ObservabilityFragment(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "docker-compose.yml")
	fragPath := filepath.Join(dir, "observability.yaml")

	fragment := `# observability overlay
services:
  grafana:
    image: grafana/grafana
    networks:
      testenv:
        ipv4_address: 172.30.0.105
`
	if err := os.WriteFile(fragPath, []byte(fragment), 0o644); err != nil {
		t.Fatalf("write fragment: %v", err)
	}

	cfg := defaultConfig()
	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig: %v", err)
	}
	if err := writeCompose(cfg, outPath, fragPath); err != nil {
		t.Fatalf("writeCompose: %v", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read compose: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "grafana:") {
		t.Error("compose missing grafana from observability fragment")
	}
	if strings.Count(out, "\nservices:\n") != 1 {
		t.Errorf("expected exactly one top-level `services:` key, got %d",
			strings.Count(out, "\nservices:\n"))
	}

	// Must still be valid YAML with the overlay merged.
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("compose+overlay not valid yaml: %v", err)
	}
	services, _ := parsed["services"].(map[string]any)
	if _, ok := services["grafana"]; !ok {
		t.Errorf("grafana not merged into services map")
	}
}

// TestWriteCompose_RepoObservabilityFragment checks the checked-in
// observability/compose-fragment.yaml still merges and yields valid YAML.
func TestWriteCompose_RepoObservabilityFragment(t *testing.T) {
	fragPath := filepath.Join("..", "..", "observability", "compose-fragment.yaml")
	if _, err := os.Stat(fragPath); err != nil {
		t.Fatalf("expected repo fragment at %s: %v", fragPath, err)
	}
	dir := t.TempDir()
	outPath := filepath.Join(dir, "docker-compose.yml")
	cfg := defaultConfig()
	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig: %v", err)
	}
	if err := writeCompose(cfg, outPath, fragPath); err != nil {
		t.Fatalf("writeCompose: %v", err)
	}
	data, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "victoria-metrics:") {
		t.Error("compose missing victoria-metrics from repo fragment")
	}
	var parsed map[string]any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("compose+fragment not valid yaml: %v", err)
	}
}

func TestStripServicesKey(t *testing.T) {
	in := "# banner\nservices:\n  grafana:\n    image: grafana/grafana\n"
	got := stripServicesKey(in)
	if strings.Contains(got, "services:\n") {
		t.Errorf("services: not stripped: %q", got)
	}
	if !strings.Contains(got, "grafana:") {
		t.Errorf("grafana entry lost: %q", got)
	}
}

func TestFirstSlotAndSlotList(t *testing.T) {
	if firstSlot(nil) != -1 {
		t.Errorf("firstSlot(nil) != -1")
	}
	if firstSlot([]int{3, 7}) != 3 {
		t.Errorf("firstSlot = %d, want 3", firstSlot([]int{3, 7}))
	}
	if got := slotList(nil); got != "(none)" {
		t.Errorf("slotList(nil) = %q", got)
	}
	if got := slotList([]int{0, 4, 8}); got != "0,4,8" {
		t.Errorf("slotList = %q", got)
	}
}

// TestFillConfig_Idempotent: a second run must not churn keys or
// slot assignments. Seed users typically commit the generated
// config.yaml — regenerating on every merge must be a no-op against
// committed content.
func TestFillConfig_Idempotent(t *testing.T) {
	cfg := defaultConfig()
	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig #1: %v", err)
	}
	first := cloneConfigKeys(cfg)

	if err := fillConfig(cfg); err != nil {
		t.Fatalf("fillConfig #2: %v", err)
	}
	second := cloneConfigKeys(cfg)

	if !stringSliceEq(first, second) {
		t.Errorf("keys churned across runs:\n  first: %v\n  second: %v", first, second)
	}
}

func cloneConfigKeys(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Hosts)+len(cfg.HeightSync.Validators)+1)
	for _, h := range cfg.Hosts {
		out = append(out, h.PrivateKeyHex)
	}
	for _, v := range cfg.HeightSync.Validators {
		out = append(out, v.PrivateKeyHex)
	}
	out = append(out, cfg.User.PrivateKeyHex)
	return out
}

func intSliceEq(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
