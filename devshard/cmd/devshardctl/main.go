package main

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/state"
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

func main() {
	fs := flag.NewFlagSet("devshardctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or DEVSHARD_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", "http://localhost:1317", "chain REST API URL")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "default model name")
	port := fs.String("port", "8080", "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to DEVSHARD_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite path for crash recovery")

	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	keyHex := *privateKey
	if keyHex == "" {
		keyHex = os.Getenv("DEVSHARD_PRIVATE_KEY")
	}
	if keyHex == "" {
		log.Fatal("--private-key flag or DEVSHARD_PRIVATE_KEY env var required")
	}

	eid := *escrowID
	if eid == "" {
		eid = os.Getenv("DEVSHARD_ESCROW_ID")
	}
	if eid == "" {
		log.Fatal("--escrow-id flag or DEVSHARD_ESCROW_ID env var required")
	}

	crest := *chainREST
	if v := os.Getenv("DEVSHARD_CHAIN_REST"); v != "" && *chainREST == "http://localhost:1317" {
		crest = v
	}

	mdl := *model
	if v := os.Getenv("DEVSHARD_MODEL"); v != "" && *model == "Qwen/Qwen2.5-7B-Instruct" {
		mdl = v
	}

	p := *port
	if v := os.Getenv("DEVSHARD_PORT"); v != "" && *port == "8080" {
		p = v
	}

	sp := *storagePath
	if sp == "" {
		sp = os.Getenv("DEVSHARD_STORAGE_PATH")
	}
	if sp == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		sp = filepath.Join(home, ".cache", "gonka", fmt.Sprintf("devshard-%s.db", eid))
	}

	if err := os.MkdirAll(filepath.Dir(sp), 0755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	registry := newStreamRegistry()

	br := bridge.NewRESTBridge(crest)
	// DEVSHARD_ROUTE_PREFIX selects which HTTP path prefix to use when
	// reaching devshard hosts. Default empty -> the embedded build-time
	// versioned route. Set it explicitly for tests or local debugging.
	routePrefix := devshardpkg.ResolveVersionedRoutePrefix(Version, os.Getenv("DEVSHARD_ROUTE_PREFIX"))
	cfg := user.HTTPSessionConfig{
		PrivateKeyHex:  keyHex,
		EscrowID:       eid,
		Bridge:         br,
		StoragePath:    sp,
		StreamCallback: registry.callback,
		RoutePrefix:    routePrefix,
	}

	session, sm, err := user.NewHTTPSession(cfg)
	if err != nil {
		log.Fatalf("create session: %v", err)
	}
	defer session.Close()

	proxy := &Proxy{
		session:  session,
		sm:       sm,
		escrowID: eid,
		model:    mdl,
		registry: registry,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("/v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("/v1/status", proxy.handleStatus)
	mux.HandleFunc("/v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("/v1/debug/state", proxy.handleDebugState)
	mux.HandleFunc("/v1/inference", proxy.handleInference)

	addr := ":" + p
	log.Printf("devshardctl listening on %s (escrow=%s model=%s)", addr, eid, mdl)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
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
