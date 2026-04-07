// subnetctl is the testenv OpenAI-compatible proxy for the escrow owner (user).
// It reads config.yaml for the user private key, escrow ID, and mock server
// address, creates a GRPCBridge to the mock server, and starts a local HTTP
// proxy that forwards inference requests to subnet participants.
//
// Run inside docker-compose (recommended — Docker DNS resolves participant names):
//
//	docker compose --profile subnetctl run --rm --service-ports subnetctl
//
// Or build locally and run against port-forwarded containers:
//
//	go run ./cmd/subnetctl -config config.yaml
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"subnet/state"
	"subnet/user"

	testenvbridge "subnet/testenv/bridge"
	"subnet/testenv/config"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to testenv config YAML")
	port := flag.Int("port", 0, "listen port (overrides config.user.port)")
	storagePath := flag.String("storage-path", "", "SQLite path for crash recovery (optional)")
	model := flag.String("model", "Qwen/Qwen2.5-7B-Instruct", "default model name")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid config: %v", err)
	}

	// User private key: config only (single source of truth at /app/config.yaml
	// in Docker). This avoids env/config drift during debugging.
	keyHex := cfg.User.PrivateKeyHex
	if keyHex == "" {
		log.Fatal("user private key not set in config.yaml (user.private_key_hex) — run `make gen-compose` first")
	}

	// Mock server address: env → config
	mockServer := os.Getenv("TESTENV_MOCK_SERVER")
	if mockServer == "" {
		mockServer = fmt.Sprintf("%s:%d", cfg.MockServer.Name, cfg.MockServer.Port)
	}

	// Escrow ID: env → config
	escrowID := os.Getenv("TESTENV_ESCROW_ID")
	if escrowID == "" {
		escrowID = cfg.EscrowID
	}

	// Listen port: flag → env → config
	listenPort := *port
	if listenPort == 0 {
		if v := os.Getenv("TESTENV_PORT"); v != "" {
			fmt.Sscanf(v, "%d", &listenPort)
		}
	}
	if listenPort == 0 {
		listenPort = cfg.User.Port
	}

	mdl := *model
	if v := os.Getenv("SUBNET_MODEL"); v != "" {
		mdl = v
	}

	sp := *storagePath
	if sp == "" {
		sp = os.Getenv("SUBNET_STORAGE_PATH")
	}
	if sp == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		sp = filepath.Join(home, ".cache", "gonka", fmt.Sprintf("subnet-%s.db", escrowID))
	}
	if err := os.MkdirAll(filepath.Dir(sp), 0755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	br, err := testenvbridge.NewGRPCBridge(mockServer)
	if err != nil {
		log.Fatalf("dial mock server %s: %v", mockServer, err)
	}

	registry := newStreamRegistry()

	sessionCfg := user.HTTPSessionConfig{
		PrivateKeyHex:  keyHex,
		EscrowID:       escrowID,
		Bridge:         br,
		StoragePath:    sp,
		StreamCallback: registry.callback,
	}

	session, sm, err := user.NewHTTPSession(sessionCfg)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer session.Close()

	proxy := &Proxy{
		session:  session,
		sm:       sm,
		escrowID: escrowID,
		model:    mdl,
		registry: registry,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("/v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("/v1/status", proxy.handleStatus)
	mux.HandleFunc("/v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("/v1/debug/state", proxy.handleDebugState)

	addr := fmt.Sprintf(":%d", listenPort)
	effCreator := cfg.EffectiveCreatorAddress()
	log.Printf("subnetctl listening on %s (escrow=%s operator=%s escrow_creator_in_config=%s model=%s)",
		addr, escrowID, cfg.User.Address, effCreator, mdl)
	if cfg.CreatorAddress != "" && cfg.CreatorAddress != cfg.User.Address {
		log.Printf("subnetctl WARNING: creator_address != user.address — requests use operator key; expect 403 if key does not match escrow creator")
	}
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	return marshalSettlementPayload(p)
}

