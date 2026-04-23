package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"subnet/bridge"
	"subnet/state"
)

const (
	defaultChainRESTURL = "http://localhost:1317"
	defaultPublicAPIURL = "http://localhost:9000"
	defaultModelName    = "Qwen/Qwen2.5-7B-Instruct"
	defaultListenPort   = "8080"
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

type cliFlags struct {
	escrowID    string
	chainREST   string
	publicAPI   string
	model       string
	port        string
	privateKey  string
	storagePath string
	storageDir  string
}

type runtimeOptions struct {
	port           string
	baseStorageDir string
	apiKeys        map[string]struct{}
	adminAPIKey    string
}

type bootstrapOptions struct {
	escrowID          string
	privateKeyHex     string
	chainREST         string
	publicAPI         string
	defaultModel      string
	storagePath       string
	baseStorageDir    string
	multiMode         bool
	bootstrapSettings GatewaySettings
}

var gatewayRuntimeBuilder = buildRuntime

func main() {
	ConfigurePoCRequestMode(os.Getenv("SUBNET_POC_REQUEST_MODE"))
	ConfigureCapacityAwareLimits(os.Getenv("SUBNET_CAPACITY_AWARE_LIMITS"))
	flags := parseCLIFlags()
	runtimeOpts := mustLoadRuntimeOptions(flags)
	gatewayStore := mustOpenGatewayStore(runtimeOpts.baseStorageDir)
	defer func() {
		if err := gatewayStore.Close(); err != nil {
			log.Printf("close gateway state: %v", err)
		}
	}()

	// Startup is intentionally two-phase:
	// 1. Read only runtime env needed to locate/open gateway.db and configure auth/listen.
	// 2. Bootstrap subnet topology/settings from env only when gateway.db does not exist yet.
	gatewayState, hasState := mustLoadPersistedGatewayState(gatewayStore)
	if !hasState {
		bootstrapOpts := mustLoadBootstrapOptions(flags, runtimeOpts.baseStorageDir)
		mustBootstrapGatewayState(gatewayStore, bootstrapOpts)
		gatewayState = mustReloadGatewayState(gatewayStore)
	}

	mustLoadParticipantThrottleState(gatewayStore)

	gateway := mustBuildGateway(gatewayStore, gatewayState, runtimeOpts.baseStorageDir)
	defer gateway.Close()

	handler := buildGatewayHandler(gateway, runtimeOpts)
	serveGateway(handler, runtimeOpts.port, len(gateway.runtimeOrder))
}

func mustLoadRuntimeOptions(flags cliFlags) runtimeOptions {
	opts := runtimeOptions{
		port:           envOverride(flags.port, os.Getenv("SUBNET_PORT"), defaultListenPort),
		baseStorageDir: resolveBaseStorageDir(flags.storageDir, firstNonEmpty(flags.storagePath, os.Getenv("SUBNET_STORAGE_PATH"))),
		apiKeys:        parseAPIKeys(os.Getenv("SUBNET_API_KEYS")),
		adminAPIKey:    strings.TrimSpace(os.Getenv("SUBNET_ADMIN_API_KEY")),
	}
	if err := os.MkdirAll(opts.baseStorageDir, 0o755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}
	return opts
}

func mustLoadBootstrapOptions(flags cliFlags, baseStorageDir string) bootstrapOptions {
	opts := bootstrapOptions{
		multiMode:      strings.TrimSpace(os.Getenv("SUBNETS_JSON")) != "",
		escrowID:       firstNonEmpty(flags.escrowID, os.Getenv("SUBNET_ESCROW_ID")),
		privateKeyHex:  firstNonEmpty(flags.privateKey, os.Getenv("SUBNET_PRIVATE_KEY")),
		chainREST:      envOverride(flags.chainREST, os.Getenv("SUBNET_CHAIN_REST"), defaultChainRESTURL),
		publicAPI:      envOverride(flags.publicAPI, os.Getenv("SUBNET_PUBLIC_API"), defaultPublicAPIURL),
		defaultModel:   envOverride(flags.model, os.Getenv("SUBNET_MODEL"), defaultModelName),
		storagePath:    firstNonEmpty(flags.storagePath, os.Getenv("SUBNET_STORAGE_PATH")),
		baseStorageDir: baseStorageDir,
	}
	if !opts.multiMode {
		requireNonEmpty(opts.privateKeyHex, "--private-key flag or SUBNET_PRIVATE_KEY env var required")
		requireNonEmpty(opts.escrowID, "--escrow-id flag or SUBNET_ESCROW_ID env var required")
	}
	if opts.storagePath == "" && !opts.multiMode {
		opts.storagePath = defaultStoragePath(opts.baseStorageDir, opts.escrowID)
	}
	opts.bootstrapSettings = GatewaySettings{
		ChainREST:               opts.chainREST,
		PublicAPI:               opts.publicAPI,
		DefaultModel:            opts.defaultModel,
		DefaultRequestMaxTokens: uint64(readInt64Env("GATEWAY_DEFAULT_MAX_TOKENS", int64(DefaultRequestMaxTokens))),
		MaxConcurrentRequests:   readInt64Env("GATEWAY_MAX_CONCURRENT_REQUESTS", 0),
		MaxInputTokensInFlight:  readInt64Env("GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT", 0),
	}
	return opts
}

func parseCLIFlags() cliFlags {
	fs := flag.NewFlagSet("subnetctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or SUBNET_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", defaultChainRESTURL, "chain REST API URL")
	publicAPI := fs.String("public-api", defaultPublicAPIURL, "public API URL used for epoch/PoC phase checks")
	model := fs.String("model", defaultModelName, "default model name")
	port := fs.String("port", defaultListenPort, "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to SUBNET_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite path for crash recovery")
	storageDir := fs.String("storage-dir", "", "base directory for multi-subnet SQLite files")
	if err := fs.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	return cliFlags{
		escrowID:    *escrowID,
		chainREST:   *chainREST,
		publicAPI:   *publicAPI,
		model:       *model,
		port:        *port,
		privateKey:  *privateKey,
		storagePath: *storagePath,
		storageDir:  *storageDir,
	}
}

func resolveBaseStorageDir(flagStorageDir, storagePath string) string {
	baseStorageDir := firstNonEmpty(flagStorageDir, os.Getenv("SUBNET_STORAGE_DIR"))
	if baseStorageDir == "" {
		if storagePath != "" {
			baseStorageDir = filepath.Dir(storagePath)
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				home = "/tmp"
			}
			baseStorageDir = filepath.Join(home, ".cache", "gonka")
		}
	}
	return baseStorageDir
}

func mustLoadParticipantThrottleState(store *GatewayStore) {
	sharedParticipantRequestLimiter.SetStore(store)
	throttles, err := store.LoadParticipantThrottles()
	if err != nil {
		log.Printf("load participant throttle state: %v", err)
		return
	}
	for _, t := range throttles {
		sharedParticipantRequestLimiter.LoadStateWithQuarantine(t.Key, t.Tokens, t.LastRefillAt, t.Status, t.QuarantineUntil, t.EmptyStreamStreak)
	}
	if len(throttles) > 0 {
		log.Printf("loaded %d persisted participant throttle state(s)", len(throttles))
	}
}

func mustOpenGatewayStore(baseStorageDir string) *GatewayStore {
	gatewayStore, err := NewGatewayStore(filepath.Join(baseStorageDir, "gateway.db"))
	if err != nil {
		log.Fatalf("open gateway state: %v", err)
	}
	return gatewayStore
}

func mustLoadPersistedGatewayState(gatewayStore *GatewayStore) (GatewayState, bool) {
	gatewayState, hasState, err := gatewayStore.LoadState()
	if err != nil {
		log.Fatalf("load gateway state: %v", err)
	}
	return gatewayState, hasState
}

func mustReloadGatewayState(gatewayStore *GatewayStore) GatewayState {
	gatewayState, hasState, err := gatewayStore.LoadState()
	if err != nil {
		log.Fatalf("reload gateway state: %v", err)
	}
	if !hasState {
		log.Fatal("gateway state missing after initialization")
	}
	return gatewayState
}

func mustBootstrapGatewayState(gatewayStore *GatewayStore, opts bootstrapOptions) {
	runtimeCfgs, err := resolveRuntimeConfigs(opts.escrowID, opts.privateKeyHex, opts.defaultModel, opts.storagePath)
	if err != nil {
		log.Fatal(err)
	}
	runtimeCfgs, err = finalizeRuntimeConfigs(runtimeCfgs, opts.defaultModel, opts.baseStorageDir)
	if err != nil {
		log.Fatal(err)
	}
	subnets := make([]GatewaySubnetState, 0, len(runtimeCfgs))
	for _, cfg := range runtimeCfgs {
		subnets = append(subnets, GatewaySubnetState{
			RuntimeConfig: cfg,
			Active:        true,
		})
	}
	if err := gatewayStore.Initialize(opts.bootstrapSettings, subnets); err != nil {
		log.Fatalf("initialize gateway state: %v", err)
	}
}

func mustBuildGateway(gatewayStore *GatewayStore, gatewayState GatewayState, baseStorageDir string) *Gateway {
	DefaultRequestMaxTokens = gatewayState.Settings.DefaultRequestMaxTokens

	runtimes, err := buildGatewayRuntimes(gatewayStore, &gatewayState, baseStorageDir)
	if err != nil {
		log.Fatalf("create runtimes: %v", err)
	}
	limiter := NewGatewayLimiter(
		gatewayState.Settings.MaxConcurrentRequests,
		gatewayState.Settings.MaxInputTokensInFlight,
	)
	return NewManagedGateway(runtimes, limiter, gatewayState.Settings, baseStorageDir, gatewayStore)
}

func buildGatewayRuntimes(gatewayStore *GatewayStore, gatewayState *GatewayState, baseStorageDir string) ([]*subnetRuntime, error) {
	activeCfgs := make([]RuntimeConfig, 0, len(gatewayState.Subnets))
	for _, subnet := range gatewayState.Subnets {
		if subnet.Active {
			activeCfgs = append(activeCfgs, subnet.RuntimeConfig)
		}
	}
	activeCfgs, err := finalizeRuntimeConfigs(activeCfgs, gatewayState.Settings.DefaultModel, baseStorageDir)
	if err != nil {
		return nil, fmt.Errorf("finalize gateway runtime configs: %w", err)
	}

	runtimes := make([]*subnetRuntime, 0, len(activeCfgs))
	for _, cfg := range activeCfgs {
		rt, err := gatewayRuntimeBuilder(cfg, gatewayState.Settings.ChainREST, gatewayState.Settings.DefaultModel)
		if err != nil {
			if errors.Is(err, bridge.ErrEscrowNotFound) {
				log.Printf("subnet %s escrow missing on chain, marking inactive and skipping runtime: %v", cfg.ID, err)
				if gatewayStore != nil {
					if deactivateErr := gatewayStore.SetSubnetActive(cfg.ID, false); deactivateErr != nil {
						closeRuntimes(runtimes)
						return nil, fmt.Errorf("deactivate subnet %s: %w", cfg.ID, deactivateErr)
					}
				}
				markSubnetInactive(gatewayState, cfg.ID)
				continue
			}
			closeRuntimes(runtimes)
			return nil, err
		}
		runtimes = append(runtimes, rt)
	}
	return runtimes, nil
}

func markSubnetInactive(gatewayState *GatewayState, id string) {
	if gatewayState == nil {
		return
	}
	for i := range gatewayState.Subnets {
		if gatewayState.Subnets[i].ID == id {
			gatewayState.Subnets[i].Active = false
			return
		}
	}
}

func closeRuntimes(runtimes []*subnetRuntime) {
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		if err := rt.close(); err != nil {
			log.Printf("close subnet %s: %v", rt.id, err)
		}
	}
}

func buildGatewayHandler(gateway *Gateway, opts runtimeOptions) http.Handler {
	var handler http.Handler = gateway.Handler()
	if len(opts.apiKeys) > 0 {
		log.Printf("API key auth enabled (%d key(s))", len(opts.apiKeys))
		handler = bearerAuthMiddleware(opts.apiKeys, handler)
	}
	handler = adminAuthMiddleware(opts.adminAPIKey, handler)
	return gateway.metrics.Wrap(handler)
}

func serveGateway(handler http.Handler, port string, runtimeCount int) {
	addr := ":" + port
	log.Printf("subnetctl gateway listening on %s (subnets=%d default_max_tokens=%d)", addr, runtimeCount, DefaultRequestMaxTokens)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server: %v", err)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requireNonEmpty(value, message string) {
	if strings.TrimSpace(value) == "" {
		log.Fatal(message)
	}
}

func envOverride(flagValue, envValue, defaultValue string) string {
	if strings.TrimSpace(envValue) != "" && flagValue == defaultValue {
		return strings.TrimSpace(envValue)
	}
	return flagValue
}

func parseAPIKeys(raw string) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k != "" {
			keys[k] = struct{}{}
		}
	}
	return keys
}

func isAuthExemptPath(path string) bool {
	return path == "/metrics" ||
		path == "/v1/status" ||
		strings.HasSuffix(path, "/v1/status") ||
		strings.HasPrefix(path, "/v1/admin/")
}

func bearerAuthMiddleware(validKeys map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isAuthExemptPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Missing or invalid Authorization header. Expected: Bearer <api-key>","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}

		key := strings.TrimPrefix(auth, "Bearer ")
		if _, ok := validKeys[key]; !ok {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Invalid API key provided.","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func adminAuthMiddleware(adminKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/admin/") {
			next.ServeHTTP(w, r)
			return
		}
		if adminKey == "" {
			http.NotFound(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") || strings.TrimPrefix(auth, "Bearer ") != adminKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Invalid admin API key.","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func readInt64Env(name string, fallback int64) int64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	var v int64
	if _, err := fmt.Sscan(raw, &v); err != nil {
		log.Printf("invalid %s=%q, using %d", name, raw, fallback)
		return fallback
	}
	return v
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
