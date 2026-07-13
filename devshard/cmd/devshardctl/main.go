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
	"strings"
	"time"

	"devshard/bridge"
	"devshard/state"
	"devshard/types"
)

type adminAuthContextKey struct{}
type adminAPIKeySuffixContextKey struct{}

const (
	defaultChainRESTURL          = "http://localhost:1317"
	defaultPublicAPIURL          = "http://localhost:9000"
	defaultModelName             = "Qwen/Qwen3-235B-A22B-Instruct-2507-FP8"
	defaultListenPort            = "8080"
	defaultMaxConcurrentRequests = 512
)

type SettlementJSON struct {
	EscrowID string `json:"escrow_id"`
	// "version" name is used for compatibility with devshardctl v1
	StateRootAndProtocolVersion string `json:"version"`
	StateRoot                   string `json:"state_root"`
	Nonce                       uint64 `json:"nonce"`
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
	RequiredValidations  uint32 `json:"required_validations,omitempty"`
	CompletedValidations uint32 `json:"completed_validations,omitempty"`
}

func hostStatsJSONFromDomain(slot uint32, hs *types.HostStats) HostStatsJSON {
	entry := HostStatsJSON{
		SlotID:  slot,
		Missed:  hs.Missed,
		Invalid: hs.Invalid,
		Cost:    hs.Cost,
	}
	if hs.RequiredValidations != 0 {
		entry.RequiredValidations = hs.RequiredValidations
	}
	if hs.CompletedValidations != 0 {
		entry.CompletedValidations = hs.CompletedValidations
	}
	return entry
}

type SlotSignatureJSON struct {
	SlotID    uint32 `json:"slot_id"`
	Signature string `json:"signature"`
}

// Version is the devshardctl release version. Set via ldflags
// -X main.Version=... . Defaults to "dev" for local builds without an override.
var Version = "dev"

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

type gatewayAccessMode string

const (
	gatewayAccessModeOpen      gatewayAccessMode = "open"
	gatewayAccessModeAPIKey    gatewayAccessMode = "api_key"
	gatewayAccessModeAdminOnly gatewayAccessMode = "admin_only"
)

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
	ConfigurePoCRequestMode(os.Getenv("DEVSHARD_POC_REQUEST_MODE"))
	ConfigureCapacityAwareLimits(os.Getenv("DEVSHARD_CAPACITY_AWARE_LIMITS"))
	configureClassifyCapsFromEnv()
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
	// 2. Bootstrap devshard topology/settings from env only when gateway.db does not exist yet.
	gatewayState, hasState := mustLoadPersistedGatewayState(gatewayStore)
	if !hasState {
		bootstrapOpts := mustLoadBootstrapOptions(flags, runtimeOpts.baseStorageDir)
		mustBootstrapGatewayState(gatewayStore, bootstrapOpts)
		gatewayState = mustReloadGatewayState(gatewayStore)
	}
	mustRepairPersistedGatewayEndpointSettings(gatewayStore, &gatewayState, flags)

	mustLoadParticipantThrottleState(gatewayStore)

	gateway := mustBuildGateway(gatewayStore, gatewayState, runtimeOpts.baseStorageDir)
	defer gateway.Close()

	handler := buildGatewayHandler(gateway, runtimeOpts)
	serveGateway(handler, runtimeOpts.port, len(gateway.runtimeOrder))
}

func mustLoadRuntimeOptions(flags cliFlags) runtimeOptions {
	opts := runtimeOptions{
		port:           envOverride(flags.port, os.Getenv("DEVSHARD_PORT"), defaultListenPort),
		baseStorageDir: resolveBaseStorageDir(flags.storageDir, firstNonEmpty(flags.storagePath, os.Getenv("DEVSHARD_STORAGE_PATH"))),
		apiKeys:        parseAPIKeys(os.Getenv("DEVSHARD_API_KEYS")),
		adminAPIKey:    strings.TrimSpace(os.Getenv("DEVSHARD_ADMIN_API_KEY")),
	}
	if err := os.MkdirAll(opts.baseStorageDir, 0o755); err != nil {
		log.Fatalf("create storage dir: %v", err)
	}
	configureRequestCaptureStore(opts.baseStorageDir)
	return opts
}

func mustLoadBootstrapOptions(flags cliFlags, baseStorageDir string) bootstrapOptions {
	opts := bootstrapOptions{
		multiMode:      strings.TrimSpace(os.Getenv("DEVSHARDS_JSON")) != "",
		escrowID:       firstNonEmpty(flags.escrowID, os.Getenv("DEVSHARD_ESCROW_ID")),
		privateKeyHex:  firstNonEmpty(flags.privateKey, os.Getenv("DEVSHARD_PRIVATE_KEY")),
		chainREST:      envOverride(flags.chainREST, os.Getenv("DEVSHARD_CHAIN_REST"), defaultChainRESTURL),
		publicAPI:      envOverride(flags.publicAPI, os.Getenv("DEVSHARD_PUBLIC_API"), defaultPublicAPIURL),
		defaultModel:   envOverride(flags.model, os.Getenv("DEVSHARD_MODEL"), defaultModelName),
		storagePath:    firstNonEmpty(flags.storagePath, os.Getenv("DEVSHARD_STORAGE_PATH")),
		baseStorageDir: baseStorageDir,
	}
	if !opts.multiMode {
		requireNonEmpty(opts.privateKeyHex, "--private-key flag or DEVSHARD_PRIVATE_KEY env var required")
		requireNonEmpty(opts.escrowID, "--escrow-id flag or DEVSHARD_ESCROW_ID env var required")
	}
	if opts.storagePath == "" && !opts.multiMode {
		opts.storagePath = defaultStoragePath(opts.baseStorageDir, opts.escrowID)
	}
	opts.bootstrapSettings = GatewaySettings{
		ChainREST:                      opts.chainREST,
		PublicAPI:                      opts.publicAPI,
		DefaultModel:                   opts.defaultModel,
		DefaultRequestMaxTokens:        uint64(readInt64Env("GATEWAY_DEFAULT_MAX_TOKENS", int64(DefaultRequestMaxTokens))),
		RequestMaxTokensCap:            uint64(readInt64Env("GATEWAY_MAX_TOKENS_CAP", int64(RequestMaxTokensCap))),
		MaxConcurrentRequests:          readInt64Env("GATEWAY_MAX_CONCURRENT_REQUESTS", defaultMaxConcurrentRequests),
		MaxConcurrentPer10000Weight:    readFloat64Env("GATEWAY_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", defaultMaxConcurrentPer10000Weight),
		PoCMaxConcurrentPer10000Weight: readFloat64Env("GATEWAY_POC_MAX_CONCURRENT_REQUESTS_PER_10000_WEIGHT", defaultPoCMaxConcurrentPer10000Weight),
		MaxInputTokensInFlight:         readInt64Env("GATEWAY_MAX_INPUT_TOKENS_IN_FLIGHT", 0),
		TxGasLimit:                     uint64(readInt64Env("DEVSHARD_TX_GAS_LIMIT", 0)),
		Disabled: GatewayDisabledSettings{
			Enabled: readBoolEnv("DEVSHARD_GATEWAY_DISABLED", false),
			Message: os.Getenv("DEVSHARD_GATEWAY_DISABLED_MESSAGE"),
			NewURL:  os.Getenv("DEVSHARD_GATEWAY_DISABLED_NEW_URL"),
		},
		EscrowRotation: EscrowRotationSettings{
			Enabled:           readBoolEnv("DEVSHARD_ESCROW_ROTATION_ENABLED", false),
			SettlementEnabled: readBoolEnv("DEVSHARD_ESCROW_ROTATION_SETTLEMENT_ENABLED", false),
			PrePoCBlocks:      readInt64Env("DEVSHARD_ESCROW_ROTATION_PRE_POC_BLOCKS", 300),
			Models:            mustReadEscrowRotationModelsEnv(),
		},
	}.WithTuningDefaults()
	return opts
}

func mustReadEscrowRotationModelsEnv() []EscrowRotationModelSettings {
	raw := strings.TrimSpace(os.Getenv("DEVSHARD_ESCROW_ROTATION_MODELS_JSON"))
	if raw == "" {
		return nil
	}
	var models []EscrowRotationModelSettings
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		log.Fatalf("invalid DEVSHARD_ESCROW_ROTATION_MODELS_JSON: %v", err)
	}
	return models
}

func parseCLIFlags() cliFlags {
	fs := flag.NewFlagSet("devshardctl", flag.ExitOnError)
	escrowID := fs.String("escrow-id", "", "escrow ID (required, or DEVSHARD_ESCROW_ID env)")
	chainREST := fs.String("chain-rest", defaultChainRESTURL, "chain REST API URL")
	publicAPI := fs.String("public-api", defaultPublicAPIURL, "public API URL used for epoch/PoC phase checks")
	model := fs.String("model", defaultModelName, "default model name")
	port := fs.String("port", defaultListenPort, "listen port")
	privateKey := fs.String("private-key", "", "private key hex (alternative to DEVSHARD_PRIVATE_KEY env)")
	storagePath := fs.String("storage-path", "", "SQLite storage directory for crash recovery")
	storageDir := fs.String("storage-dir", "", "base directory for multi-devshard SQLite files")
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
	baseStorageDir := firstNonEmpty(flagStorageDir, os.Getenv("DEVSHARD_STORAGE_DIR"))
	if baseStorageDir == "" {
		if storagePath != "" {
			baseStorageDir = filepath.Dir(normalizeStorageDir(storagePath))
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
		sharedParticipantRequestLimiter.LoadStateWithQuarantine(t.Key, t.ModelIDs, t.Tokens, t.LastRefillAt, t.Status, t.QuarantineUntil, t.FailureStrikes)
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

func mustRepairPersistedGatewayEndpointSettings(gatewayStore *GatewayStore, gatewayState *GatewayState, flags cliFlags) {
	if gatewayStore == nil || gatewayState == nil {
		return
	}
	settings := gatewayState.Settings
	changed := false
	if strings.TrimSpace(settings.ChainREST) == "" {
		settings.ChainREST = envOverride(flags.chainREST, os.Getenv("DEVSHARD_CHAIN_REST"), defaultChainRESTURL)
		changed = true
	}
	if strings.TrimSpace(settings.PublicAPI) == "" {
		settings.PublicAPI = envOverride(flags.publicAPI, os.Getenv("DEVSHARD_PUBLIC_API"), defaultPublicAPIURL)
		changed = true
	}
	if !changed {
		return
	}
	if err := gatewayStore.UpdateSettings(settings); err != nil {
		log.Fatalf("repair persisted gateway endpoints: %v", err)
	}
	gatewayState.Settings = settings
	log.Printf("repaired persisted gateway endpoint settings chain_rest=%q public_api=%q", settings.ChainREST, settings.PublicAPI)
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
	devshards := make([]GatewayDevshardState, 0, len(runtimeCfgs))
	for _, cfg := range runtimeCfgs {
		devshards = append(devshards, GatewayDevshardState{
			RuntimeConfig: cfg,
			Active:        true,
		})
	}
	if err := gatewayStore.Initialize(opts.bootstrapSettings, devshards); err != nil {
		log.Fatalf("initialize gateway state: %v", err)
	}
}

func mustBuildGateway(gatewayStore *GatewayStore, gatewayState GatewayState, baseStorageDir string) *Gateway {
	gatewayState.Settings = gatewayState.Settings.WithTuningDefaults()
	DefaultRequestMaxTokens = gatewayState.Settings.DefaultRequestMaxTokens
	RequestMaxTokensCap = gatewayState.Settings.RequestMaxTokensCap
	applyGatewayTuningSettings(gatewayState.Settings)

	perfStore, err := NewPerfStore(filepath.Join(baseStorageDir, "perf.db"))
	if err != nil {
		log.Fatalf("open global perf store: %v", err)
	}
	perf := NewPerfTracker(perfStore)

	runtimes, err := buildGatewayRuntimes(gatewayStore, &gatewayState, baseStorageDir, perf)
	if err != nil {
		perfStore.Close()
		log.Fatalf("create runtimes: %v", err)
	}
	limiter := NewGatewayLimiter(
		gatewayState.Settings.MaxConcurrentRequests,
		gatewayState.Settings.MaxInputTokensInFlight,
	)
	limiter.UpdateLimits(
		gatewayState.Settings.MaxConcurrentRequests,
		gatewayState.Settings.MaxInputTokensInFlight,
		gatewayState.Settings.ModelLimits,
	)
	gateway := NewManagedGateway(runtimes, limiter, gatewayState.Settings, baseStorageDir, gatewayStore, perf)
	gateway.perfStore = perfStore
	return gateway
}

func buildGatewayRuntimes(gatewayStore *GatewayStore, gatewayState *GatewayState, baseStorageDir string, perf *PerfTracker) ([]*devshardRuntime, error) {
	// Load only ACTIVE devshards at boot. Inactive devshards (deactivated,
	// finalized, or settled) stay in the registry but are not built into
	// memory-resident runtimes: keeping hundreds of dormant escrows resident
	// wastes RAM, and probing each one against the chain at startup causes a
	// boot-time request storm. Inactive devshards are rehydrated on demand:
	// read-only from local storage for debug/state endpoints, or fully (with
	// chain access) for manual settlement. See hydrateReadOnlyRuntime and the
	// lazy settle path.
	type cfgEntry struct {
		cfg    RuntimeConfig
		active bool
	}
	allEntries := make([]cfgEntry, 0, len(gatewayState.Devshards))
	skippedInactive := 0
	for _, devshard := range gatewayState.Devshards {
		if !devshard.Active {
			skippedInactive++
			continue
		}
		allEntries = append(allEntries, cfgEntry{cfg: devshard.RuntimeConfig, active: devshard.Active})
	}
	allCfgs := make([]RuntimeConfig, len(allEntries))
	for i, e := range allEntries {
		allCfgs[i] = e.cfg
	}
	allCfgs, err := finalizeRuntimeConfigs(allCfgs, gatewayState.Settings.DefaultModel, baseStorageDir)
	if err != nil {
		return nil, fmt.Errorf("finalize gateway runtime configs: %w", err)
	}

	type buildResult struct {
		idx int
		rt  *devshardRuntime
		err error
	}
	t0 := time.Now()
	ch := make(chan buildResult, len(allCfgs))
	// Cap concurrent builders (see maxConcurrentRuntimeBuilds); results are still
	// collected per-index below, so ordering and error handling are unchanged.
	buildSem := make(chan struct{}, resolveMaxConcurrentRuntimeBuilds())
	for i, cfg := range allCfgs {
		go func(idx int, cfg RuntimeConfig) {
			buildSem <- struct{}{}
			defer func() { <-buildSem }()
			rt, err := gatewayRuntimeBuilder(cfg, gatewayState.Settings.ChainREST, gatewayState.Settings.DefaultModel, perf)
			ch <- buildResult{idx, rt, err}
		}(i, cfg)
	}

	runtimes := make([]*devshardRuntime, len(allCfgs))
	var skipped []int
	var firstFatal error
	for range allCfgs {
		res := <-ch
		cfg := allCfgs[res.idx]
		if res.err != nil {
			if !allEntries[res.idx].active {
				log.Printf("inactive devshard %s could not be loaded, skipping runtime: %v", cfg.ID, res.err)
				skipped = append(skipped, res.idx)
				continue
			}
			if errors.Is(res.err, bridge.ErrEscrowNotFound) || errors.Is(res.err, errRuntimePrivateKeyMissing) {
				reason := "runtime could not be loaded"
				if errors.Is(res.err, bridge.ErrEscrowNotFound) {
					reason = "escrow missing on chain"
				} else if errors.Is(res.err, errRuntimePrivateKeyMissing) {
					reason = "private key missing"
				}
				log.Printf("devshard %s %s, marking inactive and skipping runtime: %v", cfg.ID, reason, res.err)
				if gatewayStore != nil {
					if deactivateErr := gatewayStore.SetDevshardActive(cfg.ID, false); deactivateErr != nil {
						if firstFatal == nil {
							firstFatal = fmt.Errorf("deactivate devshard %s: %w", cfg.ID, deactivateErr)
						}
					}
				}
				markDevshardInactive(gatewayState, cfg.ID)
				skipped = append(skipped, res.idx)
				continue
			}
			if firstFatal == nil {
				firstFatal = res.err
			}
			continue
		}
		runtimes[res.idx] = res.rt
	}
	if firstFatal != nil {
		for _, rt := range runtimes {
			if rt != nil {
				rt.close()
			}
		}
		return nil, firstFatal
	}

	// Mark inactive runtimes so they're excluded from inference routing
	// but still accessible for finalization and debug endpoints.
	var activeCount, inactiveCount int
	for i, rt := range runtimes {
		if rt == nil {
			continue
		}
		if !allEntries[i].active {
			rt.active.Store(false)
			rt.activeConfigured = true
			inactiveCount++
		} else {
			rt.active.Store(true)
			rt.activeConfigured = true
			activeCount++
		}
	}

	// Compact out nil entries from skipped escrows (chain-missing).
	out := runtimes[:0]
	for _, rt := range runtimes {
		if rt != nil {
			out = append(out, rt)
		}
	}
	log.Printf("build_runtimes_parallel count=%d active=%d inactive=%d skipped=%d skipped_inactive=%d total_elapsed_ms=%d",
		len(out), activeCount, inactiveCount, len(skipped), skippedInactive, time.Since(t0).Milliseconds())
	return out, nil
}

func markDevshardInactive(gatewayState *GatewayState, id string) {
	if gatewayState == nil {
		return
	}
	for i := range gatewayState.Devshards {
		if gatewayState.Devshards[i].ID == id {
			gatewayState.Devshards[i].Active = false
			return
		}
	}
}

func closeRuntimes(runtimes []*devshardRuntime) {
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		if err := rt.close(); err != nil {
			log.Printf("close devshard %s: %v", rt.id, err)
		}
	}
}

func buildGatewayHandler(gateway *Gateway, opts runtimeOptions) http.Handler {
	var handler http.Handler = gateway.Handler()
	if gateway != nil {
		gateway.mu.Lock()
		gateway.apiKeys = opts.apiKeys
		gateway.mu.Unlock()
	}
	log.Printf("gateway API keys loaded (%d key(s)); per-model access modes are configured in gateway settings", len(opts.apiKeys))
	handler = adminAuthMiddleware(opts.adminAPIKey, handler)
	handler = gateway.disabledMiddleware(handler)
	return gateway.metrics.Wrap(handler)
}

func serveGateway(handler http.Handler, port string, runtimeCount int) {
	addr := ":" + port
	log.Printf("devshardctl gateway listening on %s (devshards=%d default_max_tokens=%d max_tokens_cap=%d)", addr, runtimeCount, DefaultRequestMaxTokens, RequestMaxTokensCap)
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
		isAdminPath(path)
}

func isAdminPath(path string) bool {
	if strings.HasPrefix(path, "/v1/admin/") ||
		strings.HasPrefix(path, "/v1/debug/") ||
		strings.HasPrefix(path, "/debug/pprof/") ||
		path == "/v1/finalize" ||
		path == "/v1/state" {
		return true
	}
	_, innerPath, ok := parseDevshardPath(path)
	return ok && (innerPath == "/v1/finalize" ||
		innerPath == "/v1/state" ||
		strings.HasPrefix(innerPath, "/v1/debug/"))
}

func adminAuthMiddleware(adminKey string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		adminAuthenticated := adminKey != "" &&
			strings.HasPrefix(auth, "Bearer ") &&
			strings.TrimPrefix(auth, "Bearer ") == adminKey
		if adminAuthenticated {
			r = r.WithContext(context.WithValue(r.Context(), adminAuthContextKey{}, true))
			r = r.WithContext(context.WithValue(r.Context(), adminAPIKeySuffixContextKey{}, apiKeySuffix(adminKey)))
		}
		if !isAdminPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		if adminKey == "" {
			http.NotFound(w, r)
			return
		}
		if !adminAuthenticated {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"Invalid admin API key.","type":"invalid_request_error","code":"invalid_api_key"}}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func requestHasAdminAuth(r *http.Request) bool {
	if r == nil {
		return false
	}
	ok, _ := r.Context().Value(adminAuthContextKey{}).(bool)
	return ok
}

func requestAdminAPIKeySuffix(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	suffix, ok := r.Context().Value(adminAPIKeySuffixContextKey{}).(string)
	return suffix, ok && suffix != ""
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

func readFloat64Env(name string, fallback float64) float64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	var v float64
	if _, err := fmt.Sscan(raw, &v); err != nil {
		log.Printf("invalid %s=%q, using %g", name, raw, fallback)
		return fallback
	}
	return v
}

func readBoolEnv(name string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		log.Printf("invalid %s=%q, using %t", name, raw, fallback)
		return fallback
	}
}

func buildSettlementJSON(p *state.SettlementPayload) (SettlementJSON, error) {
	hsHash, err := state.ComputeHostStatsHash(p.HostStats)
	if err != nil {
		return SettlementJSON{}, err
	}
	root := state.ComputeStateRootFromRestHash(hsHash, p.RestHash, p.Fees, types.PhaseSettlement, p.StateRootAndProtocolVersion)

	stats := make([]HostStatsJSON, 0, len(p.HostStats))
	for slot, hs := range p.HostStats {
		stats = append(stats, hostStatsJSONFromDomain(slot, hs))
	}

	sigs := make([]SlotSignatureJSON, 0, len(p.Signatures))
	for slot, sig := range p.Signatures {
		sigs = append(sigs, SlotSignatureJSON{SlotID: slot, Signature: base64.StdEncoding.EncodeToString(sig)})
	}

	return SettlementJSON{
		EscrowID: p.EscrowID, StateRootAndProtocolVersion: p.StateRootAndProtocolVersion, StateRoot: base64.StdEncoding.EncodeToString(root),
		Nonce: p.Nonce, Fees: p.Fees, RestHash: base64.StdEncoding.EncodeToString(p.RestHash),
		HostStats: stats, Signatures: sigs,
	}, nil
}

func marshalSettlement(p *state.SettlementPayload) ([]byte, error) {
	settlement, err := buildSettlementJSON(p)
	if err != nil {
		return nil, err
	}
	return json.MarshalIndent(settlement, "", "  ")
}
