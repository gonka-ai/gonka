package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"subnet/bridge"
	"subnet/state"
	"subnet/user"
)

type SettlementJSON struct {
	EscrowID   string              `json:"escrow_id"`
	StateRoot  string              `json:"state_root"`
	Nonce      uint64              `json:"nonce"`
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

func main() {
	fs := flag.NewFlagSet("subnetctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or SUBNET_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", "http://localhost:1317", "chain REST API URL")
	model := fs.String("model", "Qwen/Qwen2.5-7B-Instruct", "default model name")
	port := fs.String("port", "8080", "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to SUBNET_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite path for crash recovery")

	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}

	keyHex := *privateKey
	if keyHex == "" {
		keyHex = os.Getenv("SUBNET_PRIVATE_KEY")
	}
	if keyHex == "" {
		log.Fatal("--private-key flag or SUBNET_PRIVATE_KEY env var required")
	}

	eid := *escrowID
	if eid == "" {
		eid = os.Getenv("SUBNET_ESCROW_ID")
	}
	if eid == "" {
		log.Fatal("--escrow-id flag or SUBNET_ESCROW_ID env var required")
	}

	crest := *chainREST
	if v := os.Getenv("SUBNET_CHAIN_REST"); v != "" && *chainREST == "http://localhost:1317" {
		crest = v
	}

	mdl := *model
	if v := os.Getenv("SUBNET_MODEL"); v != "" && *model == "Qwen/Qwen2.5-7B-Instruct" {
		mdl = v
	}

	p := *port
	if v := os.Getenv("SUBNET_PORT"); v != "" && *port == "8080" {
		p = v
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
		sp = filepath.Join(home, ".cache", "gonka", fmt.Sprintf("subnet-%s.db", eid))
	}

	if err := os.MkdirAll(filepath.Dir(sp), 0755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}

	registry := newStreamRegistry()

	br := bridge.NewRESTBridge(crest)
	cfg := user.HTTPSessionConfig{
		PrivateKeyHex:  keyHex,
		EscrowID:       eid,
		Bridge:         br,
		StoragePath:    sp,
		StreamCallback: registry.callback,
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

	addr := ":" + p
	log.Printf("subnetctl listening on %s (escrow=%s model=%s)", addr, eid, mdl)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	h.Write(hsHash)
	h.Write(p.RestHash)
	h.Write([]byte{0x02})
	root := h.Sum(nil)

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
		EscrowID: p.EscrowID, StateRoot: base64.StdEncoding.EncodeToString(root),
		Nonce: p.Nonce, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, "", "  ")
}
