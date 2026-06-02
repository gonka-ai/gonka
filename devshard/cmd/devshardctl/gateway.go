package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

type RuntimeConfig struct {
	ID              string `json:"id"`
	PrivateKeyHex   string `json:"private_key,omitempty"`
	PrivateKeyEnv   string `json:"private_key_env,omitempty"`
	Model           string `json:"model,omitempty"`
	StoragePath     string `json:"storage_path,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type Gateway struct {
	runtimes              map[string]*devshardRuntime
	runtimeOrder          []*devshardRuntime
	limiter               *GatewayLimiter
	participantLimiter    *ParticipantRequestLimiter
	phaseGate             *ChainPhaseGate
	escrowChecker         *EscrowChecker
	metrics               *DevshardMetrics
	capacity              *CapacityState
	settings              GatewaySettings
	store                 *GatewayStore
	perf                  *PerfTracker
	perfStore             *PerfStore
	chatCache             *chatResponseCache
	apiKeys               map[string]struct{}
	baseStorageDir        string
	rotatorStop           chan struct{}
	rotatorDone           chan struct{}
	rotationFailures      map[string]struct{}
	finalizeMu            sync.Mutex
	settlementMu          sync.Mutex
	settlementInFlight    map[string]struct{}
	replenishmentMu       sync.Mutex
	replenishmentInFlight map[string]struct{}
	mu                    sync.Mutex
	roundRobinSeed        atomic.Uint64
}

type devshardRuntime struct {
	id              string
	model           string
	handler         http.Handler
	proxy           *Proxy
	session         *user.Session
	participantKeys []string
	// participantSlotCounts maps a participant key to the number of
	// slots in this escrow's group held by that host. Used by the
	// CapacityState to compute share(h,e). Length differs from
	// participantKeys when one host occupies multiple slots in the
	// same escrow.
	participantSlotCounts map[string]int

	active         atomic.Bool
	activeRequests atomic.Int64
	reservedTokens atomic.Int64

	activeConfigured bool
}

type runtimeStatus struct {
	ID                   string `json:"id"`
	Model                string `json:"model"`
	Active               bool   `json:"active"`
	Phase                string `json:"phase,omitempty"`
	Nonce                uint64 `json:"nonce,omitempty"`
	Balance              uint64 `json:"balance,omitempty"`
	ProtocolVersion      string `json:"protocol_version,omitempty"`
	ActiveRequests       int64  `json:"active_requests"`
	ReservedTokens       int64  `json:"reserved_tokens"`
	ChainPhase           string `json:"chain_phase,omitempty"`
	ConfirmationPoCPhase string `json:"confirmation_poc_phase,omitempty"`
	RequestsBlocked      bool   `json:"requests_blocked"`
	BlockReason          string `json:"block_reason,omitempty"`
}

type gatewayCapacityStatus struct {
	TotalWeight              float64                               `json:"total_weight"`
	BaselineWeight           float64                               `json:"baseline_weight"`
	LostWeight               float64                               `json:"lost_weight"`
	ScaleFactor              float64                               `json:"scale_factor"`
	AvailablePercent         float64                               `json:"available_percent"`
	LostPercent              float64                               `json:"lost_percent"`
	HostCount                int                                   `json:"host_count"`
	AvailableHostCount       int                                   `json:"available_host_count"`
	UnavailableHostCount     int                                   `json:"unavailable_host_count"`
	CurrentWeightMatched     int                                   `json:"current_weight_matched_hosts"`
	CurrentWeightFallback    int                                   `json:"current_weight_fallback_hosts"`
	BaselineWeightMatched    int                                   `json:"baseline_weight_matched_hosts"`
	BaselineWeightFallback   int                                   `json:"baseline_weight_fallback_hosts"`
	ObservedCurrentWeightKey int                                   `json:"observed_current_weight_keys"`
	ObservedFullWeightKey    int                                   `json:"observed_full_weight_keys"`
	EscrowWeights            map[string]float64                    `json:"escrow_weights"`
	Models                   map[string]gatewayModelCapacityStatus `json:"models,omitempty"`
}

type gatewayModelCapacityStatus struct {
	TotalWeight       float64 `json:"total_weight"`    // Deprecated alias for current_weight.
	CurrentWeight     float64 `json:"current_weight"`  // Current raw poc_weight available for this model.
	FullWeight        float64 `json:"full_weight"`     // Full raw poc_weight baseline for this model.
	BaselineWeight    float64 `json:"baseline_weight"` // Deprecated alias for full_weight.
	LostWeight        float64 `json:"lost_weight"`
	ScaleFactor       float64 `json:"scale_factor"`
	LimitShare        float64 `json:"limit_share"` // Deprecated alias for scale_factor.
	AvailablePercent  float64 `json:"available_percent"`
	LostPercent       float64 `json:"lost_percent"`
	ActiveDevshards   int     `json:"active_devshards"`
	RoutableDevshards int     `json:"routable_devshards"`
	Routable          bool    `json:"routable"`
	AccessEnabled     bool    `json:"access_enabled"`
	AccessMode        string  `json:"access_mode"`
	AccessMessage     string  `json:"access_message,omitempty"`
}

var (
	DefaultRequestMaxTokens uint64 = 3_072
	RequestMaxTokensCap     uint64 = 4_096

	errRuntimePrivateKeyMissing = errors.New("private key missing")
)

type UnsupportedModelError struct {
	Model     string
	Supported []string
}

func (e *UnsupportedModelError) Error() string {
	if len(e.Supported) == 0 {
		return fmt.Sprintf("unsupported model %q", e.Model)
	}
	return fmt.Sprintf("unsupported model %q; supported models: %s", e.Model, strings.Join(e.Supported, ", "))
}

type ModelTemporarilyUnavailableError struct {
	Model   string
	Message string
}

func (e *ModelTemporarilyUnavailableError) Error() string {
	if e == nil {
		return ""
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return msg
	}
	if model := strings.TrimSpace(e.Model); model != "" {
		return fmt.Sprintf("model %q is temporarily unavailable", model)
	}
	return "model is temporarily unavailable"
}

type ModelAccessDeniedError struct {
	Model      string
	Message    string
	StatusCode int
}

func (e *ModelAccessDeniedError) Error() string {
	if e == nil {
		return ""
	}
	if msg := strings.TrimSpace(e.Message); msg != "" {
		return msg
	}
	return "model access denied"
}

func newRuntimeMux(proxy *Proxy) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", proxy.handleSwaggerUI)
	mux.HandleFunc("GET /openapi.json", proxy.handleOpenAPISpec)
	mux.HandleFunc("/v1/models", proxy.handleModels)
	mux.HandleFunc("/v1/chat/completions", proxy.handleChatCompletions)
	mux.HandleFunc("POST /v1/finalize", proxy.handleFinalize)
	mux.HandleFunc("GET /v1/finalize", proxy.handleGetFinalize)
	mux.HandleFunc("GET /v1/status", proxy.handleStatus)
	mux.HandleFunc("GET /v1/requests/{request_id}", proxy.handleRequestAccounting)
	mux.HandleFunc("GET /v1/state", proxy.handleState)
	mux.HandleFunc("GET /v1/debug/pending", proxy.handleDebugPending)
	mux.HandleFunc("GET /v1/debug/state", proxy.handleDebugState)
	mux.HandleFunc("GET /v1/debug/perf", proxy.handleDebugPerf)
	mux.HandleFunc("GET /v1/debug/pairwise", proxy.handleDebugPairwise)
	mux.HandleFunc("GET /v1/debug/signatures", proxy.handleDebugSignatures)
	mux.HandleFunc("POST /v1/debug/signatures/collect", proxy.handleCollectSignatures)
	return mux
}

func buildRuntime(cfg RuntimeConfig, chainREST, defaultModel string, perf *PerfTracker) (*devshardRuntime, error) {
	legacyStoragePath := strings.TrimSpace(cfg.StoragePath)
	keyHex := strings.TrimSpace(cfg.PrivateKeyHex)
	if keyHex == "" && cfg.PrivateKeyEnv != "" {
		keyHex = strings.TrimSpace(os.Getenv(cfg.PrivateKeyEnv))
	}
	if keyHex == "" {
		return nil, fmt.Errorf("runtime %s: %w", cfg.ID, errRuntimePrivateKeyMissing)
	}

	model := cfg.Model
	if model == "" {
		model = defaultModel
	}

	cfg.StoragePath = normalizeStorageDir(cfg.StoragePath)
	if err := os.MkdirAll(cfg.StoragePath, 0o755); err != nil {
		return nil, fmt.Errorf("runtime %s: create storage dir: %w", cfg.ID, err)
	}

	if perf == nil {
		perf = NewPerfTracker(nil)
	}

	pv, pvErr := types.ParseProtocolVersion(cfg.ProtocolVersion)
	if pvErr != nil {
		return nil, fmt.Errorf("runtime %s: %w", cfg.ID, pvErr)
	}

	br := newRESTBridgeForProtocol(chainREST, pv)
	if err := migrateGatewayLegacyStorage(cfg.StoragePath, legacyStoragePath, cfg.ID, br); err != nil {
		return nil, fmt.Errorf("runtime %s: migrate legacy storage: %w", cfg.ID, err)
	}
	routePrefix := devshardpkg.ResolveHostRoutePrefix(pv, os.Getenv("DEVSHARD_ROUTE_PREFIX"))
	session, sm, err := user.NewHTTPSession(user.HTTPSessionConfig{
		PrivateKeyHex:    keyHex,
		EscrowID:         cfg.ID,
		Bridge:           br,
		StoragePath:      cfg.StoragePath,
		RoutePrefix:      routePrefix,
		RequestAdmission: sharedParticipantRequestLimiter,
		ProtocolVersion:  pv,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime %s: create session: %w", cfg.ID, err)
	}
	if err := perf.BackfillLegacyEscrowSamples(cfg.ID, legacyPerfSourcePath(legacyStoragePath), session.HostParticipantKeyList()); err != nil {
		log.Printf("runtime %s: backfill legacy perf samples: %v", cfg.ID, err)
	}

	redundancy := NewRedundancyWithThrottle(
		session,
		perf,
		len(session.Clients()),
		model,
		sharedParticipantRequestLimiter.IsBlocked,
	)
	redundancy.participantLimiter = sharedParticipantRequestLimiter
	proxy := &Proxy{
		session:    session,
		sm:         sm,
		escrowID:   cfg.ID,
		model:      model,
		redundancy: redundancy,
		perf:       perf,
	}

	rt := &devshardRuntime{
		id:                    cfg.ID,
		model:                 model,
		handler:               newRuntimeMux(proxy),
		proxy:                 proxy,
		session:               session,
		participantKeys:       session.ParticipantKeys(),
		participantSlotCounts: hostSlotCounts(session.HostParticipantKeyList()),
	}
	rt.active.Store(true)
	rt.activeConfigured = true
	return rt, nil
}

func newRESTBridgeForProtocol(chainREST string, pv types.ProtocolVersion) *bridge.RESTBridge {
	return bridge.NewRESTBridge(chainREST)
}

// hostSlotCounts builds a slot-count map from a per-slot participant
// key list. Empty keys (uncommon, but possible if a slot lacks a
// validator address) are skipped.
func hostSlotCounts(perSlotKeys []string) map[string]int {
	counts := make(map[string]int, len(perSlotKeys))
	for _, key := range perSlotKeys {
		if key == "" {
			continue
		}
		counts[key]++
	}
	return counts
}

func (rt *devshardRuntime) close() error {
	if rt.session != nil {
		rt.session.Close()
	}
	return nil
}

func (rt *devshardRuntime) acceptsNewInferences() (bool, string) {
	if rt == nil || !rt.active.Load() {
		return false, "inactive"
	}
	if rt.proxy == nil || rt.proxy.sm == nil {
		return true, ""
	}
	phase := rt.proxy.sm.Phase()
	if phase == types.PhaseActive {
		return true, ""
	}
	return false, fmt.Sprintf("phase=%s", sessionPhaseLabel(phase))
}

func runtimeSkipReasonKey(reason string) string {
	reason = strings.TrimSpace(reason)
	if phase, ok := strings.CutPrefix(reason, "phase="); ok {
		return phase
	}
	if reason == "" {
		return "unknown"
	}
	return reason
}

func formatRuntimeSkipReasonCounts(counts map[string]int) string {
	if len(counts) == 0 {
		return ""
	}
	reasons := make([]string, 0, len(counts))
	for reason := range counts {
		reasons = append(reasons, reason)
	}
	slices.Sort(reasons)

	parts := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		parts = append(parts, fmt.Sprintf("%s=%d", reason, counts[reason]))
	}
	return strings.Join(parts, ", ")
}

func sessionPhaseLabel(phase types.SessionPhase) string {
	switch phase {
	case types.PhaseActive:
		return "active"
	case types.PhaseFinalizing:
		return "finalizing"
	case types.PhaseSettlement:
		return "settlement"
	default:
		return fmt.Sprintf("unknown(%d)", phase)
	}
}

func (rt *devshardRuntime) snapshot() runtimeStatus {
	status := runtimeStatus{
		ID:             rt.id,
		Model:          rt.model,
		Active:         rt.active.Load(),
		ActiveRequests: rt.activeRequests.Load(),
		ReservedTokens: rt.reservedTokens.Load(),
	}
	if rt.proxy != nil && rt.proxy.sm != nil && rt.proxy.session != nil {
		phase := rt.proxy.sm.Phase()
		status.Phase = sessionPhaseLabel(phase)
		st := rt.proxy.sm.SnapshotState()
		status.Nonce = rt.proxy.session.Nonce()
		status.Balance = st.Balance
		status.ProtocolVersion = string(rt.proxy.sm.ProtocolVersion())
	}
	if rt.proxy != nil && rt.proxy.phaseGate != nil {
		snapshot := rt.proxy.phaseGate.Snapshot()
		status.ChainPhase = snapshot.EpochPhase
		status.ConfirmationPoCPhase = snapshot.ConfirmationPoCPhase
		status.RequestsBlocked = snapshot.RequestsBlocked
		status.BlockReason = snapshot.BlockReason
	}
	return status
}

// TODO: the (reservedTokens*1000 + activeRequests) formula is missleading,
// let's just leave activeRequests here, and leave a todo comment, that
// we might need to change it, so that if limits for tokens or cuncurrent
// requests are set, we need to measure if the escrow is further from
// the limists

// load returns the capacity-aware load score for this runtime. Lower
// is better; the picker selects the runtime with the smallest load.
//
// Score is simply activeRequests / W(e):
//   - activeRequests is the live count of in-flight inferences this
//     runtime owns (incremented on dispatch, decremented on
//     completion). It's the most direct, low-latency signal of "is
//     this runtime busy right now".
//   - W(e) is the runtime's effective capacity: the sum of available
//     host weights, accounting for chain-side weight, share within the
//     escrow, PoC preservation, and reactive throttle.
//
// Reserved tokens (the historical "I expect this many tokens to flow
// through me soon" hint) used to dominate the score; we no longer mix
// them in because (a) they're a noisy estimate, (b) the participant
// limiter already kills hosts that get hot, and (c) keeping the score
// to one quantity makes load-balance debugging tractable.
//
// A weight <= 0 means the escrow currently has no usable hosts (every
// host is throttled or PoC-excluded). Returning +Inf pushes it to the
// back of the queue without removing it from the candidate set, which
// preserves the existing fall-back semantics if every escrow degrades
// simultaneously.
func (rt *devshardRuntime) load(weight float64) float64 {
	if weight <= 0 {
		return math.Inf(+1)
	}
	return float64(rt.activeRequests.Load()) / weight
}

func NewGateway(runtimes []*devshardRuntime, limiter *GatewayLimiter, defaultModel string) *Gateway {
	byID := make(map[string]*devshardRuntime, len(runtimes))
	for _, rt := range runtimes {
		if !rt.activeConfigured {
			rt.active.Store(true)
			rt.activeConfigured = true
		}
		byID[rt.id] = rt
	}
	g := &Gateway{
		runtimes:           byID,
		runtimeOrder:       runtimes,
		limiter:            limiter,
		participantLimiter: sharedParticipantRequestLimiter,
		metrics:            NewDevshardMetrics(),
		capacity:           NewCapacityState(),
		chatCache:          newChatResponseCache(chatResponseCacheTTL),
		settings: GatewaySettings{
			DefaultModel: defaultModel,
		},
		rotationFailures:   make(map[string]struct{}),
		settlementInFlight: make(map[string]struct{}),
	}
	g.participantLimiter.SetMetrics(g.metrics)
	g.metrics.AttachGateway(g)
	g.attachCapacityLiveAvailability()
	for _, rt := range runtimes {
		g.attachRuntimeSharedState(rt)
	}
	return g
}

func NewManagedGateway(runtimes []*devshardRuntime, limiter *GatewayLimiter, settings GatewaySettings, baseStorageDir string, store *GatewayStore, perfArgs ...*PerfTracker) *Gateway {
	settings = settings.WithTuningDefaults()
	applyGatewayTuningSettings(settings)
	g := NewGateway(runtimes, limiter, settings.DefaultModel)
	g.settings = settings
	g.baseStorageDir = baseStorageDir
	g.store = store
	if len(perfArgs) > 0 && perfArgs[0] != nil {
		g.perf = perfArgs[0]
	}
	g.phaseGate = NewChainPhaseGate(settings.PublicAPI, 0)
	if g.phaseGate != nil {
		g.phaseGate.SetPreservedSnapshotBaseURL(settings.ChainREST)
	}
	if g.phaseGate != nil {
		for _, rt := range g.runtimeOrder {
			g.attachRuntimeSharedState(rt)
		}
		g.attachCapacityStateToPhaseGate()
		g.phaseGate.Start()
	}
	g.escrowChecker = NewEscrowChecker(func() string {
		g.mu.Lock()
		defer g.mu.Unlock()
		return g.settings.ChainREST
	})
	for _, rt := range g.runtimeOrder {
		g.attachEscrowChecker(rt)
	}
	g.startEscrowRotatorIfEnabled()
	go g.balanceCheckLoop()
	return g
}

func (g *Gateway) attachRuntimeSharedState(rt *devshardRuntime) {
	if g == nil || rt == nil {
		return
	}
	if rt.proxy != nil {
		rt.proxy.phaseGate = g.phaseGate
		limits := g.outputTokenLimitsForModel(firstNonEmpty(rt.model, g.settings.DefaultModel))
		rt.proxy.defaultRequestMaxTokens = limits.DefaultMaxTokens
		rt.proxy.requestMaxTokensCap = limits.MaxTokensCap
	}
	g.attachMetrics(rt)
	g.attachEscrowChecker(rt)
	if g.capacity != nil {
		g.capacity.SetEscrowMembership(rt.id, rt.participantSlotCounts)
	}
}

func (g *Gateway) outputTokenLimitsForModel(model string) outputTokenLimits {
	if g == nil {
		return defaultOutputTokenLimits()
	}
	settings := g.settings.WithTuningDefaults()
	limits := outputTokenLimits{
		DefaultMaxTokens: settings.DefaultRequestMaxTokens,
		MaxTokensCap:     settings.RequestMaxTokensCap,
	}
	model = strings.TrimSpace(model)
	for _, modelLimit := range settings.ModelLimits {
		if modelLimit.ModelID != model {
			continue
		}
		if modelLimit.DefaultRequestMaxTokens > 0 {
			limits.DefaultMaxTokens = modelLimit.DefaultRequestMaxTokens
		}
		if modelLimit.RequestMaxTokensCap > 0 {
			limits.MaxTokensCap = modelLimit.RequestMaxTokensCap
		}
		break
	}
	return normalizedOutputTokenLimits(limits)
}

const (
	balanceCheckInterval                = 30 * time.Second
	balanceMinimumThreshold      uint64 = 1_000_000
	nonceDeactivationLimit       uint64 = 19_800
	autoSettlementRetryInterval         = 10 * time.Second
	autoSettlementAttemptTimeout        = 5 * time.Minute
	autoSettlementMaxAttempts           = 30
)

// checkBalances scans all active runtimes and deactivates any whose
// escrow is close to exhausting its usable balance or nonce budget.
func (g *Gateway) checkBalances() {
	g.mu.Lock()
	if !g.settings.EscrowRotation.Enabled {
		g.mu.Unlock()
		return
	}
	runtimes := make([]*devshardRuntime, len(g.runtimeOrder))
	copy(runtimes, g.runtimeOrder)
	g.mu.Unlock()

	for _, rt := range runtimes {
		if rt == nil || !rt.active.Load() || rt.proxy == nil || rt.proxy.sm == nil {
			continue
		}
		balance := rt.proxy.sm.Balance()
		if balance < balanceMinimumThreshold {
			log.Printf("escrow_balance_low escrow=%s balance=%d threshold=%d — scheduling replacement before deactivation",
				rt.id, balance, balanceMinimumThreshold)
			g.scheduleDepletedEscrowReplacement(rt.id, rt.model, "low_balance")
			continue
		}
		nonce := rt.proxy.sm.LatestNonce()
		if nonce >= nonceDeactivationLimit {
			log.Printf("escrow_nonce_high escrow=%s nonce=%d limit=%d — scheduling replacement before deactivation",
				rt.id, nonce, nonceDeactivationLimit)
			g.scheduleDepletedEscrowReplacement(rt.id, rt.model, "high_nonce")
		}
	}
}

// balanceCheckLoop periodically checks each active runtime's escrow limits.
func (g *Gateway) balanceCheckLoop() {
	g.checkBalances()
	ticker := time.NewTicker(balanceCheckInterval)
	defer ticker.Stop()
	for range ticker.C {
		g.checkBalances()
	}
}

// attachCapacityStateToPhaseGate wires the capacity state into the
// chain phase poll loop. Two channels are wired:
//
//   - Live availability source: the picker pulls per-host availability
//     from the participant limiter on every EscrowWeight call so a 503
//     (or recovery) shrinks/restores W(e) on the very next request,
//     without waiting for the next phase poll. Availability is binary
//     with hysteresis to full bucket recovery (see
//     ParticipantRequestLimiter.IsAvailable).
//   - Phase-gate snapshot push: chain-reported raw poc_weight capacity
//     and PoC preserved set on every refresh, plus a scale-hook callback
//     that pushes the latest W_tot/W_ref ratio to the GatewayLimiter.
func (g *Gateway) attachCapacityStateToPhaseGate() {
	if g == nil || g.phaseGate == nil || g.capacity == nil {
		return
	}
	g.attachCapacityLiveAvailability()
	scaleHook := func(scale float64) {
		if g.limiter == nil {
			return
		}
		g.limiter.ApplyScaleFactor(scale)
	}
	g.phaseGate.SetCapacityState(g.capacity, scaleHook)
}

func (g *Gateway) attachCapacityLiveAvailability() {
	if g == nil || g.capacity == nil {
		return
	}
	if g.participantLimiter == nil {
		g.capacity.SetLiveAvailable(nil)
		return
	}
	g.capacity.SetLiveAvailable(g.participantLimiter.IsAvailable)
}

func (g *Gateway) refreshCapacityScale() {
	if g == nil || g.capacity == nil || g.limiter == nil {
		return
	}
	if !g.limiter.HasConfiguredLimits() {
		return
	}
	g.limiter.ApplyScaleFactor(g.capacity.ScaleFactorAcrossModels())
}

func (g *Gateway) modelLimitSettings(model string) (GatewayModelLimitSettings, bool) {
	model = strings.TrimSpace(model)
	if g == nil || model == "" {
		return GatewayModelLimitSettings{}, false
	}
	g.mu.Lock()
	settings := append([]GatewayModelLimitSettings(nil), g.settings.ModelLimits...)
	g.mu.Unlock()
	for _, entry := range settings {
		if strings.TrimSpace(entry.ModelID) == model {
			entry.ModelID = model
			entry.AccessMode = normalizeGatewayAccessMode(entry.AccessMode)
			entry.AccessMessage = strings.TrimSpace(entry.AccessMessage)
			return entry, true
		}
	}
	return GatewayModelLimitSettings{}, false
}

func (g *Gateway) modelAccessError(r *http.Request, model string) error {
	if requestHasAdminAuth(r) {
		return nil
	}
	entry, ok := g.modelLimitSettings(model)
	if !ok {
		message := fmt.Sprintf("model %q requires an admin API key", model)
		return &ModelAccessDeniedError{Model: model, Message: message, StatusCode: http.StatusUnauthorized}
	}
	switch gatewayModelAccessModeLabel(entry.AccessMode) {
	case string(gatewayAccessModeOpen):
		return nil
	case string(gatewayAccessModeAPIKey):
		if g.requestHasAPIKey(r) {
			return nil
		}
		message := entry.AccessMessage
		if message == "" {
			message = fmt.Sprintf("model %q requires an API key", model)
		}
		return &ModelAccessDeniedError{Model: model, Message: message, StatusCode: http.StatusUnauthorized}
	case string(gatewayAccessModeAdminOnly):
		message := entry.AccessMessage
		if message == "" {
			message = fmt.Sprintf("model %q requires an admin API key", model)
		}
		return &ModelAccessDeniedError{Model: model, Message: message, StatusCode: http.StatusUnauthorized}
	default:
		return nil
	}
}

func (g *Gateway) modelAccessStatus(model string) (bool, string, string) {
	entry, ok := g.modelLimitSettings(model)
	if !ok {
		return true, string(gatewayAccessModeAdminOnly), ""
	}
	return true, gatewayModelAccessModeLabel(entry.AccessMode), strings.TrimSpace(entry.AccessMessage)
}

func (g *Gateway) requestHasAPIKey(r *http.Request) bool {
	if g == nil || r == nil {
		return false
	}
	key, ok := bearerToken(r)
	if !ok {
		return false
	}
	g.mu.Lock()
	_, ok = g.apiKeys[key]
	g.mu.Unlock()
	return ok
}

func bearerToken(r *http.Request) (string, bool) {
	if r == nil {
		return "", false
	}
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", false
	}
	key := strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	return key, key != ""
}

func apiKeySuffix(key string) string {
	key = strings.TrimSpace(key)
	if len(key) <= 8 {
		return key
	}
	return key[len(key)-8:]
}

func (g *Gateway) apiKeyLogFields(r *http.Request) []any {
	if suffix, ok := requestAdminAPIKeySuffix(r); ok {
		return []any{"api_key_suffix", suffix, "api_key_kind", "admin"}
	}
	key, ok := bearerToken(r)
	if !ok {
		return nil
	}
	kind := "unknown"
	g.mu.Lock()
	if _, valid := g.apiKeys[key]; valid {
		kind = "api"
	}
	g.mu.Unlock()
	return []any{"api_key_suffix", apiKeySuffix(key), "api_key_kind", kind}
}

func (g *Gateway) statusModels(runtimes []*devshardRuntime) []string {
	seen := map[string]struct{}{}
	if g != nil && g.capacity != nil {
		for _, model := range g.capacity.Models() {
			seen[model] = struct{}{}
		}
	}
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		if model := strings.TrimSpace(rt.model); model != "" {
			seen[model] = struct{}{}
		}
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	slices.Sort(models)
	return models
}

type gatewayModelRuntimeStatus struct {
	active   int
	routable int
}

func (g *Gateway) gatewayModelRuntimeStatuses(runtimes []*devshardRuntime) map[string]gatewayModelRuntimeStatus {
	statuses := make(map[string]gatewayModelRuntimeStatus)
	for _, rt := range runtimes {
		if rt == nil {
			continue
		}
		model := strings.TrimSpace(rt.model)
		if model == "" {
			continue
		}
		status := statuses[model]
		if rt.active.Load() {
			status.active++
		}
		if ok, _ := rt.acceptsNewInferences(); ok {
			status.routable++
		}
		statuses[model] = status
	}
	return statuses
}

func (g *Gateway) limiterModelScales(models []string, runtimeStatuses map[string]gatewayModelRuntimeStatus) map[string]float64 {
	if g == nil || g.capacity == nil || len(models) == 0 {
		return nil
	}
	scales := make(map[string]float64, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if runtimeStatuses[model].routable == 0 {
			scales[model] = 0
			continue
		}
		scales[model] = g.capacity.ScaleFactorForModel(model)
	}
	return scales
}

func (g *Gateway) limiterModelCapacities(models []string, runtimeStatuses map[string]gatewayModelRuntimeStatus) map[string]LimiterModelCapacity {
	if g == nil || g.capacity == nil || len(models) == 0 {
		return nil
	}
	capacities := make(map[string]LimiterModelCapacity, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		capacity := g.limiterCapacityForModel(model)
		if runtimeStatuses[model].routable == 0 {
			capacity.ScaleFactor = 0
			capacity.CurrentWeight = 0
		}
		capacities[model] = capacity
	}
	return capacities
}

func (g *Gateway) limiterCapacityForModel(model string) LimiterModelCapacity {
	if g == nil || g.capacity == nil {
		return LimiterModelCapacity{ScaleFactor: 1}
	}
	snapshot := g.capacity.Snapshot()
	perWeight := g.currentMaxConcurrentPer10000Weight()
	if snapshot.ObservedCurrentWeightKey == 0 && snapshot.ObservedFullWeightKey == 0 {
		perWeight = 0
	}
	return LimiterModelCapacity{
		ScaleFactor:                 g.capacity.ScaleFactorForModel(model),
		CurrentWeight:               g.capacity.TotalWeightForModel(model),
		BaselineWeight:              g.capacity.BaselineWeightForModel(model),
		MaxConcurrentPer10000Weight: perWeight,
	}
}

func (g *Gateway) currentMaxConcurrentPer10000Weight() float64 {
	if g == nil {
		return 0
	}
	g.mu.Lock()
	settings := g.settings.WithTuningDefaults()
	g.mu.Unlock()
	if g.pocOrConfirmationPoCActive() {
		return settings.PoCMaxConcurrentPer10000Weight
	}
	return settings.MaxConcurrentPer10000Weight
}

func (g *Gateway) pocOrConfirmationPoCActive() bool {
	if g != nil && g.phaseGate != nil {
		switch g.phaseGate.Snapshot().BlockReason {
		case "poc", "confirmation_poc":
			return true
		}
	}
	switch currentPoCPhaseReason() {
	case "poc", "confirmation_poc":
		return true
	default:
		return false
	}
}

func (g *Gateway) capacityStatus(models []string, runtimeStatuses map[string]gatewayModelRuntimeStatus) gatewayCapacityStatus {
	if g == nil || g.capacity == nil {
		return gatewayCapacityStatus{}
	}
	snap := g.capacity.Snapshot()
	lost := snap.BaselineWeight - snap.TotalWeight
	if lost < 0 {
		lost = 0
	}
	availablePercent := snap.ScaleFactor * 100
	lostPercent := 100 - availablePercent
	if lostPercent < 0 {
		lostPercent = 0
	}
	status := gatewayCapacityStatus{
		TotalWeight:              snap.TotalWeight,
		BaselineWeight:           snap.BaselineWeight,
		LostWeight:               lost,
		ScaleFactor:              snap.ScaleFactor,
		AvailablePercent:         availablePercent,
		LostPercent:              lostPercent,
		HostCount:                snap.HostCount,
		AvailableHostCount:       snap.AvailableHostCount,
		UnavailableHostCount:     snap.UnavailableHostCount,
		CurrentWeightMatched:     snap.CurrentWeightMatched,
		CurrentWeightFallback:    snap.CurrentWeightFallback,
		BaselineWeightMatched:    snap.BaselineWeightMatched,
		BaselineWeightFallback:   snap.BaselineWeightFallback,
		ObservedCurrentWeightKey: snap.ObservedCurrentWeightKey,
		ObservedFullWeightKey:    snap.ObservedFullWeightKey,
		EscrowWeights:            snap.EscrowWeights,
	}
	if len(models) > 0 {
		status.Models = make(map[string]gatewayModelCapacityStatus, len(models))
		for _, model := range models {
			model = strings.TrimSpace(model)
			if model == "" {
				continue
			}
			total := g.capacity.TotalWeightForModel(model)
			baseline := g.capacity.BaselineWeightForModel(model)
			runtimeStatus := runtimeStatuses[model]
			accessEnabled, accessMode, accessMessage := g.modelAccessStatus(model)
			if runtimeStatus.routable == 0 {
				total = 0
			}
			modelLost := baseline - total
			if modelLost < 0 {
				modelLost = 0
			}
			scale := g.capacity.ScaleFactorForModel(model)
			if runtimeStatus.routable == 0 {
				scale = 0
			}
			modelAvailablePercent := scale * 100
			modelLostPercent := 100 - modelAvailablePercent
			if modelLostPercent < 0 {
				modelLostPercent = 0
			}
			status.Models[model] = gatewayModelCapacityStatus{
				TotalWeight:       total,
				CurrentWeight:     total,
				FullWeight:        baseline,
				BaselineWeight:    baseline,
				LostWeight:        modelLost,
				ScaleFactor:       scale,
				LimitShare:        scale,
				AvailablePercent:  modelAvailablePercent,
				LostPercent:       modelLostPercent,
				ActiveDevshards:   runtimeStatus.active,
				RoutableDevshards: runtimeStatus.routable,
				Routable:          runtimeStatus.routable > 0,
				AccessEnabled:     accessEnabled,
				AccessMode:        accessMode,
				AccessMessage:     accessMessage,
			}
		}
	}
	return status
}

func (g *Gateway) Close() error {
	var firstErr error
	if g.phaseGate != nil {
		g.phaseGate.Stop()
	}
	g.stopEscrowRotator()
	for _, rt := range g.runtimeOrder {
		if err := rt.close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if g.perfStore != nil {
		if err := g.perfStore.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", g.metrics.Handler())
	mux.HandleFunc("/v1/models", g.handlePooledModels)
	mux.HandleFunc("/v1/chat/completions", g.handlePooledChat)
	mux.HandleFunc("/v1/status", g.handlePooledStatus)
	mux.HandleFunc("/v1/admin/state", g.handleAdminState)
	mux.HandleFunc("/v1/admin/settings", g.handleAdminSettings)
	mux.HandleFunc("/v1/admin/devshards", g.handleAdminDevshards)
	mux.HandleFunc("/v1/admin/devshards/", g.handleAdminDevshardAction)
	mux.HandleFunc("/v1/admin/escrows", g.handleAdminEscrows)
	mux.HandleFunc("/v1/admin/participants/unquarantine", g.handleAdminUnquarantine)
	mux.HandleFunc("/v1/debug/rotation", g.handleDebugRotation)
	mux.HandleFunc("/v1/finalize", g.handleSingleOnly)
	mux.HandleFunc("/v1/state", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/pending", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/state", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/perf", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/pairwise", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/signatures", g.handleSingleOnly)
	mux.HandleFunc("/v1/debug/signatures/collect", g.handleSingleOnly)
	mux.HandleFunc("/devshard/", g.handleDevshard)
	return mux
}

func (g *Gateway) handlePooledModels(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	defaultModel := g.settings.DefaultModel
	g.mu.Unlock()
	writeModelListWithCapForModel(w, gatewayModelIDs(runtimes, defaultModel), func(model string) uint64 {
		return g.outputTokenLimitsForModel(model).MaxTokensCap
	})
}

func (g *Gateway) handlePooledStatus(w http.ResponseWriter, r *http.Request) {
	g.refreshCapacityScale()
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	if len(runtimes) == 1 {
		runtimes[0].handler.ServeHTTP(w, r)
		return
	}

	statuses := make([]runtimeStatus, 0, len(runtimes))
	for _, rt := range runtimes {
		statuses = append(statuses, rt.snapshot())
	}
	models := g.statusModels(runtimes)
	modelRuntimeStatuses := g.gatewayModelRuntimeStatuses(runtimes)
	writeJSON(w, map[string]any{
		"mode":      "gateway",
		"devshards": statuses,
		"limiter":   g.limiter.SnapshotWithModelCapacities(g.limiterModelCapacities(models, modelRuntimeStatuses)),
		"capacity":  g.capacityStatus(models, modelRuntimeStatuses),
		"runtimes":  len(runtimes),
	})
}

func (g *Gateway) handleSingleOnly(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	if len(runtimes) == 1 {
		if r.URL.Path == "/v1/finalize" && r.Method == http.MethodPost {
			g.finalizeMu.Lock()
			defer g.finalizeMu.Unlock()
			log.Printf("gateway_finalize_lock_acquired escrow=%s path=%s", runtimes[0].id, r.URL.Path)
		}
		runtimes[0].handler.ServeHTTP(w, r)
		return
	}
	http.Error(w, `{"error":{"message":"use /devshard/{id} prefix for this endpoint when multiple devshards are configured"}}`, http.StatusBadRequest)
}

func (g *Gateway) handlePooledChat(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	body, model, inputTokens, err := g.parseChatReservation(r, g.settings.DefaultModel)
	if err != nil {
		logRequestStage(ctx, "gateway_parse_failed", "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), chatRequestErrorStatus(err, http.StatusBadRequest))
		return
	}
	fields := []any{"model", firstNonEmpty(model, g.settings.DefaultModel), "input_tokens", inputTokens}
	fields = append(fields, g.apiKeyLogFields(r)...)
	logRequestStage(ctx, "gateway_request_received", fields...)
	requestModel := firstNonEmpty(model, g.settings.DefaultModel)
	if err := g.validatePooledRequestedModel(requestModel); err != nil {
		logRequestStage(ctx, "gateway_model_rejected", "model", requestModel, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}
	if err := g.modelAccessError(r, requestModel); err != nil {
		logRequestStage(ctx, "gateway_model_temporarily_unavailable", "model", requestModel, "error", err)
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}

	cacheKey := chatCacheKey(requestModel, body)
	stream := chatRequestStream(body)
	if entry, ok := g.chatCache.Get(cacheKey, time.Now()); ok {
		logRequestStage(ctx, "gateway_cache_hit", "escrow", entry.EscrowID, "model", requestModel, "stream", stream)
		g.recordCachedAccountingAlias(ctx, entry)
		serveCachedChatResponse(w, r, entry)
		return
	}
	logRequestStage(ctx, "gateway_cache_miss", "model", requestModel, "stream", stream)

	if capacityAwareLimitsEnabled() || !relaxedPoCBypassActive() {
		g.refreshCapacityScale()
		limitModel := requestModel
		if err := g.limiter.AcquireForModelWithCapacity(limitModel, inputTokens, g.limiterCapacityForModel(limitModel)); err != nil {
			g.metrics.RecordLimitRejection(limiterReasonLabel(err))
			logRequestStage(ctx, "gateway_limiter_rejected", "reason", limiterReasonLabel(err), "input_tokens", inputTokens)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
			return
		}
		defer g.limiter.ReleaseForModel(limitModel, inputTokens)
		logRequestStage(ctx, "gateway_limiter_acquired", "input_tokens", inputTokens)
	} else {
		logRequestStage(ctx, "gateway_limiter_bypassed_during_poc", "input_tokens", inputTokens, "reason", currentPoCPhaseReason())
	}

	rt, err := g.reserveRuntimeForModel(model, inputTokens)
	if err != nil {
		logRequestStage(ctx, "gateway_runtime_select_failed", "error", err)
		if isParticipantRateLimitError(err) {
			g.metrics.RecordParticipantLimitRejection("pooled_route")
		}
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
		return
	}
	defer g.releaseRuntime(rt, inputTokens)
	logRequestStage(ctx, "gateway_runtime_selected", "escrow", rt.id)

	if capture := g.serveChatToRuntime(rt, "/v1/chat/completions", body, w, r); capture != nil {
		sourceRequestID, _ := requestLogFromContext(ctx)
		if entry, ok := capture.cacheEntry(rt.id, stream, sourceRequestID); ok {
			g.chatCache.Set(cacheKey, entry, time.Now())
			logRequestStage(ctx, "gateway_cache_stored", "escrow", rt.id, "model", requestModel, "stream", stream, "bytes", len(entry.Body))
		}
	}
}

func (g *Gateway) validatePooledRequestedModel(requestModel string) error {
	requestModel = strings.TrimSpace(requestModel)
	if g == nil || requestModel == "" {
		return nil
	}
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	if len(runtimes) == 0 {
		return nil
	}
	for _, rt := range runtimes {
		if rt != nil && rt.model == requestModel {
			return nil
		}
	}
	return &UnsupportedModelError{Model: requestModel, Supported: supportedModels(runtimes)}
}

func (g *Gateway) handleDevshard(w http.ResponseWriter, r *http.Request) {
	ctx, _ := ensureRequestLogContext(r.Context())
	r = r.WithContext(ctx)
	devshardID, innerPath, ok := parseDevshardPath(r.URL.Path)
	if !ok {
		logRequestStage(ctx, "gateway_devshard_path_invalid", "path", r.URL.Path)
		http.NotFound(w, r)
		return
	}
	fields := []any{"escrow", devshardID, "path", innerPath}
	fields = append(fields, g.apiKeyLogFields(r)...)
	logRequestStage(ctx, "gateway_devshard_request_received", fields...)

	g.mu.Lock()
	rt, ok := g.runtimes[devshardID]
	g.mu.Unlock()
	if !ok {
		logRequestStage(ctx, "gateway_devshard_not_found", "escrow", devshardID)
		http.Error(w, fmt.Sprintf(`{"error":{"message":"unknown devshard %s"}}`, devshardID), http.StatusNotFound)
		return
	}

	if innerPath == "/v1/chat/completions" {
		if ok, reason := rt.acceptsNewInferences(); !ok {
			logRequestStage(ctx, "gateway_devshard_unavailable", "escrow", devshardID, "reason", reason)
			http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s is unavailable for new inferences: %s"}}`, devshardID, reason), http.StatusConflict)
			return
		}
		body, model, inputTokens, err := g.parseChatReservation(r, firstNonEmpty(rt.model, g.settings.DefaultModel))
		if err != nil {
			logRequestStage(ctx, "gateway_devshard_parse_failed", "escrow", devshardID, "error", err)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), chatRequestErrorStatus(err, http.StatusBadRequest))
			return
		}
		if err := rt.validateRequestedModel(model); err != nil {
			logRequestStage(ctx, "gateway_devshard_model_rejected", "escrow", devshardID, "model", model, "error", err)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
			return
		}
		limitModel := firstNonEmpty(model, rt.model, g.settings.DefaultModel)
		if err := g.modelAccessError(r, limitModel); err != nil {
			logRequestStage(ctx, "gateway_devshard_model_temporarily_unavailable", "escrow", devshardID, "model", limitModel, "error", err)
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), gatewayStatusCodeForError(err))
			return
		}
		cacheKey := chatCacheKey(limitModel, body)
		stream := chatRequestStream(body)
		if entry, ok := g.chatCache.Get(cacheKey, time.Now()); ok {
			logRequestStage(ctx, "gateway_devshard_cache_hit", "escrow", entry.EscrowID, "model", limitModel, "stream", stream)
			g.recordCachedAccountingAlias(ctx, entry)
			serveCachedChatResponse(w, r, entry)
			return
		}
		logRequestStage(ctx, "gateway_devshard_cache_miss", "escrow", devshardID, "model", limitModel, "stream", stream)
		if capacityAwareLimitsEnabled() || !relaxedPoCBypassActive() {
			g.refreshCapacityScale()
			if err := g.limiter.AcquireForModelWithCapacity(limitModel, inputTokens, g.limiterCapacityForModel(limitModel)); err != nil {
				g.metrics.RecordLimitRejection(limiterReasonLabel(err))
				logRequestStage(ctx, "gateway_devshard_limiter_rejected", "escrow", devshardID, "reason", limiterReasonLabel(err), "input_tokens", inputTokens)
				http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusTooManyRequests)
				return
			}
			defer g.limiter.ReleaseForModel(limitModel, inputTokens)
			logRequestStage(ctx, "gateway_devshard_limiter_acquired", "escrow", devshardID, "input_tokens", inputTokens)
		} else {
			logRequestStage(ctx, "gateway_devshard_limiter_bypassed_during_poc", "escrow", devshardID, "input_tokens", inputTokens, "reason", currentPoCPhaseReason())
		}

		g.reserveRuntime(rt, inputTokens)
		defer g.releaseRuntime(rt, inputTokens)
		logRequestStage(ctx, "gateway_devshard_runtime_selected", "escrow", devshardID, "input_tokens", inputTokens)

		if capture := g.serveChatToRuntime(rt, innerPath, body, w, r); capture != nil {
			sourceRequestID, _ := requestLogFromContext(ctx)
			if entry, ok := capture.cacheEntry(rt.id, stream, sourceRequestID); ok {
				g.chatCache.Set(cacheKey, entry, time.Now())
				logRequestStage(ctx, "gateway_devshard_cache_stored", "escrow", rt.id, "model", limitModel, "stream", stream, "bytes", len(entry.Body))
			}
		}
		return
	}
	if innerPath == "/v1/finalize" && r.Method == http.MethodPost {
		if rt.activeRequests.Load() > 0 {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s has active requests"}}`, devshardID), http.StatusConflict)
			return
		}
		g.finalizeMu.Lock()
		defer g.finalizeMu.Unlock()
		log.Printf("gateway_finalize_lock_acquired escrow=%s path=%s", devshardID, r.URL.Path)
		req := cloneRequestWithBody(r, nil)
		req.URL.Path = innerPath
		req.URL.RawPath = innerPath
		req.RequestURI = innerPath
		w.Header().Set("X-Devshard-ID", devshardID)
		capture := &gatewayStatusResponseWriter{ResponseWriter: w}
		rt.handler.ServeHTTP(capture, req)
		if status := capture.statusCode(); status >= 200 && status < 300 {
			g.markDevshardInactiveAfterFinalize(devshardID, rt)
		}
		return
	}

	req := cloneRequestWithBody(r, nil)
	req.URL.Path = innerPath
	req.URL.RawPath = innerPath
	req.RequestURI = innerPath
	w.Header().Set("X-Devshard-ID", devshardID)
	rt.handler.ServeHTTP(w, req)
}

type gatewayStatusResponseWriter struct {
	http.ResponseWriter
	status int
}

func (w *gatewayStatusResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *gatewayStatusResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *gatewayStatusResponseWriter) statusCode() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

func (g *Gateway) markDevshardInactiveAfterFinalize(id string, rt *devshardRuntime) {
	rt.active.Store(false)
	if g.store == nil {
		return
	}
	if err := g.store.SetDevshardActive(id, false); err != nil {
		log.Printf("finalize: persist deactivation for devshard %s: %v", id, err)
	}
}

func (g *Gateway) serveChatToRuntime(rt *devshardRuntime, path string, body []byte, w http.ResponseWriter, r *http.Request) *gatewayChatCacheCapture {
	req := cloneRequestWithBody(r, body)
	req.URL.Path = path
	req.URL.RawPath = path
	req.RequestURI = path
	w.Header().Set("X-Devshard-ID", rt.id)
	logRequestStage(req.Context(), "gateway_request_forwarded", "escrow", rt.id, "path", path)
	capture := &gatewayChatCacheCapture{ResponseWriter: w}
	rt.handler.ServeHTTP(capture, req)
	return capture
}

func (g *Gateway) recordCachedAccountingAlias(ctx context.Context, entry cachedChatResponse) {
	requestID, ok := requestLogFromContext(ctx)
	if !ok || requestID == "" || entry.SourceRequestID == "" || entry.EscrowID == "" {
		return
	}
	perf := g.perf
	if perf == nil {
		g.mu.Lock()
		rt := g.runtimes[entry.EscrowID]
		g.mu.Unlock()
		if rt != nil && rt.proxy != nil {
			perf = rt.proxy.perf
		}
	}
	if perf == nil {
		return
	}
	perf.RecordAccountingAlias(requestID, entry.EscrowID, entry.SourceRequestID, entry.EscrowID, "cache_hit", time.Now())
	logRequestStage(ctx, "gateway_cache_accounting_alias", "escrow", entry.EscrowID, "source_request_id", entry.SourceRequestID)
}

func (g *Gateway) reserveRuntimeForModel(requestModel string, inputTokens int64) (*devshardRuntime, error) {
	g.mu.Lock()
	var depletedEscrows []struct {
		id     string
		model  string
		reason string
	}
	defer func() {
		g.mu.Unlock()
		for _, depleted := range depletedEscrows {
			g.scheduleDepletedEscrowReplacement(depleted.id, depleted.model, depleted.reason)
		}
	}()

	var candidates []*devshardRuntime
	skipReasonCounts := make(map[string]int)
	for _, rt := range g.runtimeOrder {
		if g.runtimeAtNonceLimit(rt) {
			if g.settings.EscrowRotation.Enabled {
				depletedEscrows = append(depletedEscrows, struct {
					id     string
					model  string
					reason string
				}{id: rt.id, model: rt.model, reason: "high_nonce"})
			}
			skipReasonCounts["high_nonce"]++
			continue
		}
		ok, reason := rt.acceptsNewInferences()
		if !ok {
			skipReasonCounts[runtimeSkipReasonKey(reason)]++
			continue
		}
		candidates = append(candidates, rt)
	}
	if len(candidates) == 0 {
		if summary := formatRuntimeSkipReasonCounts(skipReasonCounts); summary != "" {
			return nil, fmt.Errorf("no devshard runtimes available for new inferences (skipped: %s)", summary)
		}
		return nil, fmt.Errorf("no devshard runtimes available for new inferences")
	}
	if requestModel != "" {
		var matching []*devshardRuntime
		for _, rt := range candidates {
			if rt.model == requestModel {
				matching = append(matching, rt)
			}
		}
		if len(matching) == 0 {
			return nil, &UnsupportedModelError{Model: requestModel, Supported: supportedModels(candidates)}
		}
		candidates = matching
	}

	bestScore := g.runtimeLoad(candidates[0], requestModel)
	best := []*devshardRuntime{candidates[0]}
	for _, rt := range candidates[1:] {
		score := g.runtimeLoad(rt, requestModel)
		switch {
		case score < bestScore:
			bestScore = score
			best = []*devshardRuntime{rt}
		case score == bestScore:
			best = append(best, rt)
		}
	}

	// All candidates score +Inf only when every escrow's W(e) == 0 -
	// i.e. every host is PoC-excluded or fully throttled. Surface this
	// as a participant-rate-limit error so callers see the existing
	// 429 path instead of dispatching a request that is guaranteed to
	// fail upstream. We deliberately don't enumerate which hosts caused
	// it: a host can have W(e)==0 for many reasons (raw capacity 0, PoC
	// exclusion, reactive throttle, share rounding) and surfacing only
	// the throttled subset would mislead operators about the root
	// cause. Per-escrow W(e) is logged below for diagnostics.
	if math.IsInf(bestScore, +1) {
		log.Printf(
			"gateway: all %d candidate escrow(s) at zero capacity, returning 429; per-escrow weights: %s",
			len(candidates), g.formatCandidateWeightsLocked(candidates, requestModel),
		)
		return nil, &EscrowParticipantRateLimitError{}
	}

	chosen := best[0]
	if len(best) > 1 {
		idx := int(g.roundRobinSeed.Add(1)-1) % len(best)
		chosen = best[idx]
	}
	g.reserveRuntimeLocked(chosen, inputTokens)
	if g.metrics != nil {
		g.metrics.RecordPickerChoice(chosen.id, chosen.model)
	}
	return chosen, nil
}

func (g *Gateway) runtimeAtNonceLimit(rt *devshardRuntime) bool {
	if rt == nil || !rt.active.Load() || rt.proxy == nil || rt.proxy.sm == nil {
		return false
	}
	nonce := rt.proxy.sm.LatestNonce()
	return nonce >= nonceDeactivationLimit
}

// formatCandidateWeightsLocked returns a compact "id=W(e)" diagnostic
// string for log output when every escrow scored +Inf. Operators use
// this to tell whether the cause was a system-wide PoC pause (every
// W(e) == 0 simultaneously), a single hot escrow (one weight low),
// or a missing capacity-model registration (HasEscrow false).
func (g *Gateway) formatCandidateWeightsLocked(candidates []*devshardRuntime, requestModel string) string {
	parts := make([]string, 0, len(candidates))
	for _, rt := range candidates {
		if g.capacity != nil && g.capacity.HasEscrow(rt.id) {
			model := firstNonEmpty(requestModel, rt.model)
			parts = append(parts, fmt.Sprintf("%s=%g", rt.id, g.capacity.EscrowWeightForModel(rt.id, model)))
		} else {
			parts = append(parts, fmt.Sprintf("%s=unregistered", rt.id))
		}
	}
	return strings.Join(parts, " ")
}

// runtimeLoad bridges the gateway and the devshardRuntime: it pulls the
// effective weight W(e) from the CapacityState and feeds it into the
// runtime's load formula. Kept on the gateway so the runtime stays
// free of state dependencies.
//
// Fallback rules:
//   - No capacity state attached, or escrow not registered with the
//     state (no slot/membership info): use neutral weight 1.0 so the
//     picker degrades to a pure activeRequests comparison.
//   - Escrow registered but W(e) == 0 (every host is PoC-excluded or
//     fully throttled): honor the 0 so the runtime drops to +Inf load
//     and stops receiving traffic until at least one host recovers.
func (g *Gateway) runtimeLoad(rt *devshardRuntime, requestModel string) float64 {
	if g == nil || g.capacity == nil || !g.capacity.HasEscrow(rt.id) {
		return rt.load(1.0)
	}
	return rt.load(g.capacity.EscrowWeightForModel(rt.id, firstNonEmpty(requestModel, rt.model)))
}

func (g *Gateway) reserveRuntime(rt *devshardRuntime, inputTokens int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.reserveRuntimeLocked(rt, inputTokens)
}

func (g *Gateway) reserveRuntimeLocked(rt *devshardRuntime, inputTokens int64) {
	rt.activeRequests.Add(1)
	rt.reservedTokens.Add(inputTokens)
}

func (g *Gateway) releaseRuntime(rt *devshardRuntime, inputTokens int64) {
	rt.activeRequests.Add(-1)
	rt.reservedTokens.Add(-inputTokens)
}

func (rt *devshardRuntime) validateRequestedModel(requestModel string) error {
	if rt == nil || requestModel == "" || requestModel == rt.model {
		return nil
	}
	return &UnsupportedModelError{Model: requestModel, Supported: []string{rt.model}}
}

func supportedModels(runtimes []*devshardRuntime) []string {
	models := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		if rt == nil || rt.model == "" || slices.Contains(models, rt.model) {
			continue
		}
		models = append(models, rt.model)
	}
	return models
}

func gatewayModelIDs(runtimes []*devshardRuntime, fallback string) []string {
	models := make([]string, 0, len(runtimes))
	for _, rt := range runtimes {
		if rt == nil || !rt.active.Load() || rt.model == "" || slices.Contains(models, rt.model) {
			continue
		}
		models = append(models, rt.model)
	}
	if len(models) == 0 {
		fallback = strings.TrimSpace(fallback)
		if fallback != "" {
			models = append(models, fallback)
		}
	}
	return models
}

type modelListResponse struct {
	Object string            `json:"object"`
	Data   []modelDescriptor `json:"data"`
}

type modelDescriptor struct {
	ID                  string            `json:"id"`
	Object              string            `json:"object"`
	Created             int64             `json:"created"`
	OwnedBy             string            `json:"owned_by"`
	Name                string            `json:"name"`
	Description         string            `json:"description,omitempty"`
	ContextLength       uint64            `json:"context_length,omitempty"`
	MaxCompletionTokens uint64            `json:"max_completion_tokens,omitempty"`
	Architecture        modelArchitecture `json:"architecture"`
	Pricing             modelPricing      `json:"pricing"`
	TopProvider         modelTopProvider  `json:"top_provider"`
	PerRequestLimits    map[string]any    `json:"per_request_limits,omitempty"`
	SupportedParameters []string          `json:"supported_parameters,omitempty"`
	InputModalities     []string          `json:"input_modalities,omitempty"`
	OutputModalities    []string          `json:"output_modalities,omitempty"`
}

type modelArchitecture struct {
	Modality         string   `json:"modality"`
	InputModalities  []string `json:"input_modalities"`
	OutputModalities []string `json:"output_modalities"`
	Tokenizer        string   `json:"tokenizer,omitempty"`
	InstructType     string   `json:"instruct_type,omitempty"`
}

type modelPricing struct {
	Prompt            string `json:"prompt"`
	Completion        string `json:"completion"`
	Request           string `json:"request"`
	Image             string `json:"image,omitempty"`
	WebSearch         string `json:"web_search,omitempty"`
	InternalReasoning string `json:"internal_reasoning,omitempty"`
	InputCacheRead    string `json:"input_cache_read,omitempty"`
	InputCacheWrite   string `json:"input_cache_write,omitempty"`
}

type modelTopProvider struct {
	ContextLength       uint64 `json:"context_length,omitempty"`
	MaxCompletionTokens uint64 `json:"max_completion_tokens,omitempty"`
	IsModerated         bool   `json:"is_moderated"`
}

func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	if !allowGetOrHead(w, r) {
		return
	}
	writeModelList(w, []string{p.model}, RequestMaxTokensCap)
}

func allowGetOrHead(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		return true
	}
	w.Header().Set("Allow", "GET, HEAD")
	http.Error(w, `{"error":{"message":"method not allowed"}}`, http.StatusMethodNotAllowed)
	return false
}

func writeModelList(w http.ResponseWriter, modelIDs []string, maxTokens uint64) {
	if maxTokens == 0 {
		maxTokens = RequestMaxTokensCap
	}
	writeModelListWithCapForModel(w, modelIDs, func(string) uint64 {
		return maxTokens
	})
}

func writeModelListWithCapForModel(w http.ResponseWriter, modelIDs []string, capForModel func(string) uint64) {
	created := time.Now().Unix()
	data := make([]modelDescriptor, 0, len(modelIDs))
	for _, id := range modelIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		maxTokens := RequestMaxTokensCap
		if capForModel != nil {
			if configured := capForModel(id); configured > 0 {
				maxTokens = configured
			}
		}
		data = append(data, modelDescriptor{
			ID:                  id,
			Object:              "model",
			Created:             created,
			OwnedBy:             "gonka",
			Name:                id,
			Description:         "Gonka devshard gateway model.",
			ContextLength:       maxTokens,
			MaxCompletionTokens: maxTokens,
			Architecture: modelArchitecture{
				Modality:         "text->text",
				InputModalities:  []string{"text"},
				OutputModalities: []string{"text"},
				Tokenizer:        "Other",
			},
			Pricing: modelPricing{
				Prompt:     "0",
				Completion: "0",
				Request:    "0",
			},
			TopProvider: modelTopProvider{
				ContextLength:       maxTokens,
				MaxCompletionTokens: maxTokens,
				IsModerated:         false,
			},
			SupportedParameters: []string{"messages", "max_tokens", "stream", "temperature", "top_p", "stop"},
			InputModalities:     []string{"text"},
			OutputModalities:    []string{"text"},
		})
	}
	writeJSON(w, modelListResponse{Object: "list", Data: data})
}

func gatewayStatusCodeForError(err error) int {
	var unsupportedModelErr *UnsupportedModelError
	if errors.As(err, &unsupportedModelErr) {
		return http.StatusBadRequest
	}
	var unavailableModelErr *ModelTemporarilyUnavailableError
	if errors.As(err, &unavailableModelErr) {
		return http.StatusServiceUnavailable
	}
	var accessDeniedErr *ModelAccessDeniedError
	if errors.As(err, &accessDeniedErr) {
		if accessDeniedErr.StatusCode != 0 {
			return accessDeniedErr.StatusCode
		}
		return http.StatusUnauthorized
	}
	if isParticipantRateLimitError(err) {
		return http.StatusTooManyRequests
	}
	var reducedTokenTimeoutErr *nonStreamingReducedMaxTokensTimeoutError
	if errors.As(err, &reducedTokenTimeoutErr) {
		return http.StatusGatewayTimeout
	}
	var admissionErr *RequestAdmissionError
	if errors.As(err, &admissionErr) {
		return http.StatusServiceUnavailable
	}
	var upstreamErr *transport.UpstreamStatusError
	if errors.As(err, &upstreamErr) && isParticipantThrottleStatus(upstreamErr.StatusCode) {
		return http.StatusTooManyRequests
	}
	return http.StatusBadGateway
}

func isParticipantRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	var participantErr *ParticipantRateLimitError
	if errors.As(err, &participantErr) {
		return true
	}
	var escrowErr *EscrowParticipantRateLimitError
	return errors.As(err, &escrowErr)
}

func parseDevshardPath(path string) (devshardID, innerPath string, ok bool) {
	trimmed := strings.TrimPrefix(path, "/devshard/")
	if trimmed == path {
		return "", "", false
	}
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		return "", "", false
	}
	return parts[0], "/" + parts[1], true
}

func cloneRequestWithBody(r *http.Request, body []byte) *http.Request {
	req := r.Clone(r.Context())
	req.URL = cloneURL(r.URL)
	if body != nil {
		req.Body = io.NopCloser(bytes.NewReader(body))
		req.ContentLength = int64(len(body))
	}
	return req
}

func cloneURL(u *url.URL) *url.URL {
	if u == nil {
		return &url.URL{}
	}
	clone := *u
	return &clone
}

func (g *Gateway) parseChatReservation(r *http.Request, defaultModel string) ([]byte, string, int64, error) {
	body, err := readLimitedChatRequestBody(r)
	if err != nil {
		return nil, "", 0, err
	}
	originalBody := append([]byte(nil), body...)
	logResponseFormatDiagnostics(r.Context(), body)
	model := chatRequestModel(body)
	routedModel := firstNonEmpty(model, defaultModel)
	limits := g.outputTokenLimitsForModel(routedModel)
	updatedBody, req, err := normalizeChatRequestForAuthAndLimits(body, requestHasAdminAuth(r), limits, routedModel)
	if err != nil {
		captureFilterRejectedRequest(r, originalBody, err, model, "")
		return nil, "", 0, err
	}

	inputTokens := estimatePromptTokens(updatedBody)
	return updatedBody, req.Model, inputTokens, nil
}

func chatRequestModel(body []byte) string {
	var req chatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return ""
	}
	return strings.TrimSpace(req.Model)
}

func estimatePromptTokens(body []byte) int64 {
	if len(body) == 0 {
		return 1
	}
	// Approximate tokenizer: 1 token ~= 4 bytes. Good enough for admission control.
	estimate := (len(body) + 3) / 4
	if estimate < 1 {
		estimate = 1
	}
	return int64(estimate)
}

func resolveRuntimeConfigs(singleEscrowID, singleKeyHex, singleModel, singleStoragePath string) ([]RuntimeConfig, error) {
	if raw := strings.TrimSpace(os.Getenv("DEVSHARDS_JSON")); raw != "" {
		var runtimes []RuntimeConfig
		if err := json.Unmarshal([]byte(raw), &runtimes); err != nil {
			return nil, fmt.Errorf("parse DEVSHARDS_JSON: %w", err)
		}
		return runtimes, nil
	}

	if singleEscrowID == "" || singleKeyHex == "" {
		return nil, fmt.Errorf("--private-key/--escrow-id or DEVSHARD_PRIVATE_KEY/DEVSHARD_ESCROW_ID required")
	}

	return []RuntimeConfig{{
		ID:            singleEscrowID,
		PrivateKeyHex: singleKeyHex,
		Model:         singleModel,
		StoragePath:   singleStoragePath,
	}}, nil
}

func defaultStoragePath(baseStorageDir, escrowID string) string {
	return filepath.Join(baseStorageDir, fmt.Sprintf("escrow-%s", escrowID))
}

func normalizeStorageDir(storagePath string) string {
	storagePath = strings.TrimSpace(storagePath)
	if storagePath == "" {
		return ""
	}
	clean := filepath.Clean(storagePath)
	if filepath.Base(clean) == "state.db" {
		return filepath.Dir(clean)
	}
	return clean
}

func migrateGatewayLegacyStorage(storageDir, originalStoragePath, escrowID string, br bridge.MainnetBridge) error {
	storageDir = strings.TrimSpace(storageDir)
	if storageDir == "" {
		return nil
	}
	sqlStore, err := storage.NewSQLite(storageDir)
	if err != nil {
		return fmt.Errorf("open storage: %w", err)
	}
	defer sqlStore.Close()

	legacyPath := legacyStateDBPath(originalStoragePath, storageDir)
	migrated, err := storage.MigrateLegacySQLite(legacyPath, sqlStore, func(sessionEscrowID string) (uint64, error) {
		if sessionEscrowID != escrowID {
			return 0, fmt.Errorf("%w: legacy session %s does not belong to gateway runtime %s", storage.ErrSkipLegacySession, sessionEscrowID, escrowID)
		}
		info, gErr := br.GetEscrow(sessionEscrowID)
		if gErr != nil {
			if errors.Is(gErr, bridge.ErrEscrowNotFound) {
				return 0, storage.ErrSkipLegacySession
			}
			return 0, gErr
		}
		return info.EpochID, nil
	})
	if err != nil {
		return err
	}
	if migrated > 0 {
		log.Printf("runtime %s: legacy storage migration complete: sessions_migrated=%d", escrowID, migrated)
	}
	return nil
}

func legacyStateDBPath(originalStoragePath, storageDir string) string {
	originalStoragePath = strings.TrimSpace(originalStoragePath)
	if originalStoragePath != "" {
		clean := filepath.Clean(originalStoragePath)
		if filepath.Base(clean) == "state.db" {
			return clean
		}
	}
	return filepath.Join(storageDir, "state.db")
}

func legacyPerfSourcePath(storagePath string) string {
	storagePath = strings.TrimSpace(storagePath)
	if storagePath == "" {
		return ""
	}
	clean := filepath.Clean(storagePath)
	if filepath.Base(clean) == "state.db" {
		return clean
	}
	return ""
}

type adminDevshardRequest struct {
	ID              string `json:"id"`
	PrivateKey      string `json:"private_key,omitempty"`
	PrivateKeyEnv   string `json:"private_key_env,omitempty"`
	Model           string `json:"model,omitempty"`
	StoragePath     string `json:"storage_path,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
}

type adminImportDevshardRequest struct {
	adminDevshardRequest
	Active   *bool  `json:"active,omitempty"`
	PerfPath string `json:"perf_path,omitempty"`
}

type adminCreateEscrowRequest struct {
	PrivateKey      string `json:"private_key,omitempty"`
	PrivateKeyEnv   string `json:"private_key_env,omitempty"`
	Amount          uint64 `json:"amount"`
	ModelID         string `json:"model_id,omitempty"`
	Register        *bool  `json:"register,omitempty"`
	StoragePath     string `json:"storage_path,omitempty"`
	ProtocolVersion string `json:"protocol_version,omitempty"`
	ChainID         string `json:"chain_id,omitempty"`
	FeeDenom        string `json:"fee_denom,omitempty"`
	FeeAmount       uint64 `json:"fee_amount,omitempty"`
	GasLimit        uint64 `json:"gas_limit,omitempty"`
}

type adminSettleEscrowRequest struct {
	PrivateKey    string `json:"private_key,omitempty"`
	PrivateKeyEnv string `json:"private_key_env,omitempty"`
	ChainID       string `json:"chain_id,omitempty"`
	FeeDenom      string `json:"fee_denom,omitempty"`
	FeeAmount     uint64 `json:"fee_amount,omitempty"`
	GasLimit      uint64 `json:"gas_limit,omitempty"`
}

type adminSettingsRequest struct {
	ChainREST                      *string                          `json:"chain_rest,omitempty"`
	PublicAPI                      *string                          `json:"public_api,omitempty"`
	DefaultModel                   *string                          `json:"default_model,omitempty"`
	MaxConcurrentRequests          *int64                           `json:"max_concurrent_requests,omitempty"`
	MaxConcurrentPer10000Weight    *float64                         `json:"max_concurrent_requests_per_10000_weight,omitempty"`
	PoCMaxConcurrentPer10000Weight *float64                         `json:"poc_max_concurrent_requests_per_10000_weight,omitempty"`
	MaxInputTokensInFlight         *int64                           `json:"max_input_tokens_in_flight,omitempty"`
	ModelLimits                    *[]GatewayModelLimitSettings     `json:"model_limits,omitempty"`
	DefaultRequestMaxTokens        *uint64                          `json:"default_request_max_tokens,omitempty"`
	RequestMaxTokensCap            *uint64                          `json:"request_max_tokens_cap,omitempty"`
	TxGasLimit                     *uint64                          `json:"tx_gas_limit,omitempty"`
	Disabled                       *adminGatewayDisabledRequest     `json:"disabled,omitempty"`
	ParticipantThrottle            *adminParticipantThrottleRequest `json:"participant_throttle,omitempty"`
	Redundancy                     *adminRedundancyRequest          `json:"redundancy,omitempty"`
	Perf                           *adminPerfRequest                `json:"perf,omitempty"`
	EscrowRotation                 *adminEscrowRotationRequest      `json:"escrow_rotation,omitempty"`
}

type adminGatewayDisabledRequest struct {
	Enabled *bool   `json:"enabled,omitempty"`
	Message *string `json:"message,omitempty"`
	NewURL  *string `json:"new_url,omitempty"`
}

type adminParticipantThrottleRequest struct {
	RequestBurst                   *int   `json:"request_burst,omitempty"`
	RecoveryPerMinute              *int   `json:"recovery_per_minute,omitempty"`
	HTTPQuarantineMS               *int64 `json:"http_quarantine_ms,omitempty"`
	TransportFailureQuarantineMS   *int64 `json:"transport_failure_quarantine_ms,omitempty"`
	EmptyStreamQuarantineMS        *int64 `json:"empty_stream_quarantine_ms,omitempty"`
	StalledWinnerQuarantineMS      *int64 `json:"stalled_winner_quarantine_ms,omitempty"`
	EmptyStreamQuarantineThreshold *int   `json:"empty_stream_threshold,omitempty"`
}

type adminRedundancyRequest struct {
	ReceiptTimeoutMS              *int64   `json:"receipt_timeout_ms,omitempty"`
	FirstTokenTimeoutFloorMS      *int64   `json:"first_token_timeout_floor_ms,omitempty"`
	PerInputTokenFirstTokenLagMS  *int64   `json:"per_input_token_first_token_lag_ms,omitempty"`
	InterChunkStallTimeoutMS      *int64   `json:"inter_chunk_stall_timeout_ms,omitempty"`
	StreamingAttemptHardTimeoutMS *int64   `json:"streaming_attempt_hard_timeout_ms,omitempty"`
	NonStreamResponseFloorMS      *int64   `json:"non_stream_response_floor_ms,omitempty"`
	NonStreamNoContentTimeoutMS   *int64   `json:"non_stream_no_content_timeout_ms,omitempty"`
	NonStreamMaxAttemptWaitMS     *int64   `json:"non_stream_max_attempt_wait_ms,omitempty"`
	PerInputTokenResponseLagMS    *int64   `json:"per_input_token_response_lag_ms,omitempty"`
	SecondaryWaitAfterWinnerMS    *int64   `json:"secondary_wait_after_winner_ms,omitempty"`
	ParallelAdvantageThreshold    *float64 `json:"parallel_advantage_threshold,omitempty"`
	UnresponsiveThreshold         *float64 `json:"unresponsive_threshold,omitempty"`
	SpeedPolicy                   *string  `json:"speed_policy,omitempty"`
	PairwiseBudgetPercentile      *float64 `json:"pairwise_budget_percentile,omitempty"`
	PairwiseMaxProactiveAttempts  *int     `json:"pairwise_max_proactive_attempts,omitempty"`
	PairwiseMinDirectComparisons  *int     `json:"pairwise_min_direct_comparisons,omitempty"`
	PairwiseWinnerHoldMS          *int64   `json:"pairwise_winner_hold_ms,omitempty"`
	PairwiseWinnerHoldMinSpeedup  *float64 `json:"pairwise_winner_hold_min_speedup,omitempty"`
	PairwiseWinnerHoldMinSamples  *int     `json:"pairwise_winner_hold_min_samples,omitempty"`
}

type adminPerfRequest struct {
	SampleSize *int   `json:"sample_size,omitempty"`
	WindowMS   *int64 `json:"window_ms,omitempty"`
}

type adminEscrowRotationRequest struct {
	Enabled           *bool                          `json:"enabled,omitempty"`
	SettlementEnabled *bool                          `json:"settlement_enabled,omitempty"`
	PrePoCBlocks      *int64                         `json:"pre_poc_blocks,omitempty"`
	Models            *[]EscrowRotationModelSettings `json:"models,omitempty"`
}

func (g *Gateway) handleAdminState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	g.refreshCapacityScale()
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	g.mu.Lock()
	runtimes := append([]*devshardRuntime(nil), g.runtimeOrder...)
	g.mu.Unlock()
	models := g.statusModels(runtimes)
	modelRuntimeStatuses := g.gatewayModelRuntimeStatuses(runtimes)
	if !ok {
		writeJSON(w, map[string]any{
			"settings":  g.settings,
			"devshards": []GatewayDevshardState{},
			"limiter":   g.limiter.SnapshotWithModelCapacities(g.limiterModelCapacities(models, modelRuntimeStatuses)),
			"capacity":  g.capacityStatus(models, modelRuntimeStatuses),
		})
		return
	}

	runtimeByID := make(map[string]runtimeStatus, len(runtimes))
	for _, rt := range runtimes {
		runtimeByID[rt.id] = rt.snapshot()
	}

	type adminDevshardView struct {
		GatewayDevshardState
		Runtime *runtimeStatus `json:"runtime,omitempty"`
	}
	views := make([]adminDevshardView, 0, len(state.Devshards))
	for _, devshard := range state.Devshards {
		view := adminDevshardView{GatewayDevshardState: devshard}
		if snapshot, ok := runtimeByID[devshard.ID]; ok {
			s := snapshot
			view.Runtime = &s
		}
		views = append(views, view)
	}
	writeJSON(w, map[string]any{
		"settings":  state.Settings,
		"devshards": views,
		"limiter":   g.limiter.SnapshotWithModelCapacities(g.limiterModelCapacities(models, modelRuntimeStatuses)),
		"capacity":  g.capacityStatus(models, modelRuntimeStatuses),
	})
}

func (g *Gateway) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	switch r.Method {
	case http.MethodGet:
		g.mu.Lock()
		settings := g.settings
		g.mu.Unlock()
		writeJSON(w, settings)
	case http.MethodPost:
		var req adminSettingsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}

		g.mu.Lock()
		settings := g.settings
		if req.ChainREST != nil {
			settings.ChainREST = strings.TrimSpace(*req.ChainREST)
		}
		if req.PublicAPI != nil {
			settings.PublicAPI = strings.TrimSpace(*req.PublicAPI)
		}
		if req.DefaultModel != nil {
			settings.DefaultModel = strings.TrimSpace(*req.DefaultModel)
		}
		if req.MaxConcurrentRequests != nil {
			settings.MaxConcurrentRequests = *req.MaxConcurrentRequests
		}
		if req.MaxConcurrentPer10000Weight != nil {
			settings.MaxConcurrentPer10000Weight = *req.MaxConcurrentPer10000Weight
		}
		if req.PoCMaxConcurrentPer10000Weight != nil {
			settings.PoCMaxConcurrentPer10000Weight = *req.PoCMaxConcurrentPer10000Weight
		}
		if req.MaxInputTokensInFlight != nil {
			settings.MaxInputTokensInFlight = *req.MaxInputTokensInFlight
		}
		if req.ModelLimits != nil {
			settings.ModelLimits = normalizeGatewayModelLimits(*req.ModelLimits)
		}
		if req.DefaultRequestMaxTokens != nil {
			settings.DefaultRequestMaxTokens = *req.DefaultRequestMaxTokens
		}
		if req.RequestMaxTokensCap != nil {
			settings.RequestMaxTokensCap = *req.RequestMaxTokensCap
		}
		if req.TxGasLimit != nil {
			settings.TxGasLimit = *req.TxGasLimit
		}
		if req.Disabled != nil {
			applyGatewayDisabledRequest(&settings.Disabled, req.Disabled)
		}
		if req.ParticipantThrottle != nil {
			applyParticipantThrottleRequest(&settings.ParticipantThrottle, req.ParticipantThrottle)
		}
		if req.Redundancy != nil {
			applyRedundancyRequest(&settings.Redundancy, req.Redundancy)
		}
		if req.Perf != nil {
			applyPerfRequest(&settings.Perf, req.Perf)
		}
		if req.EscrowRotation != nil {
			applyEscrowRotationRequest(&settings.EscrowRotation, req.EscrowRotation)
		}
		if err := validateGatewaySettings(settings); err != nil {
			g.mu.Unlock()
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}
		if err := g.store.UpdateSettings(settings); err != nil {
			g.mu.Unlock()
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
			return
		}
		g.settings = settings
		if g.phaseGate != nil {
			g.phaseGate.Stop()
		}
		g.phaseGate = NewChainPhaseGate(settings.PublicAPI, 0)
		if g.phaseGate != nil {
			g.phaseGate.SetPreservedSnapshotBaseURL(settings.ChainREST)
		}
		for _, rt := range g.runtimeOrder {
			g.attachRuntimeSharedState(rt)
		}
		if g.phaseGate != nil {
			g.attachCapacityStateToPhaseGate()
			g.phaseGate.Start()
		}
		g.limiter.UpdateLimits(settings.MaxConcurrentRequests, settings.MaxInputTokensInFlight, settings.ModelLimits)
		DefaultRequestMaxTokens = settings.DefaultRequestMaxTokens
		RequestMaxTokensCap = settings.RequestMaxTokensCap
		applyGatewayTuningSettings(settings)
		if g.perf != nil {
			g.perf.ResizeRings()
		}
		if settings.EscrowRotation.Enabled {
			g.startEscrowRotatorLocked()
		} else {
			g.stopEscrowRotatorLocked()
		}
		g.mu.Unlock()

		writeJSON(w, settings)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleDebugRotation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()

	var snapshot ChainPhaseSnapshot
	if g.phaseGate != nil {
		snapshot = g.phaseGate.Snapshot()
	}
	blocksToEpochSwitch := int64(0)
	blocksUntilNextRotation := int64(0)
	if snapshot.BlockHeight > 0 && snapshot.epochSwitchBlockHeight > 0 {
		blocksToEpochSwitch = snapshot.epochSwitchBlockHeight - snapshot.BlockHeight
		blocksUntilNextRotation = blocksToEpochSwitch - settings.EscrowRotation.PrePoCBlocks
		if blocksUntilNextRotation < 0 {
			blocksUntilNextRotation = 0
		}
	}

	statuses, err := g.store.LoadRotationStatuses(100)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"settings": map[string]any{
			"enabled":            settings.EscrowRotation.Enabled,
			"settlement_enabled": settings.EscrowRotation.SettlementEnabled,
			"pre_poc_blocks":     settings.EscrowRotation.PrePoCBlocks,
			"models":             settings.EscrowRotation.Models,
		},
		"chain": map[string]any{
			"block_height":               snapshot.BlockHeight,
			"epoch_index":                snapshot.EpochIndex,
			"phase":                      snapshot.EpochPhase,
			"confirmation_poc_phase":     snapshot.ConfirmationPoCPhase,
			"epoch_switch_block_height":  snapshot.epochSwitchBlockHeight,
			"blocks_to_epoch_switch":     blocksToEpochSwitch,
			"blocks_until_next_rotation": blocksUntilNextRotation,
		},
		"latest": statuses,
	})
}

func applyGatewayDisabledRequest(settings *GatewayDisabledSettings, req *adminGatewayDisabledRequest) {
	if req.Enabled != nil {
		settings.Enabled = *req.Enabled
	}
	if req.Message != nil {
		settings.Message = strings.TrimSpace(*req.Message)
	}
	if req.NewURL != nil {
		settings.NewURL = strings.TrimSpace(*req.NewURL)
	}
	*settings = settings.WithDefaults()
}

func applyParticipantThrottleRequest(settings *ParticipantThrottleSettings, req *adminParticipantThrottleRequest) {
	if req.RequestBurst != nil {
		settings.RequestBurst = *req.RequestBurst
	}
	if req.RecoveryPerMinute != nil {
		settings.RecoveryPerMinute = *req.RecoveryPerMinute
	}
	if req.HTTPQuarantineMS != nil {
		settings.HTTPQuarantineMS = *req.HTTPQuarantineMS
	}
	if req.TransportFailureQuarantineMS != nil {
		settings.TransportFailureQuarantineMS = *req.TransportFailureQuarantineMS
	}
	if req.EmptyStreamQuarantineMS != nil {
		settings.EmptyStreamQuarantineMS = *req.EmptyStreamQuarantineMS
	}
	if req.StalledWinnerQuarantineMS != nil {
		settings.StalledWinnerQuarantineMS = *req.StalledWinnerQuarantineMS
	}
	if req.EmptyStreamQuarantineThreshold != nil {
		settings.EmptyStreamQuarantineThreshold = *req.EmptyStreamQuarantineThreshold
	}
}

func applyRedundancyRequest(settings *RedundancySettings, req *adminRedundancyRequest) {
	if req.ReceiptTimeoutMS != nil {
		settings.ReceiptTimeoutMS = *req.ReceiptTimeoutMS
	}
	if req.FirstTokenTimeoutFloorMS != nil {
		settings.FirstTokenTimeoutFloorMS = *req.FirstTokenTimeoutFloorMS
	}
	if req.PerInputTokenFirstTokenLagMS != nil {
		settings.PerInputTokenFirstTokenLagMS = *req.PerInputTokenFirstTokenLagMS
	}
	if req.InterChunkStallTimeoutMS != nil {
		settings.InterChunkStallTimeoutMS = *req.InterChunkStallTimeoutMS
	}
	if req.StreamingAttemptHardTimeoutMS != nil {
		settings.StreamingAttemptHardTimeoutMS = *req.StreamingAttemptHardTimeoutMS
	}
	if req.NonStreamResponseFloorMS != nil {
		settings.NonStreamResponseFloorMS = *req.NonStreamResponseFloorMS
	}
	if req.NonStreamNoContentTimeoutMS != nil {
		settings.NonStreamNoContentTimeoutMS = *req.NonStreamNoContentTimeoutMS
	}
	if req.NonStreamMaxAttemptWaitMS != nil {
		settings.NonStreamMaxAttemptWaitMS = *req.NonStreamMaxAttemptWaitMS
	}
	if req.PerInputTokenResponseLagMS != nil {
		settings.PerInputTokenResponseLagMS = *req.PerInputTokenResponseLagMS
	}
	if req.SecondaryWaitAfterWinnerMS != nil {
		settings.SecondaryWaitAfterWinnerMS = *req.SecondaryWaitAfterWinnerMS
	}
	if req.ParallelAdvantageThreshold != nil {
		settings.ParallelAdvantageThreshold = *req.ParallelAdvantageThreshold
	}
	if req.UnresponsiveThreshold != nil {
		settings.UnresponsiveThreshold = *req.UnresponsiveThreshold
	}
	if req.SpeedPolicy != nil {
		settings.SpeedPolicy = normalizeRedundancySpeedPolicy(*req.SpeedPolicy)
	}
	if req.PairwiseBudgetPercentile != nil {
		settings.PairwiseBudgetPercentile = *req.PairwiseBudgetPercentile
	}
	if req.PairwiseMaxProactiveAttempts != nil {
		settings.PairwiseMaxProactiveAttempts = *req.PairwiseMaxProactiveAttempts
	}
	if req.PairwiseMinDirectComparisons != nil {
		settings.PairwiseMinDirectComparisons = *req.PairwiseMinDirectComparisons
	}
	if req.PairwiseWinnerHoldMS != nil {
		settings.PairwiseWinnerHoldMS = *req.PairwiseWinnerHoldMS
	}
	if req.PairwiseWinnerHoldMinSpeedup != nil {
		settings.PairwiseWinnerHoldMinSpeedup = *req.PairwiseWinnerHoldMinSpeedup
	}
	if req.PairwiseWinnerHoldMinSamples != nil {
		settings.PairwiseWinnerHoldMinSamples = *req.PairwiseWinnerHoldMinSamples
	}
}

func applyPerfRequest(settings *PerfSettings, req *adminPerfRequest) {
	if req.SampleSize != nil {
		settings.SampleSize = *req.SampleSize
	}
	if req.WindowMS != nil {
		settings.WindowMS = *req.WindowMS
	}
}

func applyEscrowRotationRequest(settings *EscrowRotationSettings, req *adminEscrowRotationRequest) {
	if req.Enabled != nil {
		settings.Enabled = *req.Enabled
	}
	if req.SettlementEnabled != nil {
		settings.SettlementEnabled = *req.SettlementEnabled
	}
	if req.PrePoCBlocks != nil {
		settings.PrePoCBlocks = *req.PrePoCBlocks
	}
	if req.Models != nil {
		settings.Models = append([]EscrowRotationModelSettings(nil), (*req.Models)...)
		for i := range settings.Models {
			settings.Models[i].ModelID = strings.TrimSpace(settings.Models[i].ModelID)
			settings.Models[i].PrivateKeyEnv = strings.TrimSpace(settings.Models[i].PrivateKeyEnv)
		}
	}
}

func validateGatewaySettings(settings GatewaySettings) error {
	switch {
	case settings.DefaultRequestMaxTokens == 0:
		return fmt.Errorf("default_request_max_tokens must be > 0")
	case settings.RequestMaxTokensCap == 0:
		return fmt.Errorf("request_max_tokens_cap must be > 0")
	case settings.DefaultRequestMaxTokens > settings.RequestMaxTokensCap:
		return fmt.Errorf("default_request_max_tokens must be <= request_max_tokens_cap")
	case settings.MaxConcurrentPer10000Weight < 0:
		return fmt.Errorf("max_concurrent_requests_per_10000_weight must be >= 0")
	case settings.PoCMaxConcurrentPer10000Weight < 0:
		return fmt.Errorf("poc_max_concurrent_requests_per_10000_weight must be >= 0")
	}
	p := settings.ParticipantThrottle
	switch {
	case p.RequestBurst <= 0:
		return fmt.Errorf("participant_throttle.request_burst must be > 0")
	case p.RecoveryPerMinute <= 0:
		return fmt.Errorf("participant_throttle.recovery_per_minute must be > 0")
	case p.HTTPQuarantineMS <= 0:
		return fmt.Errorf("participant_throttle.http_quarantine_ms must be > 0")
	case p.TransportFailureQuarantineMS <= 0:
		return fmt.Errorf("participant_throttle.transport_failure_quarantine_ms must be > 0")
	case p.EmptyStreamQuarantineMS <= 0:
		return fmt.Errorf("participant_throttle.empty_stream_quarantine_ms must be > 0")
	case p.StalledWinnerQuarantineMS <= 0:
		return fmt.Errorf("participant_throttle.stalled_winner_quarantine_ms must be > 0")
	case p.EmptyStreamQuarantineThreshold <= 0:
		return fmt.Errorf("participant_throttle.empty_stream_threshold must be > 0")
	}
	r := settings.Redundancy
	switch {
	case r.ReceiptTimeoutMS <= 0:
		return fmt.Errorf("redundancy.receipt_timeout_ms must be > 0")
	case r.FirstTokenTimeoutFloorMS <= 0:
		return fmt.Errorf("redundancy.first_token_timeout_floor_ms must be > 0")
	case r.PerInputTokenFirstTokenLagMS < 0:
		return fmt.Errorf("redundancy.per_input_token_first_token_lag_ms must be >= 0")
	case r.InterChunkStallTimeoutMS < 0:
		return fmt.Errorf("redundancy.inter_chunk_stall_timeout_ms must be >= 0")
	case r.StreamingAttemptHardTimeoutMS <= 0:
		return fmt.Errorf("redundancy.streaming_attempt_hard_timeout_ms must be > 0")
	case r.NonStreamResponseFloorMS <= 0:
		return fmt.Errorf("redundancy.non_stream_response_floor_ms must be > 0")
	case r.NonStreamNoContentTimeoutMS <= 0:
		return fmt.Errorf("redundancy.non_stream_no_content_timeout_ms must be > 0")
	case r.NonStreamMaxAttemptWaitMS <= 0:
		return fmt.Errorf("redundancy.non_stream_max_attempt_wait_ms must be > 0")
	case r.NonStreamMaxAttemptWaitMS < r.NonStreamNoContentTimeoutMS:
		return fmt.Errorf("redundancy.non_stream_max_attempt_wait_ms must be >= non_stream_no_content_timeout_ms")
	case r.PerInputTokenResponseLagMS < 0:
		return fmt.Errorf("redundancy.per_input_token_response_lag_ms must be >= 0")
	case r.SecondaryWaitAfterWinnerMS <= 0:
		return fmt.Errorf("redundancy.secondary_wait_after_winner_ms must be > 0")
	case r.ParallelAdvantageThreshold <= 0 || r.ParallelAdvantageThreshold >= 1:
		return fmt.Errorf("redundancy.parallel_advantage_threshold must be > 0 and < 1")
	case r.UnresponsiveThreshold <= 0 || r.UnresponsiveThreshold > 1:
		return fmt.Errorf("redundancy.unresponsive_threshold must be > 0 and <= 1")
	case normalizeRedundancySpeedPolicy(r.SpeedPolicy) != RedundancySpeedPolicyLegacy &&
		normalizeRedundancySpeedPolicy(r.SpeedPolicy) != RedundancySpeedPolicyHybrid &&
		normalizeRedundancySpeedPolicy(r.SpeedPolicy) != RedundancySpeedPolicyPairwise:
		return fmt.Errorf("redundancy.speed_policy must be one of legacy, hybrid, pairwise")
	case r.PairwiseBudgetPercentile <= 0 || r.PairwiseBudgetPercentile >= 1:
		return fmt.Errorf("redundancy.pairwise_budget_percentile must be > 0 and < 1")
	case r.PairwiseMaxProactiveAttempts <= 0:
		return fmt.Errorf("redundancy.pairwise_max_proactive_attempts must be > 0")
	case r.PairwiseMinDirectComparisons <= 0:
		return fmt.Errorf("redundancy.pairwise_min_direct_comparisons must be > 0")
	case r.PairwiseWinnerHoldMS < 0:
		return fmt.Errorf("redundancy.pairwise_winner_hold_ms must be >= 0")
	case r.PairwiseWinnerHoldMinSpeedup <= 0 || r.PairwiseWinnerHoldMinSpeedup >= 1:
		return fmt.Errorf("redundancy.pairwise_winner_hold_min_speedup must be > 0 and < 1")
	case r.PairwiseWinnerHoldMinSamples <= 0:
		return fmt.Errorf("redundancy.pairwise_winner_hold_min_samples must be > 0")
	}
	perf := settings.Perf
	switch {
	case perf.SampleSize <= 0:
		return fmt.Errorf("perf.sample_size must be > 0")
	case perf.WindowMS <= 0:
		return fmt.Errorf("perf.window_ms must be > 0")
	}
	seenLimitModels := make(map[string]struct{}, len(settings.ModelLimits))
	for _, limit := range settings.ModelLimits {
		modelID := strings.TrimSpace(limit.ModelID)
		switch {
		case modelID == "":
			return fmt.Errorf("model_limits.model_id is required")
		case limit.MaxConcurrentRequests < 0:
			return fmt.Errorf("model_limits.max_concurrent_requests must be >= 0")
		case limit.MaxInputTokensInFlight < 0:
			return fmt.Errorf("model_limits.max_input_tokens_in_flight must be >= 0")
		}
		switch gatewayModelAccessModeLabel(limit.AccessMode) {
		case string(gatewayAccessModeOpen), string(gatewayAccessModeAPIKey), string(gatewayAccessModeAdminOnly):
		default:
			return fmt.Errorf("model_limits.access_mode must be open, api_key, or admin_only for model_id %q", modelID)
		}
		effectiveTokenLimits := outputTokenLimits{
			DefaultMaxTokens: settings.DefaultRequestMaxTokens,
			MaxTokensCap:     settings.RequestMaxTokensCap,
		}
		if limit.DefaultRequestMaxTokens > 0 {
			effectiveTokenLimits.DefaultMaxTokens = limit.DefaultRequestMaxTokens
		}
		if limit.RequestMaxTokensCap > 0 {
			effectiveTokenLimits.MaxTokensCap = limit.RequestMaxTokensCap
		}
		if effectiveTokenLimits.DefaultMaxTokens > effectiveTokenLimits.MaxTokensCap {
			return fmt.Errorf("model_limits.default_request_max_tokens must be <= request_max_tokens_cap for model_id %q", modelID)
		}
		if _, ok := seenLimitModels[modelID]; ok {
			return fmt.Errorf("model_limits contains duplicate model_id %q", modelID)
		}
		seenLimitModels[modelID] = struct{}{}
	}
	rotation := settings.EscrowRotation
	if rotation.Enabled {
		if rotation.PrePoCBlocks <= 0 {
			return fmt.Errorf("escrow_rotation.pre_poc_blocks must be > 0")
		}
		if len(rotation.Models) == 0 {
			return fmt.Errorf("escrow_rotation.models must contain at least one model when rotation is enabled")
		}
		seenModels := make(map[string]struct{})
		for _, model := range rotation.Models {
			switch {
			case strings.TrimSpace(model.ModelID) == "":
				return fmt.Errorf("escrow_rotation.models.model_id is required when rotation is enabled")
			case model.TempCount <= 0:
				return fmt.Errorf("escrow_rotation.temp_count must be > 0")
			case model.TargetCount <= 0:
				return fmt.Errorf("escrow_rotation.target_count must be > 0")
			case model.Amount == 0:
				return fmt.Errorf("escrow_rotation.amount must be > 0 when rotation is enabled")
			case strings.TrimSpace(model.PrivateKeyEnv) == "":
				return fmt.Errorf("escrow_rotation.private_key_env is required when rotation is enabled")
			}
			if _, ok := seenModels[model.ModelID]; ok {
				return fmt.Errorf("escrow_rotation.models contains duplicate model_id %q", model.ModelID)
			}
			seenModels[model.ModelID] = struct{}{}
		}
	}
	return nil
}

func applyGatewayTuningSettings(settings GatewaySettings) {
	settings = settings.WithTuningDefaults()
	sharedParticipantRequestLimiter.UpdateSettings(settings.ParticipantThrottle)
	ApplyRedundancySettings(settings.Redundancy)
	ApplyPerfSettings(settings.Perf)
}

func (g *Gateway) handleAdminDevshards(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		g.handleAdminState(w, r)
	case http.MethodPost:
		g.handleAdminAddDevshard(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (g *Gateway) handleAdminEscrows(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	var req adminCreateEscrowRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if req.Amount == 0 {
		http.Error(w, `{"error":{"message":"amount is required"}}`, http.StatusBadRequest)
		return
	}
	modelID := strings.TrimSpace(req.ModelID)
	if modelID == "" {
		modelID = g.settings.DefaultModel
	}
	privateKeyEnv := strings.TrimSpace(req.PrivateKeyEnv)
	if strings.TrimSpace(req.PrivateKey) == "" && privateKeyEnv == "" && strings.TrimSpace(os.Getenv("DEVSHARD_PRIVATE_KEY")) != "" {
		privateKeyEnv = "DEVSHARD_PRIVATE_KEY"
	}
	signer, keyHex, err := signerFromRequestKey(req.PrivateKey, privateKeyEnv)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	txClient, err := newGatewayRESTChainTxClient(g.settings, req.ChainID, req.FeeDenom, req.FeeAmount, req.GasLimit)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	result, err := txClient.CreateDevshardEscrow(r.Context(), signer, req.Amount, modelID)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadGateway)
		return
	}

	register := true
	if req.Register != nil {
		register = *req.Register
	}
	response := map[string]any{
		"escrow_id":  result.EscrowID,
		"tx_hash":    result.TxHash,
		"creator":    result.Creator,
		"registered": register,
	}
	if !register {
		writeJSON(w, response)
		return
	}

	record := GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:              strconv.FormatUint(result.EscrowID, 10),
			Model:           modelID,
			StoragePath:     strings.TrimSpace(req.StoragePath),
			ProtocolVersion: strings.TrimSpace(req.ProtocolVersion),
		},
		Active: true,
	}
	if strings.TrimSpace(req.PrivateKey) != "" {
		record.PrivateKeyHex = keyHex
	} else {
		record.PrivateKeyEnv = privateKeyEnv
	}
	record, err = g.addCreatedEscrowRuntime(record)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q,"escrow_id":%q,"tx_hash":%q}}`, err.Error(), record.ID, result.TxHash), http.StatusInternalServerError)
		return
	}
	response["id"] = record.ID
	response["model"] = record.Model
	response["storage_path"] = record.StoragePath
	writeJSON(w, response)
}

func newGatewayRESTChainTxClient(settings GatewaySettings, chainID, feeDenom string, feeAmount, gasLimit uint64) (*RESTChainTxClient, error) {
	return NewRESTChainTxClient(RESTChainTxConfig{
		BaseURL:      settings.ChainREST,
		TxQueryURL:   firstNonEmpty(os.Getenv("DEVSHARD_TX_QUERY_REST"), "http://node1.gonka.ai:8000/chain-api"),
		ChainID:      firstNonEmpty(chainID, os.Getenv("DEVSHARD_CHAIN_ID")),
		FeeDenom:     firstNonEmpty(feeDenom, os.Getenv("DEVSHARD_TX_FEE_DENOM")),
		FeeAmount:    firstNonZeroUint64(feeAmount, uint64(readInt64Env("DEVSHARD_TX_FEE_AMOUNT", int64(defaultTxFeeAmount)))),
		GasLimit:     firstNonZeroUint64(gasLimit, settings.TxGasLimit, uint64(readInt64Env("DEVSHARD_TX_GAS_LIMIT", int64(defaultTxGasLimit)))),
		PollInterval: txSettingDurationMS(os.Getenv("DEVSHARD_TX_POLL_INTERVAL_MS"), defaultTxPollInterval),
		PollTimeout:  txSettingDurationMS(os.Getenv("DEVSHARD_TX_POLL_TIMEOUT_MS"), defaultTxPollTimeout),
	})
}

func firstNonZeroUint64(values ...uint64) uint64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func (g *Gateway) addCreatedEscrowRuntime(record GatewayDevshardState) (GatewayDevshardState, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		return record, err
	}
	if !ok {
		return record, fmt.Errorf("gateway state is not initialized")
	}
	if _, exists := g.runtimes[record.ID]; exists {
		return record, fmt.Errorf("devshard %s already exists", record.ID)
	}
	if record.Model == "" {
		record.Model = state.Settings.DefaultModel
	}
	if record.StoragePath == "" {
		record.StoragePath = defaultStoragePath(g.baseStorageDir, record.ID)
	} else {
		record.StoragePath = normalizeStorageDir(record.StoragePath)
	}
	rt, err := gatewayRuntimeBuilder(record.RuntimeConfig, state.Settings.ChainREST, state.Settings.DefaultModel, g.perf)
	if err != nil {
		return record, err
	}
	if err := g.store.UpsertDevshard(record); err != nil {
		rt.close()
		return record, err
	}
	g.runtimes[record.ID] = rt
	g.runtimeOrder = append(g.runtimeOrder, rt)
	g.attachRuntimeSharedState(rt)
	g.sortRuntimeOrderLocked()
	return record, nil
}

func (g *Gateway) handleAdminDevshardAction(w http.ResponseWriter, r *http.Request) {
	trimmed := strings.TrimPrefix(r.URL.Path, "/v1/admin/devshards/")
	parts := strings.Split(strings.Trim(trimmed, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && parts[0] == "import" && r.Method == http.MethodPost {
		g.handleAdminImportDevshard(w, r)
		return
	}
	id := parts[0]
	if len(parts) == 1 && r.Method == http.MethodDelete {
		g.handleAdminCleanDevshard(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "deactivate" && r.Method == http.MethodPost {
		g.handleAdminDeactivateDevshard(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "settle" && r.Method == http.MethodPost {
		g.handleAdminSettleDevshard(w, r, id)
		return
	}
	if len(parts) == 2 && parts[1] == "participants" && r.Method == http.MethodGet {
		g.handleAdminDevshardParticipants(w, r, id)
		return
	}
	http.NotFound(w, r)
}

type adminDevshardParticipantView struct {
	ParticipantKey string `json:"participant_key"`
	SlotCount      int    `json:"slot_count"`
	ParticipantThrottleSnapshot
}

func (g *Gateway) handleAdminDevshardParticipants(w http.ResponseWriter, r *http.Request, id string) {
	if g == nil {
		http.Error(w, `{"error":{"message":"gateway unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	rt, ok := g.runtimes[id]
	if !ok {
		g.mu.Unlock()
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s not found"}}`, id), http.StatusNotFound)
		return
	}
	model := rt.model
	active := rt.active.Load()
	activeRequests := rt.activeRequests.Load()
	participantKeys := runtimeParticipantKeys(rt)
	slotCounts := make(map[string]int, len(rt.participantSlotCounts))
	for key, count := range rt.participantSlotCounts {
		slotCounts[key] = count
	}
	g.mu.Unlock()

	var throttleSnapshots map[string]ParticipantThrottleSnapshot
	if g.participantLimiter != nil {
		throttleSnapshots = g.participantLimiter.Snapshot(participantKeys)
	} else {
		throttleSnapshots = (*ParticipantRequestLimiter)(nil).Snapshot(participantKeys)
	}

	participants := make([]adminDevshardParticipantView, 0, len(participantKeys))
	blockedCount := 0
	quarantinedCount := 0
	availableCount := 0
	for _, key := range participantKeys {
		slotCount := slotCounts[key]
		if slotCount == 0 {
			slotCount = 1
		}
		status := throttleSnapshots[key]
		if status.Blocked {
			blockedCount++
		}
		if status.Quarantined {
			quarantinedCount++
		}
		if status.AvailableForCapacity {
			availableCount++
		}
		participants = append(participants, adminDevshardParticipantView{
			ParticipantKey:              key,
			SlotCount:                   slotCount,
			ParticipantThrottleSnapshot: status,
		})
	}

	writeJSON(w, map[string]any{
		"id":                id,
		"model":             model,
		"active":            active,
		"active_requests":   activeRequests,
		"participant_count": len(participants),
		"available_count":   availableCount,
		"blocked_count":     blockedCount,
		"quarantined_count": quarantinedCount,
		"participants":      participants,
	})
}

func runtimeParticipantKeys(rt *devshardRuntime) []string {
	if rt == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(rt.participantKeys)+len(rt.participantSlotCounts))
	for _, key := range rt.participantKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	for key := range rt.participantSlotCounts {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func (g *Gateway) handleAdminSettleDevshard(w http.ResponseWriter, r *http.Request, id string) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	var req adminSettleEscrowRequest
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(string(body)) != "" {
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}
	}

	result, err := g.settleDevshardOnChain(r.Context(), id, req)
	if err != nil {
		switch {
		case errors.Is(err, errDevshardBusy):
			http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s has active requests"}}`, id), http.StatusConflict)
			return
		case strings.Contains(err.Error(), "is not active"):
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusNotFound)
			return
		case strings.Contains(err.Error(), "private key") || strings.Contains(err.Error(), "gateway state"):
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
			return
		}
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadGateway)
		return
	}
	writeJSON(w, map[string]any{
		"id":        id,
		"escrow_id": result.EscrowID,
		"active":    false,
		"tx_hash":   result.TxHash,
		"settler":   result.Settler,
	})
}

func (g *Gateway) handleAdminImportDevshard(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	var req adminImportDevshardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	req.StoragePath = normalizeStorageDir(req.StoragePath)
	if req.ID == "" {
		http.Error(w, `{"error":{"message":"id is required"}}`, http.StatusBadRequest)
		return
	}
	if req.StoragePath == "" {
		http.Error(w, `{"error":{"message":"storage_path is required for import"}}`, http.StatusBadRequest)
		return
	}
	hasKey := strings.TrimSpace(req.PrivateKey) != "" || strings.TrimSpace(req.PrivateKeyEnv) != ""
	if !hasKey {
		http.Error(w, `{"error":{"message":"private_key or private_key_env is required for import"}}`, http.StatusBadRequest)
		return
	}
	active := false
	if req.Active != nil {
		active = *req.Active
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"message":"gateway state is not initialized"}}`, http.StatusServiceUnavailable)
		return
	}
	if _, exists := g.runtimes[req.ID]; exists {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s already loaded"}}`, req.ID), http.StatusConflict)
		return
	}
	if _, found := findGatewayDevshard(state.Devshards, req.ID); found {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s already exists in gateway state"}}`, req.ID), http.StatusConflict)
		return
	}

	record := GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{
			ID:              req.ID,
			PrivateKeyHex:   strings.TrimSpace(req.PrivateKey),
			PrivateKeyEnv:   strings.TrimSpace(req.PrivateKeyEnv),
			Model:           strings.TrimSpace(req.Model),
			StoragePath:     req.StoragePath,
			ProtocolVersion: strings.TrimSpace(req.ProtocolVersion),
		},
		Active: active,
	}
	if record.Model == "" {
		record.Model = state.Settings.DefaultModel
	}
	rt, err := gatewayRuntimeBuilder(record.RuntimeConfig, state.Settings.ChainREST, state.Settings.DefaultModel, g.perf)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if err := g.store.UpsertDevshard(record); err != nil {
		rt.close()
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	rt.active.Store(active)
	rt.activeConfigured = true
	g.runtimes[record.ID] = rt
	g.runtimeOrder = append(g.runtimeOrder, rt)
	g.attachRuntimeSharedState(rt)
	g.sortRuntimeOrderLocked()

	accountingImported := int64(0)
	accountingAttemptsImported := int64(0)
	perfPath := strings.TrimSpace(req.PerfPath)
	if perfPath != "" && g.perfStore != nil {
		accountingImported, accountingAttemptsImported, err = g.perfStore.ImportRequestAccounting(perfPath, record.ID)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q,"id":%q,"active":%t}}`, err.Error(), record.ID, active), http.StatusInternalServerError)
			return
		}
	}
	writeJSON(w, map[string]any{
		"id":                           record.ID,
		"active":                       active,
		"model":                        record.Model,
		"storage_path":                 record.StoragePath,
		"perf_path":                    perfPath,
		"accounting_records_imported":  accountingImported,
		"accounting_attempts_imported": accountingAttemptsImported,
	})
}

func (g *Gateway) resolveDevshardSettlementKey(id string, req adminSettleEscrowRequest) (string, string, error) {
	if strings.TrimSpace(req.PrivateKey) != "" || strings.TrimSpace(req.PrivateKeyEnv) != "" {
		return req.PrivateKey, req.PrivateKeyEnv, nil
	}
	state, ok, err := g.store.LoadState()
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("gateway state is not initialized")
	}
	record, found := findGatewayDevshard(state.Devshards, id)
	if !found {
		return "", "", fmt.Errorf("devshard %s not found", id)
	}
	if strings.TrimSpace(record.PrivateKeyHex) != "" || strings.TrimSpace(record.PrivateKeyEnv) != "" {
		return record.PrivateKeyHex, record.PrivateKeyEnv, nil
	}
	return "", "", fmt.Errorf("private_key or private_key_env is required")
}

func (g *Gateway) handleAdminAddDevshard(w http.ResponseWriter, r *http.Request) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	var req adminDevshardRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	req.ID = strings.TrimSpace(req.ID)
	if req.ID == "" {
		http.Error(w, `{"error":{"message":"id is required"}}`, http.StatusBadRequest)
		return
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"message":"gateway state is not initialized"}}`, http.StatusServiceUnavailable)
		return
	}

	record, found := findGatewayDevshard(state.Devshards, req.ID)
	if found {
		if strings.TrimSpace(req.PrivateKey) != "" {
			record.PrivateKeyHex = strings.TrimSpace(req.PrivateKey)
		}
		if strings.TrimSpace(req.PrivateKeyEnv) != "" {
			record.PrivateKeyEnv = strings.TrimSpace(req.PrivateKeyEnv)
		}
		if strings.TrimSpace(req.Model) != "" {
			record.Model = strings.TrimSpace(req.Model)
		}
		if strings.TrimSpace(req.StoragePath) != "" {
			record.StoragePath = normalizeStorageDir(req.StoragePath)
		}
		if strings.TrimSpace(req.ProtocolVersion) != "" {
			record.ProtocolVersion = strings.TrimSpace(req.ProtocolVersion)
		}
		record.Active = true
	} else {
		hasKey := strings.TrimSpace(req.PrivateKey) != "" || strings.TrimSpace(req.PrivateKeyEnv) != ""
		if !hasKey {
			http.Error(w, `{"error":{"message":"private_key or private_key_env is required for a new devshard"}}`, http.StatusBadRequest)
			return
		}
		record = GatewayDevshardState{
			RuntimeConfig: RuntimeConfig{
				ID:              req.ID,
				PrivateKeyHex:   strings.TrimSpace(req.PrivateKey),
				PrivateKeyEnv:   strings.TrimSpace(req.PrivateKeyEnv),
				Model:           strings.TrimSpace(req.Model),
				StoragePath:     normalizeStorageDir(req.StoragePath),
				ProtocolVersion: strings.TrimSpace(req.ProtocolVersion),
			},
			Active: true,
		}
	}

	if existing, exists := g.runtimes[req.ID]; exists {
		if existing.active.Load() {
			http.Error(w, `{"error":{"message":"devshard already active"}}`, http.StatusConflict)
			return
		}
		if err := g.store.UpsertDevshard(record); err != nil {
			http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
			return
		}
		existing.active.Store(true)
		writeJSON(w, map[string]any{
			"id":           record.ID,
			"active":       true,
			"model":        record.Model,
			"storage_path": record.StoragePath,
		})
		return
	}

	if record.Model == "" {
		record.Model = state.Settings.DefaultModel
	}
	if record.StoragePath == "" {
		record.StoragePath = defaultStoragePath(g.baseStorageDir, record.ID)
	} else {
		record.StoragePath = normalizeStorageDir(record.StoragePath)
	}

	rt, err := gatewayRuntimeBuilder(record.RuntimeConfig, state.Settings.ChainREST, state.Settings.DefaultModel, g.perf)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if err := g.store.UpsertDevshard(record); err != nil {
		rt.close()
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	g.runtimes[record.ID] = rt
	g.runtimeOrder = append(g.runtimeOrder, rt)
	g.attachRuntimeSharedState(rt)
	g.sortRuntimeOrderLocked()
	writeJSON(w, map[string]any{
		"id":           record.ID,
		"active":       true,
		"model":        record.Model,
		"storage_path": record.StoragePath,
	})
}

func (g *Gateway) handleAdminDeactivateDevshard(w http.ResponseWriter, r *http.Request, id string) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.runtimes[id]
	if !ok {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s is not active"}}`, id), http.StatusNotFound)
		return
	}
	if err := g.store.SetDevshardActive(id, false); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	rt.active.Store(false)
	writeJSON(w, map[string]any{
		"id":     id,
		"active": false,
	})
}

func (g *Gateway) handleAdminCleanDevshard(w http.ResponseWriter, r *http.Request, id string) {
	if g.store == nil {
		http.Error(w, `{"error":{"message":"gateway state store unavailable"}}`, http.StatusServiceUnavailable)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	state, ok, err := g.store.LoadState()
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if !ok {
		http.Error(w, `{"error":{"message":"gateway state is not initialized"}}`, http.StatusServiceUnavailable)
		return
	}
	record, found := findGatewayDevshard(state.Devshards, id)
	if !found {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s not found"}}`, id), http.StatusNotFound)
		return
	}
	if record.Active {
		http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s is active; deactivate it first"}}`, id), http.StatusConflict)
		return
	}
	if rt, ok := g.runtimes[id]; ok {
		if rt.activeRequests.Load() > 0 {
			http.Error(w, fmt.Sprintf(`{"error":{"message":"devshard %s has active requests"}}`, id), http.StatusConflict)
			return
		}
		delete(g.runtimes, id)
		g.runtimeOrder = removeRuntime(g.runtimeOrder, id)
		if g.capacity != nil {
			g.capacity.RemoveEscrow(id)
		}
		if err := rt.close(); err != nil {
			log.Printf("close devshard %s: %v", id, err)
		}
	}
	if err := g.store.DeleteDevshard(id); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	if err := removeDevshardStorage(record.StoragePath, g.baseStorageDir); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{
		"id":      id,
		"deleted": true,
	})
}

func (g *Gateway) handleAdminUnquarantine(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		ParticipantKey string `json:"participant_key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":{"message":%q}}`, err.Error()), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.ParticipantKey) == "" {
		http.Error(w, `{"error":{"message":"participant_key is required"}}`, http.StatusBadRequest)
		return
	}
	if g.participantLimiter == nil {
		http.Error(w, `{"error":{"message":"participant limiter not configured"}}`, http.StatusServiceUnavailable)
		return
	}
	cleared := g.participantLimiter.ClearQuarantine(req.ParticipantKey)
	writeJSON(w, map[string]any{
		"participant_key": req.ParticipantKey,
		"cleared":         cleared,
	})
}

func findGatewayDevshard(devshards []GatewayDevshardState, id string) (GatewayDevshardState, bool) {
	for _, devshard := range devshards {
		if devshard.ID == id {
			return devshard, true
		}
	}
	return GatewayDevshardState{}, false
}

func removeRuntime(runtimes []*devshardRuntime, id string) []*devshardRuntime {
	out := runtimes[:0]
	for _, rt := range runtimes {
		if rt.id != id {
			out = append(out, rt)
		}
	}
	return out
}

func (g *Gateway) sortRuntimeOrderLocked() {
	slices.SortFunc(g.runtimeOrder, func(a, b *devshardRuntime) int {
		return strings.Compare(a.id, b.id)
	})
}

func (g *Gateway) attachMetrics(rt *devshardRuntime) {
	if g == nil || g.metrics == nil || rt == nil || rt.proxy == nil || rt.proxy.redundancy == nil {
		return
	}
	rt.proxy.redundancy.metrics = g.metrics
	rt.proxy.redundancy.devshardID = rt.id
}

func (g *Gateway) attachEscrowChecker(rt *devshardRuntime) {
	if g == nil || rt == nil || rt.proxy == nil || rt.proxy.redundancy == nil {
		return
	}
	escrowID := rt.id
	modelID := rt.model
	if g.escrowChecker != nil {
		rt.proxy.redundancy.onEscrowMissing = func() {
			go g.escrowChecker.TriggerCheck(escrowID, func() {
				g.deactivateDevshardByID(escrowID)
			})
		}
	}
	rt.proxy.redundancy.onBalanceExhausted = func() {
		if !g.escrowRotationEnabled() {
			g.deactivateDevshardByIDWithReason(escrowID, "escrow balance exhausted")
			return
		}
		log.Printf("gateway_replacing_exhausted_escrow escrow=%s", escrowID)
		g.scheduleDepletedEscrowReplacement(escrowID, modelID, "balance_exhausted")
	}
}

func (g *Gateway) escrowRotationEnabled() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.settings.EscrowRotation.Enabled
}

// deactivateDevshardByID marks a devshard inactive in memory and persists the change.
// Safe to call from any goroutine.
func (g *Gateway) deactivateDevshardByID(id string) bool {
	return g.deactivateDevshardByIDWithReason(id, "escrow confirmed missing on chain")
}

func (g *Gateway) deactivateDevshardByIDWithReason(id, reason string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	rt, ok := g.runtimes[id]
	if !ok || !rt.active.Load() {
		return false
	}
	rt.active.Store(false)
	if g.store != nil {
		if err := g.store.SetDevshardActive(id, false); err != nil {
			log.Printf("escrow checker: persist deactivation for %s: %v", id, err)
		}
	}
	log.Printf("devshard %s deactivated: %s", id, reason)
	return true
}

func (g *Gateway) deactivateAndSettleDevshardByID(id, reason string) {
	if !g.deactivateDevshardByIDWithReason(id, reason) {
		return
	}
	g.scheduleAutoSettlement(id, reason)
}

func (g *Gateway) retireRotatedDevshard(ctx context.Context, id, reason string, settings GatewaySettings) (bool, error) {
	if !settings.EscrowRotation.SettlementEnabled {
		if g.deactivateDevshardByIDWithReason(id, reason) {
			log.Printf("escrow_rotation_deactivated_without_settlement escrow=%s reason=%q", id, reason)
		}
		return false, nil
	}
	log.Printf("escrow_rotation_settling escrow=%s reason=%q", id, reason)
	if _, err := gatewaySettleDevshardOnChain(g, ctx, id, adminSettleEscrowRequest{}); err != nil {
		return false, err
	}
	return true, nil
}

func (g *Gateway) scheduleDepletedEscrowReplacement(id, modelID, reason string) {
	if g == nil {
		return
	}
	if !g.escrowRotationEnabled() {
		return
	}
	g.replenishmentMu.Lock()
	if g.replenishmentInFlight == nil {
		g.replenishmentInFlight = make(map[string]struct{})
	}
	if _, exists := g.replenishmentInFlight[id]; exists {
		g.replenishmentMu.Unlock()
		return
	}
	g.replenishmentInFlight[id] = struct{}{}
	g.replenishmentMu.Unlock()

	go func() {
		defer func() {
			g.replenishmentMu.Lock()
			delete(g.replenishmentInFlight, id)
			g.replenishmentMu.Unlock()
		}()

		ctx, cancel := context.WithTimeout(context.Background(), autoSettlementAttemptTimeout)
		defer cancel()
		if err := g.replaceDepletedEscrow(ctx, id, modelID, reason); err != nil {
			log.Printf("escrow_depletion_replacement_failed escrow=%s model=%q reason=%q error=%v", id, modelID, reason, err)
		}
	}()
}

func (g *Gateway) replaceDepletedEscrow(ctx context.Context, id, modelID, reason string) error {
	g.mu.Lock()
	settings := g.settings
	g.mu.Unlock()
	if !settings.EscrowRotation.Enabled {
		return nil
	}
	model, ok := replacementModelForDepletedEscrow(settings, modelID)
	if !ok {
		return fmt.Errorf("no escrow rotation model configured for %q", modelID)
	}

	var epoch uint64
	if g.phaseGate != nil {
		epoch = g.phaseGate.Snapshot().EpochIndex
	}
	create := (*Gateway).createRotationEscrow
	if gatewayCreateDepletionEscrow != nil {
		create = gatewayCreateDepletionEscrow
	}
	result, err := create(g, ctx, settings, model, rotationRoleRegular, epoch)
	if err != nil {
		return fmt.Errorf("create replacement escrow: %w", err)
	}
	log.Printf("escrow_depletion_replacement_created old_escrow=%s new_escrow=%d model=%q reason=%q tx_hash=%s",
		id, result.EscrowID, model.ModelID, reason, result.TxHash)
	if !settings.EscrowRotation.SettlementEnabled {
		g.deactivateDevshardByIDWithReason(id, reason)
	} else {
		g.deactivateAndSettleDevshardByID(id, reason)
	}
	return nil
}

func replacementModelForDepletedEscrow(settings GatewaySettings, modelID string) (EscrowRotationModelSettings, bool) {
	if !settings.EscrowRotation.Enabled {
		return EscrowRotationModelSettings{}, false
	}
	modelID = strings.TrimSpace(modelID)
	for _, model := range normalizedEscrowRotationModels(settings) {
		if model.ModelID == modelID && model.Amount > 0 && strings.TrimSpace(model.PrivateKeyEnv) != "" {
			return model, true
		}
	}
	return EscrowRotationModelSettings{}, false
}

func (g *Gateway) scheduleAutoSettlement(id, reason string) {
	if g.store == nil {
		log.Printf("auto_settle_skipped escrow=%s reason=%s error=missing_gateway_store", id, reason)
		return
	}

	g.settlementMu.Lock()
	if g.settlementInFlight == nil {
		g.settlementInFlight = make(map[string]struct{})
	}
	if _, exists := g.settlementInFlight[id]; exists {
		g.settlementMu.Unlock()
		return
	}
	g.settlementInFlight[id] = struct{}{}
	g.settlementMu.Unlock()

	go func() {
		defer func() {
			g.settlementMu.Lock()
			delete(g.settlementInFlight, id)
			g.settlementMu.Unlock()
		}()

		for attempt := 1; attempt <= autoSettlementMaxAttempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), autoSettlementAttemptTimeout)
			result, err := gatewaySettleDevshardOnChain(g, ctx, id, adminSettleEscrowRequest{})
			cancel()
			if err == nil {
				log.Printf("auto_settle_submitted escrow=%s reason=%s tx_hash=%s settler=%s",
					id, reason, result.TxHash, result.Settler)
				return
			}
			log.Printf("auto_settle_failed escrow=%s reason=%s attempt=%d/%d error=%v",
				id, reason, attempt, autoSettlementMaxAttempts, err)
			if attempt == autoSettlementMaxAttempts {
				return
			}
			time.Sleep(autoSettlementRetryInterval)
		}
	}()
}

func removeDevshardStorage(storagePath, baseStorageDir string) error {
	if strings.TrimSpace(storagePath) == "" {
		return nil
	}
	storagePath = normalizeStorageDir(storagePath)
	baseStorageDir = filepath.Clean(baseStorageDir)
	if !strings.HasPrefix(storagePath, baseStorageDir+string(os.PathSeparator)) && storagePath != baseStorageDir {
		return fmt.Errorf("refusing to delete storage outside base dir: %s", storagePath)
	}
	if storagePath == baseStorageDir {
		return fmt.Errorf("refusing to delete base storage dir: %s", storagePath)
	}
	return os.RemoveAll(storagePath)
}

func finalizeRuntimeConfigs(runtimes []RuntimeConfig, defaultModel, baseStorageDir string) ([]RuntimeConfig, error) {
	out := make([]RuntimeConfig, 0, len(runtimes))
	seen := make(map[string]struct{}, len(runtimes))
	for _, cfg := range runtimes {
		cfg.ID = strings.TrimSpace(cfg.ID)
		if cfg.ID == "" {
			return nil, fmt.Errorf("runtime config missing id")
		}
		if _, ok := seen[cfg.ID]; ok {
			return nil, fmt.Errorf("duplicate runtime id %s", cfg.ID)
		}
		seen[cfg.ID] = struct{}{}
		if cfg.Model == "" {
			cfg.Model = defaultModel
		}
		if cfg.StoragePath == "" {
			cfg.StoragePath = defaultStoragePath(baseStorageDir, cfg.ID)
		} else {
			cfg.StoragePath = normalizeStorageDir(cfg.StoragePath)
		}
		out = append(out, cfg)
	}
	slices.SortFunc(out, func(a, b RuntimeConfig) int {
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

func buildRuntimes(configs []RuntimeConfig, chainREST, defaultModel string) ([]*devshardRuntime, error) {
	type result struct {
		idx int
		rt  *devshardRuntime
		err error
	}
	t0 := time.Now()
	perf := NewPerfTracker(nil)
	ch := make(chan result, len(configs))
	for i, cfg := range configs {
		go func(idx int, cfg RuntimeConfig) {
			rt, err := buildRuntime(cfg, chainREST, defaultModel, perf)
			ch <- result{idx, rt, err}
		}(i, cfg)
	}

	runtimes := make([]*devshardRuntime, len(configs))
	var firstErr error
	for range configs {
		res := <-ch
		if res.err != nil && firstErr == nil {
			firstErr = res.err
		}
		if res.rt != nil {
			runtimes[res.idx] = res.rt
			log.Printf("loaded devshard runtime escrow=%s model=%s storage=%s",
				configs[res.idx].ID, res.rt.model, configs[res.idx].StoragePath)
		}
	}
	if firstErr != nil {
		for _, rt := range runtimes {
			if rt != nil {
				rt.close()
			}
		}
		return nil, firstErr
	}
	log.Printf("build_runtimes_parallel count=%d total_elapsed_ms=%d", len(configs), time.Since(t0).Milliseconds())
	return runtimes, nil
}
