package user

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/heightsync"
	"devshard/logging"
	"devshard/signing"
	"devshard/state"
	"devshard/storage"
	"devshard/transport"
	"devshard/types"
)

// HTTPSessionConfig holds the parameters needed to create an HTTP-backed user session.
type HTTPSessionConfig struct {
	PrivateKeyHex    string
	EscrowID         string
	Bridge           bridge.MainnetBridge
	StoragePath      string                          // SQLite path for session persistence; default ~/.cache/gonka/devshard-<escrowID>
	StreamCallback   func(nonce uint64, line string) // optional: receives raw SSE data lines during inference
	RoutePrefix      string                          // HTTP path prefix used to reach hosts; default devshard.DefaultRoutePrefix()
	RequestAdmission transport.RequestAdmissionController
	// RequireHeightSeed fails closed on chat/warmup until half the roster
	// returns a host-signed Anchor. Default false in this library; the
	// gateway sets it from DEVSHARD_REQUIRE_HEIGHT_SEED (default true).
	RequireHeightSeed bool
	// DisableHeightSyncHeartbeat skips the quiet-session cadence. Default
	// false in this library so tests can call MaybeHeartbeat. The gateway
	// sets it unless DEVSHARD_HEIGHTSYNC_HEARTBEAT is true/1/on.
	DisableHeightSyncHeartbeat bool
	// QuietSettleDelay overrides DefaultQuietSettleDelay for leftover
	// receipt/Finish compose. Zero keeps the session default (5m). The
	// gateway sets this from DEVSHARD_QUIET_SETTLE_DELAY (testenv: 5s).
	QuietSettleDelay time.Duration
	// ExtraClientConfig: only its HeightSync fields reach each host client.
	ExtraClientConfig *transport.ClientConfig
	// Heartbeat overlays compiled height-sync scheduling knobs. Nil keeps defaults.
	Heartbeat *heightsync.HeartbeatConfig

	HeaderTimes state.HeaderTimeSource
	// Escrow is an optional pre-fetched chain escrow. When set, NewHTTPSession
	// skips Bridge.GetEscrow and builds the group from this value.
	Escrow *bridge.EscrowInfo
	// Optional bind-time timeout overrides. These are mainly for integration
	// harnesses that need protocol timeouts shorter than production defaults.
	RefusalTimeoutSeconds   *int64
	ExecutionTimeoutSeconds *int64
	// AllowHostStateRecovery enables reconstructing session state from
	// Creator-signed diffs on the escrow's hosts when local SQLite recovery is
	// unrecoverable or missing. Used exclusively on settlement paths so an
	// unservable escrow can still be settled with real host stats.
	AllowHostStateRecovery bool
}

// hostClientConfig is the transport config one host client runs with.
func hostClientConfig(
	cfg HTTPSessionConfig,
	routePrefix, validatorAddress string,
	sharedPeerTips *transport.HeightSyncPeerTips,
) transport.ClientConfig {
	clientConfig := transport.DefaultClientConfig()
	if cfg.StreamCallback != nil {
		clientConfig.StreamCallback = cfg.StreamCallback
	}
	clientConfig.RoutePrefix = routePrefix
	if cfg.RequestAdmission != nil {
		clientConfig.ParticipantKey = validatorAddress
		clientConfig.Admission = cfg.RequestAdmission
	}
	if cfg.ExtraClientConfig != nil {
		clientConfig.AllowRetiredHTTPSession = cfg.ExtraClientConfig.AllowRetiredHTTPSession
		if cfg.ExtraClientConfig.HeightSync != nil {
			clientConfig.HeightSync = cfg.ExtraClientConfig.HeightSync
			clientConfig.HeightSyncPeerTips = sharedPeerTips
		}
		if cfg.ExtraClientConfig.HeightSyncLogOracle != nil {
			clientConfig.HeightSyncLogOracle = cfg.ExtraClientConfig.HeightSyncLogOracle
		}
		if cfg.ExtraClientConfig.HeightSyncRequestMutateHook != nil {
			clientConfig.HeightSyncRequestMutateHook = cfg.ExtraClientConfig.HeightSyncRequestMutateHook
		}
	}
	return clientConfig
}

func asHostClient(v any) HostClient {
	if v == nil {
		return nil
	}
	if hc, ok := v.(HostClient); ok {
		return hc
	}
	panic(fmt.Sprintf("SelectTransport returned %T, not HostClient", v))
}

func deferredWarmKeyResolver(resolve state.WarmKeyResolver) (state.WarmKeyResolver, func()) {
	var recoveryComplete atomic.Bool
	resolver := func(warmAddr, coldAddr string) (bool, error) {
		if !recoveryComplete.Load() {
			return false, nil
		}
		return resolve(warmAddr, coldAddr)
	}
	return resolver, func() { recoveryComplete.Store(true) }
}

func resolveHTTPSessionStoragePath(escrowID, configured string) string {
	if configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".cache", "gonka", fmt.Sprintf("devshard-%s", escrowID))
}

// LocalSessionConfig holds the parameters needed to rehydrate a user Session
// entirely from local storage, with no chain access and no host clients.
type LocalSessionConfig struct {
	PrivateKeyHex string
	EscrowID      string
	StoragePath   string
	HeaderTimes   state.HeaderTimeSource
}

// NewLocalSession rehydrates a Session from local SQLite storage without
// contacting the chain and without wiring any host clients. The returned
// session can answer read-only queries (state, status, debug, settlement
// build) but cannot dispatch new inferences. Callers own the returned
// Session and must Close it when done, which also closes the underlying
// storage handle.
//
// Warm-key verification is intentionally omitted (nil resolver): stored
// diffs carry their warm-key deltas, which RecoverSession injects before
// replay, so no chain-backed resolver is needed to rebuild state.
func NewLocalSession(cfg LocalSessionConfig) (*Session, *state.StateMachine, error) {
	if strings.TrimSpace(cfg.StoragePath) == "" {
		return nil, nil, fmt.Errorf("local session requires a storage path")
	}
	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()

	store, err := storage.NewSQLite(cfg.StoragePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage: %w", err)
	}
	meta, err := store.GetSessionMeta(cfg.EscrowID)
	if err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("get session meta: %w", err)
	}
	// No host clients: read-only sessions never dispatch inferences. The
	// slice length must match the group so NewSession's invariant holds.
	clients := make([]HostClient, len(meta.Group))
	var smOpts []state.SMOption
	if cfg.HeaderTimes != nil {
		smOpts = append(smOpts, state.WithHeaderTimes(cfg.HeaderTimes))
	}
	session, sm, err := RecoverSession(store, signer, verifier, cfg.EscrowID, "", meta.Group, clients, smOpts...)
	if err != nil {
		store.Close()
		return nil, nil, fmt.Errorf("recover session: %w", err)
	}
	return session, sm, nil
}

// NewHTTPSession creates a user Session wired with HTTP clients to real dapi hosts.
// It queries the bridge for escrow and group info, then creates transport clients
// for each slot.
func NewHTTPSession(cfg HTTPSessionConfig) (*Session, *state.StateMachine, error) {
	cfg.RoutePrefix = strings.TrimSpace(cfg.RoutePrefix)
	if cfg.RoutePrefix == "" {
		return nil, nil, fmt.Errorf("RoutePrefix is required; use /devshard/{version}")
	}
	if err := devshardpkg.ValidateEscrowID(cfg.EscrowID); err != nil {
		return nil, nil, err
	}

	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()

	routePrefix := devshardpkg.NormalizeRoutePrefix(cfg.RoutePrefix)
	sessionVersion, err := devshardpkg.VersionForRoutePrefix(routePrefix)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve route version: %w", err)
	}

	escrow := cfg.Escrow
	if escrow == nil {
		fetched, fetchErr := cfg.Bridge.GetEscrow(cfg.EscrowID)
		if fetchErr != nil {
			return nil, nil, fmt.Errorf("get escrow: %w", fetchErr)
		}
		escrow = fetched
	}
	group, err := bridge.BuildGroupFromEscrow(escrow)
	if err != nil {
		return nil, nil, fmt.Errorf("build group: %w", err)
	}

	config := bridge.SessionConfigAtBind(len(group), escrow)
	if cfg.RefusalTimeoutSeconds != nil {
		config.RefusalTimeout = *cfg.RefusalTimeoutSeconds
	}
	if cfg.ExecutionTimeoutSeconds != nil {
		config.ExecutionTimeout = *cfg.ExecutionTimeoutSeconds
	}
	config = types.NormalizeSessionConfig(config, len(group))

	storagePath := resolveHTTPSessionStoragePath(cfg.EscrowID, cfg.StoragePath)
	if err := os.MkdirAll(filepath.Dir(storagePath), 0755); err != nil && !cfg.AllowHostStateRecovery {
		return nil, nil, fmt.Errorf("create storage dir: %w", err)
	}
	sqlStore, sqlOpenErr := storage.NewSQLite(storagePath)
	if sqlOpenErr != nil && !cfg.AllowHostStateRecovery {
		return nil, nil, fmt.Errorf("open storage: %w", sqlOpenErr)
	}

	clients := make([]HostClient, len(group))
	participantKeys := make([]string, len(group))
	var sharedPeerTips *transport.HeightSyncPeerTips
	if cfg.ExtraClientConfig != nil && cfg.ExtraClientConfig.HeightSync != nil {
		if cfg.ExtraClientConfig.HeightSyncPeerTips != nil {
			sharedPeerTips = cfg.ExtraClientConfig.HeightSyncPeerTips
		} else {
			sharedPeerTips = transport.NewHeightSyncPeerTips()
		}
	}
	endpoints := transport.RPCEndpointsFromEnv()
	if cfg.ExtraClientConfig != nil && !cfg.ExtraClientConfig.RPCEndpoints.Empty() {
		if cfg.ExtraClientConfig.AllowRetiredHTTPSession {
			endpoints = cfg.ExtraClientConfig.RPCEndpoints
		} else {
			endpoints = transport.ResolveRPCEndpoints(cfg.ExtraClientConfig.RPCEndpoints)
		}
	}
	build := func(addr string) (HostClient, error) {
		info, err := cfg.Bridge.GetHostInfo(addr)
		if err != nil {
			err = fmt.Errorf("get host info for %s: %w", addr, err)
		} else if info == nil || strings.TrimSpace(info.URL) == "" {
			err = fmt.Errorf("host %s: %w", addr, transport.ErrPeerURLMissing)
		}
		if err != nil {
			logging.Warn("host client unresolved; retried on next use", "subsystem", "session",
				"escrow", cfg.EscrowID, "participant", addr, "error", err)
			return nil, err
		}
		httpClient := transport.NewHTTPClient(strings.TrimSpace(info.URL), cfg.EscrowID, signer,
			hostClientConfig(cfg, routePrefix, addr, sharedPeerTips))
		return asHostClient(transport.SelectTransport(httpClient, addr, endpoints, cfg.ExtraClientConfig)), nil
	}
	clientCache, err := openHostClients(cfg.EscrowID, group, build)
	if err != nil {
		if sqlStore != nil {
			sqlStore.Close()
		}
		return nil, nil, err
	}
	for i, slot := range group {
		participantKeys[i] = slot.ValidatorAddress
		clients[i] = clientCache[slot.ValidatorAddress]
	}

	wireRecoveredSession := func(session *Session) {
		session.SetParticipantKeys(participantKeys)
		session.SetRequireHeightSeed(cfg.RequireHeightSeed)
		session.SetDisableHeightSyncHeartbeat(cfg.DisableHeightSyncHeartbeat)
		if cfg.QuietSettleDelay > 0 {
			session.SetQuietSettleDelay(cfg.QuietSettleDelay)
		}
		if cfg.ExtraClientConfig != nil && cfg.ExtraClientConfig.HeightSync != nil {
			hs := cfg.ExtraClientConfig.HeightSync
			session.SetHeightSyncCadence(hs.K(), hs.SlotsNum())
		}
		session.SetHeightSyncPeerTips(sharedPeerTips)
	}

	recoverFromHosts := func(localCause error) (*Session, *state.StateMachine, error) {
		if sqlStore != nil {
			sqlStore.Close()
			sqlStore = nil
		}
		session, recSM, hostErr := RecoverSessionFromHosts(
			context.Background(),
			signer,
			verifier,
			cfg.EscrowID,
			sessionVersion,
			escrow.EpochID,
			escrow.CreatorAddress,
			escrow.Amount,
			config,
			group,
			clients,
			httpSessionSMOpts(cfg, state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey))...,
		)
		if hostErr != nil {
			closeHostClients(hostClientsFromCache(clientCache))
			return nil, nil, fmt.Errorf("recover session from hosts after %v: %w", localCause, hostErr)
		}
		wireRecoveredSession(session)
		return session, recSM, nil
	}

	if sqlStore == nil {
		return recoverFromHosts(sqlOpenErr)
	}

	// Check if there is an existing session to recover from.
	_, metaErr := sqlStore.GetSessionMeta(cfg.EscrowID)
	if metaErr == nil {
		warmKeyResolver, enableWarmKeyResolver := deferredWarmKeyResolver(cfg.Bridge.VerifyWarmKey)
		session, recSM, recErr := RecoverSession(sqlStore, signer, verifier, cfg.EscrowID, sessionVersion, group, clients,
			httpSessionSMOpts(cfg, state.WithWarmKeyResolver(warmKeyResolver))...,
		)
		if recErr != nil {
			if cfg.AllowHostStateRecovery && errors.Is(recErr, ErrLocalStateUnrecoverable) {
				return recoverFromHosts(recErr)
			}
			closeHostClients(hostClientsFromCache(clientCache))
			sqlStore.Close()
			return nil, nil, fmt.Errorf("recover session: %w", recErr)
		}
		enableWarmKeyResolver()
		wireRecoveredSession(session)
		return session, recSM, nil
	}
	if cfg.AllowHostStateRecovery {
		return recoverFromHosts(metaErr)
	}
	if !errors.Is(metaErr, storage.ErrSessionNotFound) {
		closeHostClients(hostClientsFromCache(clientCache))
		sqlStore.Close()
		return nil, nil, fmt.Errorf("check existing session: %w", metaErr)
	}

	if createErr := sqlStore.CreateSession(storage.CreateSessionParams{
		EscrowID:       cfg.EscrowID,
		EpochID:        escrow.EpochID,
		Version:        sessionVersion,
		CreatorAddr:    escrow.CreatorAddress,
		Config:         config,
		Group:          group,
		InitialBalance: escrow.Amount,
	}); createErr != nil {
		closeHostClients(hostClientsFromCache(clientCache))
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create storage session: %w", createErr)
	}

	sm, err := state.NewStateMachine(cfg.EscrowID, config, group, escrow.Amount, escrow.CreatorAddress, verifier, sqlStore,
		httpSessionSMOpts(cfg, state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey), state.WithVersion(sessionVersion))...,
	)
	if err != nil {
		closeHostClients(hostClientsFromCache(clientCache))
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	session, err := NewSession(sm, signer, cfg.EscrowID, group, clients, verifier, httpSessionOpts(cfg, WithStorage(sqlStore))...)
	if err != nil {
		closeHostClients(hostClientsFromCache(clientCache))
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create session: %w", err)
	}
	session.SetParticipantKeys(participantKeys)
	if cfg.ExtraClientConfig != nil && cfg.ExtraClientConfig.HeightSync != nil {
		hs := cfg.ExtraClientConfig.HeightSync
		session.SetHeightSyncCadence(hs.K(), hs.SlotsNum())
	}
	session.SetHeightSyncPeerTips(sharedPeerTips)

	return session, sm, nil
}

// hostLookupOpenWait bounds how long opening a session waits for its hosts'
// URL lookups, which all run at once. A host still unresolved after it opens
// lazily and keeps its lookup running.
var hostLookupOpenWait = transport.DefaultLazyWait

// openHostClients builds one client per distinct participant in group. A
// participant whose lookup fails transiently, publishes no URL, or outlasts
// hostLookupOpenWait does not fail the session: it gets a lazyHostClient that
// resolves on use, which the gateway's chat picker skips until then. A
// participant absent from the chain (bridge.ErrParticipantNotFound) opens the
// same way, so the escrow serves on the rest of its group and takes the host
// back once the chain lists it again.
func openHostClients(escrowID string, group []types.SlotAssignment, build func(addr string) (HostClient, error)) (map[string]HostClient, error) {
	lazies := make(map[string]*transport.Lazy[HostClient], len(group))
	for _, slot := range group {
		addr := slot.ValidatorAddress
		if _, ok := lazies[addr]; ok {
			continue
		}
		lazies[addr] = transport.NewLazy(func() (HostClient, error) { return build(addr) }, releaseHostClient, 0, 0)
	}

	ctx, cancel := context.WithTimeout(context.Background(), hostLookupOpenWait)
	defer cancel()
	var wg sync.WaitGroup
	for _, l := range lazies {
		wg.Add(1)
		go func(l *transport.Lazy[HostClient]) {
			defer wg.Done()
			_, _ = l.Get(ctx)
		}(l)
	}
	wg.Wait()

	out := make(map[string]HostClient, len(lazies))
	var unresolved []string
	for addr, l := range lazies {
		if hc, ok := l.Peek(); ok {
			out[addr] = hc
			continue
		}
		out[addr] = newLazyHostClient(l)
		unresolved = append(unresolved, addr)
	}
	if len(unresolved) > 0 {
		sort.Strings(unresolved)
		logging.Warn("session opened with unresolved hosts", "subsystem", "session",
			"escrow", escrowID, "unresolved", strings.Join(unresolved, ","), "participants", len(lazies))
	}
	return out, nil
}

func hostClientsFromCache(cache map[string]HostClient) []HostClient {
	out := make([]HostClient, 0, len(cache))
	for _, c := range cache {
		out = append(out, c)
	}
	return out
}

func httpSessionSMOpts(cfg HTTPSessionConfig, extra ...state.SMOption) []state.SMOption {
	if cfg.Heartbeat != nil {
		extra = append(extra, state.WithHeartbeatConfig(*cfg.Heartbeat))
	}
	if cfg.HeaderTimes != nil {
		extra = append(extra, state.WithHeaderTimes(cfg.HeaderTimes))
	}
	return extra
}

func httpSessionOpts(cfg HTTPSessionConfig, extra ...SessionOption) []SessionOption {
	if cfg.Heartbeat != nil {
		extra = append(extra, WithHeartbeatConfig(*cfg.Heartbeat))
	}
	if cfg.RequireHeightSeed {
		extra = append(extra, WithRequireHeightSeed(true))
	}
	if cfg.DisableHeightSyncHeartbeat {
		extra = append(extra, WithDisableHeightSyncHeartbeat(true))
	}
	if cfg.QuietSettleDelay > 0 {
		extra = append(extra, WithQuietSettleDelay(cfg.QuietSettleDelay))
	}
	return extra
}
