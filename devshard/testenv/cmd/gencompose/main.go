// Binary gencompose renders docker-compose.yml from a testenv config
// YAML so adding or removing a host does not require hand-editing the
// compose file.
//
// It is deliberately single-file and has no runtime dependencies beyond
// the repo's signing + config packages:
//
//   - Reads `-config` (default `config.yaml`); falls back to a built-in
//     default when the file is absent so a brand-new clone can bootstrap
//     a full stack with one command.
//   - Fills every missing `private_key_hex` (hosts, user, and every
//     `height_sync.validators[]`), derives the bech32 address, assigns
//     slots round-robin across hosts, and rewrites `config.yaml` so the
//     generated compose file and the pinned validator set agree on keys
//     without an out-of-band channel.
//   - Writes `-out` (default `docker-compose.yml`) with one
//     `mock-chain`, one `height-sync`, N `devshardd-testenv-<i>`, and a
//     `devshardctl` service (operator proxy; same `docker compose up` as hosts).
//   - Appends `-obs-fragment` (default `observability/compose-fragment.yaml`)
//     when present so the observability overlay stays a regeneration-
//     friendly sibling rather than a parallel tree.
//
// See devshard/docs/testenv.md §Phase 10 for the full contract.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"devshard/signing"
	"devshard/testenv/config"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "input config YAML (created if missing)")
	outPath := flag.String("out", "docker-compose.yml", "output docker-compose file")
	obsFragment := flag.String(
		"obs-fragment",
		"observability/compose-fragment.yaml",
		"optional observability fragment appended after generated services",
	)
	flag.Parse()

	cfg, err := loadOrDefault(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if err := fillConfig(cfg); err != nil {
		log.Fatalf("fill config: %v", err)
	}

	if err := cfg.Validate(); err != nil {
		log.Fatalf("validate config: %v", err)
	}

	if err := ensureHostDataBindDirs(cfg.Hosts); err != nil {
		log.Fatalf("host data dirs: %v", err)
	}

	if err := writeCompose(cfg, *outPath, *obsFragment); err != nil {
		log.Fatalf("write docker-compose: %v", err)
	}

	if err := cfg.Save(*cfgPath); err != nil {
		log.Fatalf("save config: %v", err)
	}

	log.Printf("wrote %s and updated %s", *outPath, *cfgPath)
	log.Printf("chain: %s  escrow: %s  hosts: %d  slots: %d  validators: %d",
		cfg.Chain.ID, cfg.Escrow.ID, len(cfg.Hosts), cfg.Escrow.Slots, len(cfg.HeightSync.Validators))
	log.Printf("devshardctl: http://localhost:%d", cfg.User.Port)
	log.Printf("start: docker compose up -d")
}

// ensureHostDataBindDirs creates ./db/<host.ID>/ for each devshardd-testenv
// bind mount in docker-compose.yml. If a path exists but is not a directory
// (common mistake), compose mounts it as a file at /data and SQLite startup
// fails with: mkdir /data/devshardd.db: not a directory.
func ensureHostDataBindDirs(hosts []config.HostCfg) error {
	for _, h := range hosts {
		dir := filepath.Join("db", h.ID)
		if fi, err := os.Stat(dir); err == nil && !fi.IsDir() {
			return fmt.Errorf("%s exists but is not a directory; remove the file and re-run gencompose", dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}

// loadOrDefault loads cfg from path or returns a defaulted config if
// the file is missing. Any other I/O error (permission, malformed YAML)
// is surfaced so operators don't silently regenerate from scratch when
// a config already exists but is unreadable.
func loadOrDefault(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err == nil {
		return cfg, nil
	}
	if !os.IsNotExist(err) && !strings.Contains(err.Error(), "no such file") {
		return nil, err
	}
	log.Printf("config not found at %s, starting from defaults", path)
	return defaultConfig(), nil
}

// defaultConfig returns the bootstrap skeleton when config.yaml is absent:
// 10 hosts, 16 escrow slots (defaults from config.ApplyDefaults), 10 mock-mainnet
// validator slots (filled by fillConfig). For citest / make ci-integration use
// scripts/gen-integration-testenv-config.sh (4 hosts, 4 slots, K=8).
func defaultConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Hosts = make([]config.HostCfg, 10)
	for i := range cfg.Hosts {
		cfg.Hosts[i].ID = fmt.Sprintf("devshardd-testenv-%d", i)
	}
	cfg.HeightSync.Validators = make([]config.HeightSyncValidatorCfg, config.DefaultHeightSyncValidators)
	cfg.ApplyDefaults()
	return cfg
}

// fillConfig mutates cfg in place so that every field gencompose owns
// has a concrete value: host keys and addresses, the user key,
// validator keys, slot assignments, and IP/url defaults.
func fillConfig(cfg *config.Config) error {
	if err := generateHosts(cfg); err != nil {
		return fmt.Errorf("hosts: %w", err)
	}
	if err := generateUser(cfg); err != nil {
		return fmt.Errorf("user: %w", err)
	}
	if err := generateValidators(cfg); err != nil {
		return fmt.Errorf("validators: %w", err)
	}
	if cfg.Escrow.CreatorAddress == "" {
		cfg.Escrow.CreatorAddress = cfg.User.Address
	}
	assignSlots(cfg)
	fillNetworkDefaults(cfg)
	return nil
}

// generateHosts ensures every host has a key, address, name, port, ip,
// and url. A placeholder prefix ("TODO", "CHANGEME", empty) is treated
// as unset so a template file round-trips cleanly.
func generateHosts(cfg *config.Config) error {
	for i := range cfg.Hosts {
		h := &cfg.Hosts[i]

		if isPlaceholderKey(h.PrivateKeyHex) {
			signer, err := signing.GenerateKey()
			if err != nil {
				return fmt.Errorf("generate host %d key: %w", i, err)
			}
			h.PrivateKeyHex = signer.PrivateKeyHex()
			h.Address = signer.Address()
		} else if h.Address == "" {
			signer, err := signing.SignerFromHex(h.PrivateKeyHex)
			if err != nil {
				return fmt.Errorf("parse host %d key: %w", i, err)
			}
			h.Address = signer.Address()
		}
		if h.ID == "" {
			h.ID = fmt.Sprintf("devshardd-testenv-%d", i)
		}
		if h.Port == 0 {
			h.Port = config.DefaultHostPort
		}
	}
	return nil
}

// generateUser ensures the subnetctl operator identity is populated.
func generateUser(cfg *config.Config) error {
	if isPlaceholderKey(cfg.User.PrivateKeyHex) {
		signer, err := signing.GenerateKey()
		if err != nil {
			return fmt.Errorf("generate user key: %w", err)
		}
		cfg.User.PrivateKeyHex = signer.PrivateKeyHex()
		cfg.User.Address = signer.Address()
	} else if cfg.User.Address == "" {
		signer, err := signing.SignerFromHex(cfg.User.PrivateKeyHex)
		if err != nil {
			return fmt.Errorf("parse user key: %w", err)
		}
		cfg.User.Address = signer.Address()
	}
	if cfg.User.Port == 0 {
		cfg.User.Port = config.DefaultUserPort
	}
	return nil
}

// generateValidators fills in TODO/empty keys for every declared
// mock-mainnet validator. It never silently adds or removes entries;
// operators control the count by editing the config skeleton.
func generateValidators(cfg *config.Config) error {
	for i := range cfg.HeightSync.Validators {
		v := &cfg.HeightSync.Validators[i]
		if isPlaceholderKey(v.PrivateKeyHex) {
			signer, err := signing.GenerateKey()
			if err != nil {
				return fmt.Errorf("generate validator %d key: %w", i, err)
			}
			v.PrivateKeyHex = signer.PrivateKeyHex()
		}
		if v.Power <= 0 {
			v.Power = config.DefaultValidatorPower
		}
	}
	return nil
}

// assignSlots distributes the escrow's slots among hosts round-robin
// (slot i → host i % len(hosts)). Clears any previous assignment so
// re-running gencompose is idempotent.
func assignSlots(cfg *config.Config) {
	for i := range cfg.Hosts {
		cfg.Hosts[i].SlotIDs = nil
	}
	n := len(cfg.Hosts)
	if n == 0 {
		return
	}
	for slot := 0; slot < cfg.Escrow.Slots; slot++ {
		idx := slot % n
		cfg.Hosts[idx].SlotIDs = append(cfg.Hosts[idx].SlotIDs, slot)
	}
}

// fillNetworkDefaults stamps per-host ip / url so the compose template
// doesn't need conditional blocks.
func fillNetworkDefaults(cfg *config.Config) {
	base := cfg.Network.BaseIP
	if base == "" {
		base = config.DefaultNetworkBaseIP
	}
	for i := range cfg.Hosts {
		h := &cfg.Hosts[i]
		if h.IP == "" {
			// .10 .. .10+N-1 → leaves .2 for mock-chain, .3 for
			// height-sync, .9 for devshardctl, .100-119 for the
			// observability overlay.
			h.IP = fmt.Sprintf("%s.%d", base, 10+i)
		}
		if h.URL == "" {
			h.URL = fmt.Sprintf("http://%s:%d", h.ID, h.Port)
		}
	}
}

// isPlaceholderKey returns true for values gencompose should overwrite:
// empty strings, whitespace, or strings starting with TODO/CHANGEME.
func isPlaceholderKey(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return true
	}
	up := strings.ToUpper(t)
	return strings.HasPrefix(up, "TODO") || strings.HasPrefix(up, "CHANGEME")
}

// slotList renders []int as a compact comma-separated string for
// comments in the generated compose file.
func slotList(ids []int) string {
	if len(ids) == 0 {
		return "(none)"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = fmt.Sprintf("%d", id)
	}
	return strings.Join(parts, ",")
}

// firstSlot returns the primary slot id for a host (or -1 if none).
// Exposed so the SLOT_INDEX env var stays predictable even if SlotIDs
// is ever reordered.
func firstSlot(ids []int) int {
	if len(ids) == 0 {
		return -1
	}
	return ids[0]
}

// composeTmpl renders the full docker-compose.yml. The template is
// intentionally inline so `go run ./testenv/cmd/gencompose` needs no
// embedded assets.
const composeTmpl = `# Auto-generated by gencompose — do not edit manually.
# Re-generate with: go run ./testenv/cmd/gencompose
#
# Build context is the devshard module root (..). All Dockerfiles live
# in testenv/ and COPY the module verbatim.

networks:
  testenv:
    driver: bridge
    ipam:
      config:
        - subnet: {{ .Network.Subnet }}

services:

  # ── Mock mainnet gRPC server ───────────────────────────────────────────────
  mock-chain:
    build:
      context: ..
      dockerfile: testenv/Dockerfile.mock-chain
    image: devshard-mock-chain:latest
    environment:
      CONFIG_PATH: "/app/config.yaml"
      MOCK_CHAIN_PORT: "{{ .MockChain.Port }}"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
    ports:
      - "{{ .MockChain.Port }}:{{ .MockChain.Port }}"
    networks:
      testenv:
        ipv4_address: 172.30.0.2
    restart: unless-stopped

  # ── Height-sync (mock-mainnet block oracle) ────────────────────────────────
  height-sync:
    build:
      context: ..
      dockerfile: testenv/Dockerfile.height-sync
    image: devshard-height-sync:latest
    environment:
      CONFIG_PATH: "/app/config.yaml"
      HEIGHT_SYNC_PORT: "{{ .HeightSync.Port }}"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
    ports:
      - "{{ .HeightSync.Port }}:{{ .HeightSync.Port }}"
    networks:
      testenv:
        ipv4_address: 172.30.0.3
    depends_on:
      - mock-chain
    restart: unless-stopped
{{ range $i, $h := .Hosts }}
  # ── {{ $h.ID }} (slots: {{ slotList $h.SlotIDs }}) ───────────────────────
  {{ $h.ID }}:
    build:
      context: ..
      dockerfile: testenv/Dockerfile.devshardd-testenv
    image: devshard-devshardd-testenv:latest
    environment:
      TESTENV_PRIVATE_KEY: "{{ $h.PrivateKeyHex }}"
      ESCROW_ID: "{{ $.Escrow.ID }}"
      SLOT_INDEX: "{{ firstSlot $h.SlotIDs }}"
      MOCK_CHAIN_URL: "mock-chain:{{ $.MockChain.Port }}"
      HEIGHT_SYNC_URL: "http://height-sync:{{ $.HeightSync.Port }}"
      HEIGHT_SYNC_ANCHOR_PERIOD_NONCES: "{{ $.HeightSync.AnchorPeriodNonces }}"
      HEIGHT_SYNC_SYNC_TURN_SLOTS: "{{ $.HeightSync.SyncTurnSlots }}"
      CHAIN_ID: "{{ $.Chain.ID }}"
      HTTP_PORT: "{{ $h.Port }}"
      DATA_DIR: "/data"
      EXPORT_METRICS: "1"
      METRICS_PORT: "9600"
      LOG_LEVEL: "debug"
      TESTENV_JSON_LOGS: "1"
      DEVSHARDD_DEBUG: "1"
      MOCKDAPI_STALE_AFTER: "3s"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./db/{{ $h.ID }}:/data
    networks:
      testenv:
        ipv4_address: {{ $h.IP }}
    ports:
      - "127.0.0.1:{{ $h.PublicMetricsPort }}:9600"
    depends_on:
      - mock-chain
      - height-sync
    restart: unless-stopped
{{ end }}
  # ── devshardctl (operator CLI proxy; always part of default compose up) ─────
  devshardctl:
    build:
      context: ..
      dockerfile: testenv/Dockerfile.devshardctl
    image: devshard-devshardctl:latest
    environment:
      TESTENV_PRIVATE_KEY: "{{ .User.PrivateKeyHex }}"
      ESCROW_ID: "{{ .Escrow.ID }}"
      MOCK_CHAIN_URL: "mock-chain:{{ .MockChain.Port }}"
      # devshardctl uses grpc GetHostInfo → each host url from config.yaml (mock-chain).
      # Hosts mount transport under /v1/devshard (see devshardd-testenv); default Version=dev would use /devshard/dev.
      DEVSHARD_ROUTE_PREFIX: "/v1/devshard"
      CONFIG_PATH: "/app/config.yaml"
      # Courier user: no HEIGHT_SYNC_URL / local oracle — peer tips from host Anchors only.
      HEIGHT_SYNC_ANCHOR_PERIOD_NONCES: "{{ $.HeightSync.AnchorPeriodNonces }}"
      HEIGHT_SYNC_SYNC_TURN_SLOTS: "{{ $.HeightSync.SyncTurnSlots }}"
      DEVSHARDCTL_DEBUG: "1"
      MOCKDAPI_STALE_AFTER: "3s"
      LOG_LEVEL: "debug"
      TESTENV_JSON_LOGS: "1"
      # Persist on host so reuse-stack tests can wipe session state per test (see resetSharedStackHostDB).
      DEVSHARD_STORAGE_PATH: "/data/session.db"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./db/devshardctl:/data
    ports:
      # Host publishes config user.port; container listens on devshardctl default (:8080).
      - "{{ .User.Port }}:8080"
    networks:
      testenv:
        ipv4_address: 172.30.0.9
    depends_on:
      - mock-chain
{{- range $i, $h := .Hosts }}
      - {{ $h.ID }}
{{- end }}
    restart: unless-stopped
`

// stripServicesKey removes the bare `services:` line from a YAML
// fragment. The fragment is a valid standalone YAML file that wraps
// service entries under a `services:` key; when appended to our
// generated output, that key is already open, so the duplicate would
// trip compose's parser.
func stripServicesKey(content string) string {
	lines := strings.Split(content, "\n")
	out := make([]string, 0, len(lines))
	stripped := false
	for _, line := range lines {
		if !stripped && strings.TrimSpace(line) == "services:" {
			stripped = true
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// writeCompose renders the compose template plus the optional
// observability overlay into outPath.
func writeCompose(cfg *config.Config, outPath, obsFragmentPath string) error {
	funcMap := template.FuncMap{
		"slotList":  slotList,
		"firstSlot": firstSlot,
	}
	tmpl, err := template.New("compose").Funcs(funcMap).Parse(composeTmpl)
	if err != nil {
		return fmt.Errorf("parse template: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create %s: %w", outPath, err)
	}
	defer f.Close()

	if err := tmpl.Execute(f, cfg); err != nil {
		return fmt.Errorf("execute template: %w", err)
	}

	fragment, err := os.ReadFile(obsFragmentPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("observability fragment not found at %s — skipping (observability services will not be included)", obsFragmentPath)
			return nil
		}
		return fmt.Errorf("read observability fragment %s: %w", obsFragmentPath, err)
	}
	if _, err := fmt.Fprintf(f, "\n%s", stripServicesKey(string(fragment))); err != nil {
		return fmt.Errorf("append observability fragment: %w", err)
	}
	composeDir := filepath.Dir(outPath)
	if err := ensureObsDataBindDirs(composeDir); err != nil {
		return fmt.Errorf("obs bind dirs: %w", err)
	}
	return nil
}

// ensureObsDataBindDirs creates ./obs-data/<svc>/ under composeDir for the
// observability fragment bind mounts (default TESTENV_OBS_REL_SUBDIR in compose).
func ensureObsDataBindDirs(composeDir string) error {
	base := filepath.Join(composeDir, "obs-data")
	for _, sub := range []string{"victoria-metrics", "loki", "grafana", "alloy"} {
		dir := filepath.Join(base, sub)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	return nil
}
