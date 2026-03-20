package user

import (
	"errors"
	"fmt"

	"subnet/bridge"
	"subnet/signing"
	"subnet/state"
	"subnet/storage"
	"subnet/transport"
	"subnet/types"
)

// HTTPSessionConfig holds the parameters needed to create an HTTP-backed user session.
type HTTPSessionConfig struct {
	PrivateKeyHex  string
	EscrowID       string
	Bridge         bridge.MainnetBridge
	StoragePath    string              // optional: path to SQLite DB for session persistence
	StreamCallback func(nonce uint64, line string) // optional: receives raw SSE data lines during inference
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

	group, err := bridge.BuildGroup(cfg.EscrowID, cfg.Bridge)
	if err != nil {
		return nil, nil, fmt.Errorf("build group: %w", err)
	}

	escrow, err := cfg.Bridge.GetEscrow(cfg.EscrowID)
	if err != nil {
		return nil, nil, fmt.Errorf("get escrow: %w", err)
	}

	config := types.SessionConfigWithPrice(len(group), escrow.TokenPrice)

	sm := state.NewStateMachine(cfg.EscrowID, config, group, escrow.Amount, escrow.CreatorAddress, verifier,
		state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
	)

	clients := make([]HostClient, len(group))
	clientCache := make(map[string]*transport.HTTPClient)
	for i, slot := range group {
		if c, ok := clientCache[slot.ValidatorAddress]; ok {
			clients[i] = c
			continue
		}
		info, err := cfg.Bridge.GetHostInfo(slot.ValidatorAddress)
		if err != nil {
			return nil, nil, fmt.Errorf("get host info for %s: %w", slot.ValidatorAddress, err)
		}
		var clientCfgs []transport.ClientConfig
		if cfg.StreamCallback != nil {
			cc := transport.DefaultClientConfig()
			cc.StreamCallback = cfg.StreamCallback
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
			session, recSM, recErr := RecoverSession(sqlStore, signer, verifier, cfg.EscrowID, group, clients,
				state.WithWarmKeyResolver(cfg.Bridge.VerifyWarmKey),
			)
			if recErr != nil {
				sqlStore.Close()
				return nil, nil, fmt.Errorf("recover session: %w", recErr)
			}
			return session, recSM, nil
		}
		if !errors.Is(metaErr, storage.ErrSessionNotFound) {
			sqlStore.Close()
			return nil, nil, fmt.Errorf("check existing session: %w", metaErr)
		}

		// First run: create the session row so AppendDiff works later.
		if createErr := sqlStore.CreateSession(storage.CreateSessionParams{
			EscrowID:       cfg.EscrowID,
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

	return session, sm, nil
}
