package user

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	devshardpkg "devshard"
	"devshard/bridge"
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
	RoutePrefix      string                          // optional: HTTP path prefix used to reach hosts; default devshard.LegacyRoutePrefix. Versioned binaries use devshard.VersionedRoutePrefix(...).
	RequestAdmission transport.RequestAdmissionController
	ProtocolVersion  types.ProtocolVersion // optional: defaults to ProtocolV1
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

// NewHTTPSession creates a user Session wired with HTTP clients to real dapi hosts.
// It queries the bridge for escrow and group info, then creates transport clients
// for each slot.
func NewHTTPSession(cfg HTTPSessionConfig) (*Session, *state.StateMachine, error) {
	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	verifier := signing.NewSecp256k1Verifier()

	pv := cfg.ProtocolVersion
	if pv == "" {
		pv = types.ProtocolV1
	}
	routePrefix := devshardpkg.ResolveHostRoutePrefix(pv, cfg.RoutePrefix)
	sessionVersion := devshardpkg.ProtocolSessionVersion(pv)
	if cfg.ProtocolVersion == "" && cfg.RoutePrefix != "" {
		var versionErr error
		sessionVersion, versionErr = devshardpkg.VersionForRoutePrefix(cfg.RoutePrefix)
		if versionErr != nil {
			return nil, nil, fmt.Errorf("resolve route version: %w", versionErr)
		}
	}

	group, err := bridge.BuildGroup(cfg.EscrowID, cfg.Bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := cfg.Bridge.GetEscrow(cfg.EscrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get escrow: %w", err)
	}

	config, err := sessionConfigAtBind(len(group), escrow, cfg.Bridge)
	if err != nil {
		return nil, nil, err
	}

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
		var clientCfgs []transport.ClientConfig
		if cfg.StreamCallback != nil || routePrefix != "" || cfg.RequestAdmission != nil {
			cc := transport.DefaultClientConfig()
			cc.ProtocolVersion = pv
			if cfg.StreamCallback != nil {
				cc.StreamCallback = cfg.StreamCallback
			}
			if routePrefix != "" {
				cc.RoutePrefix = routePrefix
			}
			if cfg.RequestAdmission != nil {
				cc.ParticipantKey = slot.ValidatorAddress
				cc.Admission = cfg.RequestAdmission
			}
			clientCfgs = append(clientCfgs, cc)
		}
		c := transport.NewHTTPClient(info.URL, cfg.EscrowID, signer, clientCfgs...)
		clientCache[slot.ValidatorAddress] = c
		clients[i] = c
	}

	// Check if there is an existing session to recover from.
	_, metaErr := sqlStore.GetSessionMeta(cfg.EscrowID)
	if metaErr == nil {
		session, recSM, recErr := RecoverSession(sqlStore, signer, verifier, cfg.EscrowID, sessionVersion, group, clients,
			state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
			state.WithProtocolVersion(pv),
		)
		if recErr != nil {
			sqlStore.Close()
			return nil, nil, fmt.Errorf("recover session: %w", recErr)
		}
		session.SetParticipantKeys(participantKeys)
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
		state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
		state.WithVersion(types.EffectiveStateRootAndProtocolVersion),
		state.WithProtocolVersion(pv),
	)
	if err != nil {
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	session, err := NewSession(sm, signer, cfg.EscrowID, group, clients, verifier, WithStorage(sqlStore))
	if err != nil {
		sqlStore.Close()
		return nil, nil, fmt.Errorf("create session: %w", err)
	}
	session.SetParticipantKeys(participantKeys)

	return session, sm, nil
}
