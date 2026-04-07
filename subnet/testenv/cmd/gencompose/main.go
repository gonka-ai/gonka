// gencompose reads a testenv config YAML, fills in generated keys and addresses,
// assigns slots to participants, and writes docker-compose.yml + an updated config.
//
// Usage:
//
//	gencompose [-config config.yaml] [-out docker-compose.yml]
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"text/template"

	"subnet/signing"
	"subnet/testenv/config"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "input config YAML")
	outPath := flag.String("out", "docker-compose.yml", "output docker-compose file")
	obsFragment := flag.String("obs-fragment", "observability/compose-fragment.yaml",
		"path to the observability compose fragment appended after the generated services")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		// If config doesn't exist, start from defaults.
		log.Printf("config not found at %s, using defaults", *cfgPath)
		cfg = defaultConfig()
	}

	if err := generateParticipants(cfg); err != nil {
		log.Fatalf("generate participants: %v", err)
	}

	if err := generateUser(cfg); err != nil {
		log.Fatalf("generate user key: %v", err)
	}
	if cfg.CreatorAddress == "" {
		cfg.CreatorAddress = cfg.User.Address
	}

	assignSlots(cfg)

	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	if err := writeCompose(cfg, *outPath, *obsFragment); err != nil {
		log.Fatalf("write docker-compose: %v", err)
	}

	if err := cfg.Save(*cfgPath); err != nil {
		log.Fatalf("save config: %v", err)
	}

	log.Printf("wrote %s and updated %s", *outPath, *cfgPath)
	log.Printf("participants: %d  slots: %d  escrow: %s", len(cfg.Participants), cfg.Slots, cfg.EscrowID)
	log.Printf("user.address (subnetctl operator): %s", cfg.User.Address)
	log.Printf("creator_address in GetEscrow: %s", cfg.EffectiveCreatorAddress())
	if cfg.CreatorAddress != "" && cfg.CreatorAddress != cfg.User.Address {
		log.Printf("NOTE: creator_address != user.address — intentional mismatch for testing")
	}
	log.Printf("subnetctl port: %d", cfg.User.Port)
	log.Printf("\nStart with:\n  make build && make up\n")
	log.Printf("subnetctl proxy: http://localhost:%d (started with make up)\n", cfg.User.Port)
}

// defaultConfig builds a config with 10 empty participants and applies defaults.
func defaultConfig() *config.Config {
	cfg := &config.Config{
		Participants: make([]config.Participant, config.DefaultParticipants),
	}
	// applyDefaults is triggered by Load, but since we're not loading from file
	// we call it indirectly via Save + Load. For now replicate the defaults.
	cfg.EscrowID = config.DefaultEscrowID
	cfg.Slots = config.DefaultSlots
	cfg.Amount = config.DefaultAmount
	cfg.TokenPrice = config.DefaultTokenPrice
	cfg.AppHash = config.DefaultAppHash
	cfg.Network.Subnet = config.DefaultNetworkCIDR
	cfg.Network.BaseIP = config.DefaultBaseIP
	cfg.MockServer.Port = config.DefaultMockPort
	cfg.MockServer.IP = config.DefaultMockIP
	cfg.MockServer.Name = config.DefaultMockName
	for i := range cfg.Participants {
		cfg.Participants[i].Port = config.DefaultHostPort
	}
	return cfg
}

// generateParticipants creates secp256k1 keys and derives addresses for any
// participant that is missing a private key. Fills Name and IP fields.
func generateParticipants(cfg *config.Config) error {
	for i := range cfg.Participants {
		p := &cfg.Participants[i]

		if p.PrivateKeyHex == "" {
			signer, err := signing.GenerateKey()
			if err != nil {
				return fmt.Errorf("generate key for participant %d: %w", i, err)
			}
			p.PrivateKeyHex = signer.PrivateKeyHex()
			p.Address = signer.Address()
		} else if p.Address == "" {
			signer, err := signing.SignerFromHex(p.PrivateKeyHex)
			if err != nil {
				return fmt.Errorf("derive address for participant %d: %w", i, err)
			}
			p.Address = signer.Address()
		}

		if p.Name == "" {
			p.Name = fmt.Sprintf("participant-%d", i)
		}
		if p.IP == "" {
			p.IP = fmt.Sprintf("%s.%d", cfg.Network.BaseIP, 10+i)
		}
		if p.Port == 0 {
			p.Port = config.DefaultHostPort
		}
	}
	return nil
}

// generateUser creates a secp256k1 key pair for the subnetctl operator if
// missing. Mock GetEscrow uses config.creator_address when set, else user.address.
func generateUser(cfg *config.Config) error {
	if cfg.User.PrivateKeyHex == "" {
		signer, err := signing.GenerateKey()
		if err != nil {
			return fmt.Errorf("generate user key: %w", err)
		}
		cfg.User.PrivateKeyHex = signer.PrivateKeyHex()
		cfg.User.Address = signer.Address()
	} else if cfg.User.Address == "" {
		signer, err := signing.SignerFromHex(cfg.User.PrivateKeyHex)
		if err != nil {
			return fmt.Errorf("derive user address: %w", err)
		}
		cfg.User.Address = signer.Address()
	}
	if cfg.User.Port == 0 {
		cfg.User.Port = config.DefaultSubnetctlPort
	}
	return nil
}

// assignSlots distributes escrow slots among participants using round-robin.
// slot i → participant i % len(participants).
func assignSlots(cfg *config.Config) {
	// Clear existing assignments.
	for i := range cfg.Participants {
		cfg.Participants[i].SlotIDs = nil
	}
	n := len(cfg.Participants)
	for slot := 0; slot < cfg.Slots; slot++ {
		idx := slot % n
		cfg.Participants[idx].SlotIDs = append(cfg.Participants[idx].SlotIDs, slot)
	}
}

// ── docker-compose template ───────────────────────────────────────────────────

const composeTmpl = `# Auto-generated by gencompose — do not edit manually.
# Re-generate with: make gen-compose
#
# Participants, mock-server, and subnetctl are templated from config.yaml.
# The observability section is appended from observability/compose-fragment.yaml.
#
# Build context is the repository root (../.. from this file) so Dockerfiles can
# COPY subnet/ and subnet/testenv/ (same as Makefile REPO_ROOT + make build).

networks:
  testenv:
    driver: bridge
    ipam:
      config:
        - subnet: {{ .Network.Subnet }}

services:

  # ── Mock mainnet gRPC server ───────────────────────────────────────────────
  {{ .MockServer.Name }}:
    build:
      context: ../..
      dockerfile: subnet/testenv/Dockerfile.mockserver
    image: subnet-mockserver:latest
    volumes:
      - ./config.yaml:/app/config.yaml:ro
    ports:
      - "{{ .MockServer.Port }}:{{ .MockServer.Port }}"
      - "{{ addOne .MockServer.Port }}:{{ addOne .MockServer.Port }}"
    networks:
      testenv:
        ipv4_address: {{ .MockServer.IP }}
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:{{ addOne .MockServer.Port }}/health"]
      interval: 5s
      timeout: 3s
      retries: 10
      start_period: 5s

{{ range .Participants }}
  # ── {{ .Name }} (slots: {{ slotList .SlotIDs }}) ───────────────────────────
  {{ .Name }}:
    build:
      context: ../..
      dockerfile: subnet/testenv/Dockerfile.subnethost
    image: subnet-host:latest
    environment:
      TESTENV_PRIVATE_KEY: "{{ .PrivateKeyHex }}"
      TESTENV_ESCROW_ID: "{{ $.EscrowID }}"
      TESTENV_MOCK_SERVER: "{{ $.MockServer.Name }}:{{ $.MockServer.Port }}"
      TESTENV_PORT: "{{ .Port }}"
    networks:
      testenv:
        ipv4_address: {{ .IP }}
    depends_on:
      {{ $.MockServer.Name }}:
        condition: service_healthy
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://localhost:{{ .Port }}/health"]
      interval: 5s
      timeout: 3s
      retries: 10
      start_period: 10s
{{ end }}

  # ── subnetctl: OpenAI-compatible proxy for the user (escrow owner) ─────────
  # Starts with docker compose up / make up together with mock-server and participants.
  subnetctl:
    build:
      context: ../..
      dockerfile: subnet/testenv/Dockerfile.subnetctl
    image: subnet-subnetctl:latest
    environment:
      TESTENV_ESCROW_ID: "{{ .EscrowID }}"
      TESTENV_MOCK_SERVER: "{{ .MockServer.Name }}:{{ .MockServer.Port }}"
      TESTENV_PORT: "{{ .User.Port }}"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - ./db/subnetctl:/root/.cache/gonka
    ports:
      - "{{ .User.Port }}:{{ .User.Port }}"
    networks:
      testenv:
        ipv4_address: 172.30.0.9
    depends_on:
      {{ .MockServer.Name }}:
        condition: service_healthy
    restart: unless-stopped
`

// stripServicesKey removes the bare `services:` line from a YAML fragment.
// The fragment is a valid standalone YAML file that wraps service entries under
// a `services:` key. When appended to the generated compose file, that key is
// already open, so the duplicate must be stripped.
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

func writeCompose(cfg *config.Config, outPath, obsFragmentPath string) error {
	funcMap := template.FuncMap{
		"addOne": func(n int) int { return n + 1 },
		"slotList": func(ids []int) string {
			parts := make([]string, len(ids))
			for i, id := range ids {
				parts[i] = fmt.Sprintf("%d", id)
			}
			return strings.Join(parts, ",")
		},
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

	// Append the observability fragment if it exists.
	fragment, err := os.ReadFile(obsFragmentPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("observability fragment not found at %s — skipping (observability services will not be included)", obsFragmentPath)
			return nil
		}
		return fmt.Errorf("read observability fragment %s: %w", obsFragmentPath, err)
	}

	// The fragment is a valid standalone YAML file that wraps its service entries
	// under a top-level `services:` key. When appending here, `services:` is
	// already open in the template output, so we drop the bare `services:` line
	// to avoid a duplicate key and let the indented entries continue the mapping.
	content := stripServicesKey(string(fragment))

	if _, err := fmt.Fprintf(f, "\n%s", content); err != nil {
		return fmt.Errorf("append observability fragment: %w", err)
	}
	return nil
}
