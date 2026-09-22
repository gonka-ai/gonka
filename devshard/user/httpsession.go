package user

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	devshardpkg "devshard"
	"devshard/bridge"
	"devshard/heightsync"
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
	// ExtraClientConfig is merged into each transport.HTTPClient when non-nil.
	// Used to attach courier-mode HeightSync (peer-tip cache; no local follower).
	ExtraClientConfig *transport.ClientConfig
	// Heartbeat overlays compiled height-sync scheduling knobs. Nil keeps defaults.
	Heartbeat *heightsync.HeartbeatConfig
	// Escrow is an optional pre-fetched chain escrow. When set, NewHTTPSession
	// skips Bridge.GetEscrow and builds the group from this value.
	Escrow *bridge.EscrowInfo
	// Optional bind-time timeout overrides. These are mainly for integration
	// harnesses that need protocol timeouts shorter than production defaults.
	RefusalTimeoutSeconds   *int64
	ExecutionTimeoutSeconds *int64
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
	session, sm, err := RecoverSession(store, signer, verifier, cfg.EscrowID, "", meta.Group, clients)
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
	if err := os.MkdirAll(filepath.Dir(storagePath), 0755); err != nil {
		return nil, nil, fmt.Errorf("create storage dir: %w", err)
	}
	sqlStore, err := storage.NewSQLite(storagePath)
	if err != nil {
		return nil, nil, fmt.Errorf("open storage: %w", err)
	}

	clients := make([]HostClient, len(group))
	participantKeys := make([]string, len(group))
	clientCache := make(map[string]*transport.HTTPClient)
	var sharedPeerTips *transport.HeightSyncPeerTips
	if cfg.ExtraClientConfig != nil && cfg.ExtraClientConfig.HeightSync != nil {
		if cfg.ExtraClientConfig.HeightSyncPeerTips != nil {
			sharedPeerTips = cfg.ExtraClientConfig.HeightSyncPeerTips
		} else {
			sharedPeerTips = transport.NewHeightSyncPeerTips()
		}
	}
	for i, slot := range group {
		participantKeys[i] = slot.ValidatorAddress
		if c, ok := clientCache[slot.ValidatorAddress]; ok {
			clients[i] = c
			continue
		}
		info, err := cfg.Bridge.GetHostInfo(slot.ValidatorAddress)
		if err != nil {
			sqlStore.Close()
			return nil, nil, fmt.Errorf("get host info for %s: %w", slot.ValidatorAddress, err)
		}
		cc := transport.DefaultClientConfig()
		if cfg.StreamCallback != nil {
			cc.StreamCallback = cfg.StreamCallback
		}
		cc.RoutePrefix = routePrefix
		if cfg.RequestAdmission != nil {
			cc.ParticipantKey = slot.ValidatorAddress
			cc.Admission = cfg.RequestAdmission
		}
		if cfg.ExtraClientConfig != nil {
			if cfg.ExtraClientConfig.HeightSync != nil {
				cc.HeightSync = cfg.ExtraClientConfig.HeightSync
				cc.HeightSyncPeerTips = sharedPeerTips
			}
			if cfg.ExtraClientConfig.HeightSyncLogOracle != nil {
				cc.HeightSyncLogOracle = cfg.ExtraClientConfig.HeightSyncLogOracle
			}
			if cfg.ExtraClientConfig.HeightSyncRequestMutateHook != nil {
				cc.HeightSyncRequestMutateHook = cfg.ExtraClientConfig.HeightSyncRequestMutateHook
			}
		}
		var clientCfgs []transport.ClientConfig
		if cfg.StreamCallback != nil || routePrefix != "" || cfg.RequestAdmission != nil || cfg.ExtraClientConfig != nil {
			clientCfgs = append(clientCfgs, cc)
		}
		c := transport.NewHTTPClient(info.URL, cfg.EscrowID, signer, clientCfgs...)
		clientCache[slot.ValidatorAddress] = c
		clients[i] = c
	}

	// Check if there is an existing session to recover from.
	_, metaErr := sqlStore.GetSessionMeta(cfg.EscrowID)
	if metaErr == nil {
		warmKeyResolver, enableWarmKeyResolver := deferredWarmKeyResolver(cfg.Bridge.VerifyWarmKey)
		session, recSM, recErr := RecoverSession(sqlStore, signer, verifier, cfg.EscrowID, sessionVersion, group, clients,
			httpSessionSMOpts(cfg, state.WithWarmKeyResolver(warmKeyResolver))...,
		)
		if recErr != nil {
			sqlStore.Close()
			return nil, nil, fmt.Errorf("recover session: %w", recErr)
		}
		enableWarmKeyResolver()
		session.SetParticipantKeys(participantKeys)
		session.SetRequireHeightSeed(cfg.RequireHeightSeed)
		if cfg.ExtraClientConfig != nil && cfg.ExtraClientConfig.HeightSync != nil {
			hs := cfg.ExtraClientConfig.HeightSync
			session.SetHeightSyncCadence(hs.K(), hs.SlotsNum())
		}
		session.SetHeightSyncPeerTips(sharedPeerTips)
		return session, recSM, nil
	}
	if !errors.Is(metaErr, storage.ErrSessionNotFound) {
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
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create storage session: %w", createErr)
	}

	sm, err := state.NewStateMachine(cfg.EscrowID, config, group, escrow.Amount, escrow.CreatorAddress, verifier, sqlStore,
		httpSessionSMOpts(cfg, state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey), state.WithVersion(sessionVersion))...,
	)
	if err != nil {
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	session, err := NewSession(sm, signer, cfg.EscrowID, group, clients, verifier, httpSessionOpts(cfg, WithStorage(sqlStore))...)
	if err != nil {
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

func httpSessionSMOpts(cfg HTTPSessionConfig, extra ...state.SMOption) []state.SMOption {
	if cfg.Heartbeat != nil {
		extra = append(extra, state.WithHeartbeatConfig(*cfg.Heartbeat))
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
	return extra
}
