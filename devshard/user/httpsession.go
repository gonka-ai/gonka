package user

import (
	"errors"
	"fmt"

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
	StoragePath      string                          // optional: directory for SQLite session persistence
	StreamCallback   func(nonce uint64, line string) // optional: receives raw SSE data lines during inference
	RoutePrefix      string                          // optional: HTTP path prefix used to reach hosts; default devshard.LegacyRoutePrefix. Versioned binaries use devshard.VersionedRoutePrefix(...).
	RequestAdmission transport.RequestAdmissionController
	ProtocolVersion  types.ProtocolVersion // optional: defaults to ProtocolV1
}

// NewHTTPSession creates a user Session wired with HTTP clients to real dapi hosts.
// It queries the bridge for escrow and group info, then creates transport clients
// for each slot.
func NewHTTPSession(cfg HTTPSessionConfig) (*Session, *state.StateMachine, error) {
	signer, err := signing.SignerFromHex(cfg.PrivateKeyHex)
	if err != nil {
		return nil, nil, fmt.Errorf("create signer: %w", err)
	}
	pv := cfg.ProtocolVersion
	if pv == "" {
		pv = types.ProtocolV1
	}
	routePrefix := devshardpkg.ResolveHostRoutePrefix(pv, cfg.RoutePrefix)
	verifier := signing.NewSecp256k1Verifier()
	version := devshardpkg.ProtocolSessionVersion(pv)

	group, err := bridge.BuildGroup(cfg.EscrowID, cfg.Bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := cfg.Bridge.GetEscrow(cfg.EscrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get escrow: %w", err)
	}

	config := types.SessionConfigWithPrice(len(group), escrow.TokenPrice)

	sm, err := state.NewStateMachine(cfg.EscrowID, config, group, escrow.Amount, escrow.CreatorAddress, verifier,
		state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
		state.WithVersion(version),
		state.WithProtocolVersion(pv),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("create state machine: %w", err)
	}

	// Canonical participant key is the slot's validator address (gonka
	// bech32 string). We deliberately do NOT key on inference URL host
	// even though the transport dials the URL: chain-side state
	// (weights, PoC preservation, escrow membership) is keyed by
	// validator address, and so is the throttle limiter -- mixing
	// schemes would cause silent map misses (see CapacityState +
	// ParticipantRequestLimiter wiring in cmd/devshardctl).
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

	var opts []SessionOption
	if cfg.StoragePath != "" {
		sqlStore, storeErr := storage.NewSQLite(cfg.StoragePath)
		if storeErr != nil {
			return nil, nil, fmt.Errorf("open storage: %w", storeErr)
		}
		opts = append(opts, WithStorage(sqlStore))

		// Check if there is an existing session to recover from.
		_, metaErr := sqlStore.GetSessionMeta(cfg.EscrowID)
		if metaErr == nil {
			session, recSM, recErr := RecoverSession(sqlStore, signer, verifier, cfg.EscrowID, version, group, clients,
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

		// First run: create the session row so AppendDiff works later.
		if createErr := sqlStore.CreateSession(storage.CreateSessionParams{
			EscrowID:       cfg.EscrowID,
			EpochID:        escrow.EpochID,
			Version:        version,
			CreatorAddr:    escrow.CreatorAddress,
			Config:         config,
			Group:          group,
			InitialBalance: escrow.Amount,
		}); createErr != nil {
			sqlStore.Close()
			return nil, nil, fmt.Errorf("create storage session: %w", createErr)
		}
	}

	session, err := NewSession(sm, signer, cfg.EscrowID, group, clients, verifier, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("create session: %w", err)
	}
	session.SetParticipantKeys(participantKeys)

	return session, sm, nil
}
