package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/state"
	testenvbridge "devshard/testenv/bridge"
	"devshard/types"
	"devshard/user"
)

type SettlementJSON struct {
	EscrowID  string `json:"escrow_id"`
	Version   string `json:"version"`
	StateRoot string `json:"state_root"`
	Nonce     uint64 `json:"nonce"`
	// Fees is the total fee amount deducted during session execution.
	Fees       uint64              `json:"fees"`
	RestHash   string              `json:"rest_hash"`
	HostStats  []HostStatsJSON     `json:"host_stats"`
	Signatures []SlotSignatureJSON `json:"signatures"`
}

type HostStatsJSON struct {
	SlotID               uint32 `json:"slot_id"`
	Missed               uint32 `json:"missed"`
	Invalid              uint32 `json:"invalid"`
	Cost                 uint64 `json:"cost"`
	RequiredValidations  uint32 `json:"required_validations"`
	CompletedValidations uint32 `json:"completed_validations"`
}

type SlotSignatureJSON struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

// Version is the devshardctl release version. Set via ldflags
// -X main.Version=... . Defaults to "dev" for local builds without an override.
var Version = "dev"

// resolvedConfig is the post-flag/env/default view of devshardctl's
// runtime knobs. Extracted from main() so unit tests can pin the
// resolution order without spawning a process.
type resolvedConfig struct {
	KeyHex        string
	EscrowID      string
	Model         string
	Port          string
	StoragePath   string
	ChainRESTURL  string // empty when MockChainURL is set
	MockChainURL  string // non-empty selects testenv gRPC bridge
	PinnedHostURL string // non-empty pins every GetHostInfo to this URL
	RoutePrefix   string
}

// resolveConfig merges CLI flags, env vars, and defaults into a single
// resolved config. Precedence per field: flag (when non-default) → env
// → default. The keyHex and escrowID fields have no "default" — a
// missing value returns an error so misconfig is surfaced once at
// startup rather than in the first request.
//
// Testenv-specific knobs (TESTENV_PRIVATE_KEY, MOCK_CHAIN_URL,
// DEVSHARDD_URL, ESCROW_ID) fall back *after* the prod-equivalent
// knobs (DEVSHARD_PRIVATE_KEY, DEVSHARD_CHAIN_REST, DEVSHARD_ESCROW_ID)
// so a developer exporting both sees the prod values take precedence.
// That ordering matches the existing DEVSHARD_* contract and keeps
// back-compat for anyone who already configured this binary for prod
// use.
func resolveConfig(fs *flagSet, getenv func(string) string) (resolvedConfig, error) {
	cfg := resolvedConfig{}

	// ── keyHex ──────────────────────────────────────────────────────────
	cfg.KeyHex = firstNonEmpty(
		fs.PrivateKey,
		getenv("DEVSHARD_PRIVATE_KEY"),
		getenv("TESTENV_PRIVATE_KEY"),
	)
	if cfg.KeyHex == "" {
		return cfg, errors.New("--private-key flag, DEVSHARD_PRIVATE_KEY, or TESTENV_PRIVATE_KEY required")
	}

	// ── escrowID ────────────────────────────────────────────────────────
	cfg.EscrowID = firstNonEmpty(
		fs.EscrowID,
		getenv("DEVSHARD_ESCROW_ID"),
		getenv("ESCROW_ID"),
	)
	if cfg.EscrowID == "" {
		return cfg, errors.New("--escrow-id flag, DEVSHARD_ESCROW_ID, or ESCROW_ID required")
	}

	// ── bridge selection ────────────────────────────────────────────────
	//
	// mock-chain wins over chain-rest when both are set: testenv
	// operators may have left DEVSHARD_CHAIN_REST at its default from a
	// previous prod session, and we want `--mock-chain` to be the
	// explicit-intent override.
	cfg.MockChainURL = firstNonEmpty(
		fs.MockChainURL,
		getenv("MOCK_CHAIN_URL"),
	)
	if cfg.MockChainURL == "" {
		cfg.ChainRESTURL = fs.ChainREST
		if v := getenv("DEVSHARD_CHAIN_REST"); v != "" && fs.ChainREST == defaultChainREST {
			cfg.ChainRESTURL = v
		}
	}

	// ── pinned host ─────────────────────────────────────────────────────
	cfg.PinnedHostURL = firstNonEmpty(
		fs.Host,
		getenv("DEVSHARDD_URL"),
	)

	// ── model / port / storage ──────────────────────────────────────────
	cfg.Model = fs.Model
	if v := getenv("DEVSHARD_MODEL"); v != "" && fs.Model == defaultModel {
		cfg.Model = v
	}

	cfg.Port = fs.Port
	if v := getenv("DEVSHARD_PORT"); v != "" && fs.Port == defaultPort {
		cfg.Port = v
	}

	cfg.StoragePath = firstNonEmpty(fs.StoragePath, getenv("DEVSHARD_STORAGE_PATH"))
	if cfg.StoragePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		cfg.StoragePath = filepath.Join(home, ".cache", "gonka", fmt.Sprintf("devshard-%s.db", cfg.EscrowID))
	}

	cfg.RoutePrefix = devshardpkg.ResolveVersionedRoutePrefix(Version, getenv("DEVSHARD_ROUTE_PREFIX"))
	return cfg, nil
}

// flagSet is the raw snapshot of CLI flags; isolated from the flag
// package so resolveConfig is easy to call in tests.
type flagSet struct {
	EscrowID     string
	ChainREST    string
	Model        string
	Port         string
	PrivateKey   string
	StoragePath  string
	MockChainURL string
	Host         string
}

const (
	defaultChainREST = "http://localhost:1317"
	defaultModel     = "Qwen/Qwen2.5-7B-Instruct"
	defaultPort      = "8080"
)

func parseFlags(args []string) (*flagSet, error) {
	fs := flag.NewFlagSet("devshardctl", flag.ContinueOnError)
	out := &flagSet{}
	fs.StringVar(&out.EscrowID, "escrow-id", "", "escrow ID (required, or DEVSHARD_ESCROW_ID / ESCROW_ID env)")
	fs.StringVar(&out.ChainREST, "chain-rest", defaultChainREST, "chain REST API URL (prod bridge)")
	fs.StringVar(&out.Model, "model", defaultModel, "default model name")
	fs.StringVar(&out.Port, "port", defaultPort, "listen port")
	fs.StringVar(&out.PrivateKey, "private-key", "", "private key hex (alternative to DEVSHARD_PRIVATE_KEY / TESTENV_PRIVATE_KEY env)")
	fs.StringVar(&out.StoragePath, "storage-path", "", "SQLite path for crash recovery")
	fs.StringVar(&out.MockChainURL, "mock-chain", "", "testenv mock-chain gRPC URL (e.g. mock-chain:9090). When set, overrides --chain-rest and uses the testenv gRPC bridge. Alias of MOCK_CHAIN_URL env.")
	fs.StringVar(&out.Host, "host", "", "pin every host address to this URL (e.g. http://devshard-1:9500). Alias of DEVSHARDD_URL env. Use with testenv to drive a specific devshardd container.")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	return out, nil
}

func main() {
	fs, err := parseFlags(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := resolveConfig(fs, os.Getenv)
	if err != nil {
		log.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(cfg.StoragePath), 0755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	registry := newStreamRegistry()

	br, err := buildBridge(cfg)
	if err != nil {
		log.Fatalf("build bridge: %v", err)
	}

	sessionCfg := user.HTTPSessionConfig{
		PrivateKeyHex:  cfg.KeyHex,
		EscrowID:       cfg.EscrowID,
		Bridge:         br,
		StoragePath:    cfg.StoragePath,
		StreamCallback: registry.callback,
		RoutePrefix:    cfg.RoutePrefix,
	}

	session, sm, err := user.NewHTTPSession(sessionCfg)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer session.Close()

	proxy := &Proxy{
		session:  session,
		sm:       sm,
		escrowID: cfg.EscrowID,
		model:    cfg.Model,
		registry: registry,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("/v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("/v1/status", proxy.handleStatus)
	mux.HandleFunc("/v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("/v1/debug/state", proxy.handleDebugState)
	mux.HandleFunc("/v1/inference", proxy.handleInference)

	addr := ":" + cfg.Port
	log.Printf("devshardctl listening on %s (escrow=%s model=%s bridge=%s host_pin=%s)",
		addr, cfg.EscrowID, cfg.Model, describeBridge(cfg), describeHostPin(cfg))
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

// buildBridge picks between the production REST bridge and the testenv
// gRPC bridge based on which URL is set, then wraps the result with a
// pinned-host decorator when --host / DEVSHARDD_URL is set. Extracted
// so unit tests can exercise the branch decisions without touching
// network code.
func buildBridge(cfg resolvedConfig) (bridge.MainnetBridge, error) {
	var br bridge.MainnetBridge
	switch {
	case cfg.MockChainURL != "":
		// Use a background context because devshardctl is a long-running
		// daemon; the bridge's connection outlives any single request.
		gb, err := testenvbridge.NewGRPCBridge(context.Background(), cfg.MockChainURL)
		if err != nil {
			return nil, fmt.Errorf("dial testenv mock-chain %s: %w", cfg.MockChainURL, err)
		}
		br = gb
	default:
		br = bridge.NewRESTBridge(cfg.ChainRESTURL)
	}

	if cfg.PinnedHostURL != "" {
		br = newPinnedHostBridge(br, cfg.PinnedHostURL)
	}
	return br, nil
}

// describeBridge returns a short human-readable tag for the bridge
// active in cfg, suitable for the startup log line.
func describeBridge(cfg resolvedConfig) string {
	if cfg.MockChainURL != "" {
		return fmt.Sprintf("mock-chain:%s", cfg.MockChainURL)
	}
	return fmt.Sprintf("rest:%s", cfg.ChainRESTURL)
}

// describeHostPin returns "<pinned url>" or "auto" for the log line.
func describeHostPin(cfg resolvedConfig) string {
	if cfg.PinnedHostURL == "" {
		return "auto"
	}
	return cfg.PinnedHostURL
}

// firstNonEmpty returns the first non-empty string from the args.
// Kept inline (no strings dep) because devshardctl imports are already
// dense enough.
func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return nil, err
	}
	root := state.ComputeStateRootFromRestHash(hsHash, p.RestHash, p.Fees, types.PhaseSettlement, p.Version)

	stats := make([]HostStatsJSON, 0, len(p.HostStats))
	for slot, hs := range p.HostStats {
		stats = append(stats, HostStatsJSON{
			SlotID: slot, Missed: hs.Missed, Invalid: hs.Invalid,
			Cost: hs.Cost, RequiredValidations: hs.RequiredValidations,
			CompletedValidations: hs.CompletedValidations,
		})
	}

	sigs := make([]SlotSignatureJSON, 0, len(p.Signatures))
	for slot, sig := range p.Signatures {
		sigs = append(sigs, SlotSignatureJSON{SlotID: slot, Signature: base64.StdEncoding.EncodeToString(sig)})
	}

	return json.MarshalIndent(SettlementJSON{
		EscrowID: p.EscrowID, Version: p.Version, StateRoot: base64.StdEncoding.EncodeToString(root),
		Nonce: p.Nonce, Fees: p.Fees, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, "", "  ")
}
