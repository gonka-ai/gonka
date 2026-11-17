package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	commonchain "common/chain"
	"common/vocabulary"

	"devshard/bridge"
	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/heights"
	"devshard/cmd/gateway/internal/logkey"
	"devshard/cmd/gateway/registry"
	"devshard/cmd/gateway/store"
	"devshard/logging"
	"devshard/runtimeparams"
	"devshard/transport"
	"devshard/user"
)

// chainBackedSessions owns the chain connection because it is the only provider that needs one.
func chainBackedSessions(records devshardLookup, storageDir string) sessionSources {
	return func(endpoints config.Chain, heightSettings config.HeightSync, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) (chainSources, error) {
		// This client carries the CometBFT RPC query fallback. See ../README.md, "Escrow sessions and the chain connection".
		chainClient, err := commonchain.NewWithQueryFallback(endpoints.GRPCEndpoint, endpoints.RPCEndpoint)
		if err != nil {
			return chainSources{}, fmt.Errorf("dialing chain grpc %s: %w", endpoints.GRPCEndpoint, err)
		}
		grpcChain := chain.NewGRPCChain(chainClient, endpoints.ChainID)
		governanceSettings := runtimeparams.SettingsFromEnv()
		follower, err := heights.NewOracle(heightSettings, heights.OracleSources{
			NodeManagerAddr: governanceSettings.NodeManagerAddr,
			CometRPC:        endpoints.RPCEndpoint,
			Chain:           chainClient,
		})
		if err != nil {
			return chainSources{}, err
		}
		governance, err := openRuntimeParams(chainClient, governanceSettings)
		if err != nil {
			return chainSources{}, errors.Join(err, follower.Close())
		}
		escrowBridge := bridge.NewGRPCBridge(chainClient)
		return chainSources{
			Serving:    servingSessions(records, storageDir, escrowBridge, heightSettings, follower, governance, routePrefix, adoption, steps),
			ReadOnly:   readOnlySessions(records, storageDir),
			Reader:     grpcChain,
			Transport:  grpcChain,
			Heights:    follower,
			Governance: governance,

			ModelSnapshots: grpcChain,
			Hosts:          escrowBridge,
		}, nil
	}
}

func resolveStorageDir(explicit *string) (string, error) {
	if explicit != nil {
		return *explicit, nil
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir for storage: %w", err)
	}
	return filepath.Join(homeDir, ".cache", "gonka-gateway"), nil
}

// sessionSources is a parameter so the transport an escrow is served over is chosen once, at compose.
type sessionSources func(endpoints config.Chain, heightSettings config.HeightSync, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) (chainSources, error)

// chainSources is what one dial yields; Reader and Transport are interfaces so a test dials nothing.
type chainSources struct {
	Serving        registry.SessionFactory
	ReadOnly       registry.SessionFactory
	Reader         chain.Reader
	Transport      chain.Transport
	Heights        *heights.Oracle
	Governance     *runtimeParams
	PublicAPI      *http.Client
	ModelSnapshots vocabulary.EpochGroupDataQuery
	Hosts          HostDirectory
}

// sessionInputs resolves what both session factories need before they diverge on how they reach hosts.
func sessionInputs(ctx context.Context, records devshardLookup, storageDir, escrowID string) (store.DevshardRecord, string, string, error) {
	record, err := findDevshard(ctx, records, escrowID)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	keyHex, err := env.PrivateKey(record.PrivateKeyEnv)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	storagePath, err := escrowStorage(storageDir, escrowID)
	if err != nil {
		return store.DevshardRecord{}, "", "", err
	}
	return record, keyHex, storagePath, nil
}

// The bridge is one object for the process. See ../README.md, "Escrow sessions and the chain connection".
func servingSessions(records devshardLookup, storageDir string, escrowBridge bridge.MainnetBridge, heightSettings config.HeightSync, follower *heights.Oracle, governance *runtimeParams, routePrefix string, adoption *transport.PeerRPCAdoption, steps user.StepTimer) registry.SessionFactory {
	return func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
		record, keyHex, storagePath, err := sessionInputs(ctx, records, storageDir, escrowID)
		if err != nil {
			return nil, err
		}
		// A boot seed writes back the row settlement dropped, so the chain decides. See ../docs/operations.md, "Boot".
		onChain, err := escrowBridge.GetEscrow(escrowID)
		switch {
		case err != nil:
			return nil, fmt.Errorf("get escrow: %w", err)
		case onChain.Settled:
			return nil, fmt.Errorf("escrow %s: %w", escrowID, bridge.ErrEscrowSettled)
		}
		sessionTimeouts := env.LoadE2E().SessionTimeouts
		session, machine, err := user.NewHTTPSession(user.HTTPSessionConfig{
			OpenAbsentHostsAsDeadSlots: true,
			PrivateKeyHex:              keyHex,
			EscrowID:                   escrowID,
			Bridge:                     escrowBridge,
			Escrow:                     onChain,
			StoragePath:                storagePath,
			RoutePrefix:                escrowRoutePrefix(record, routePrefix),
			RefusalTimeoutSeconds:      sessionTimeouts.RefusalTimeoutSeconds,
			ExecutionTimeoutSeconds:    sessionTimeouts.ExecutionTimeoutSeconds,
			RequireHeightSeed:          heightSettings.Enabled && heightSettings.RequireSeed,
			DisableHeightSyncHeartbeat: !heightSettings.Heartbeat,
			ExtraClientConfig:          peerRPCClientConfig(heights.BuildCourier(heightSettings, follower), adoption),
			Heartbeat:                  governance.Heartbeat(),
			QuietSettleDelay:           time.Duration(heightSettings.QuietSettleDelayMS) * time.Millisecond,
			StepTimer:                  steps,
		})
		if err != nil {
			return nil, err
		}
		heights.StartCadence(session, heightSettings)
		return registry.NewSessionHandle(session, machine), nil
	}
}

func escrowRoutePrefix(record store.DevshardRecord, gatewayPrefix string) string {
	if record.RoutePrefix != "" {
		return record.RoutePrefix
	}
	logging.Warn("escrow route prefix unpinned, following the gateway",
		logkey.Escrow, record.EscrowID, logkey.RoutePrefix, gatewayPrefix)
	return gatewayPrefix
}

func readOnlySessions(records devshardLookup, storageDir string) registry.SessionFactory {
	return func(ctx context.Context, escrowID string) (registry.EscrowSession, error) {
		_, keyHex, storagePath, err := sessionInputs(ctx, records, storageDir, escrowID)
		if err != nil {
			return nil, err
		}
		session, machine, err := user.NewLocalSession(user.LocalSessionConfig{
			PrivateKeyHex: keyHex,
			EscrowID:      escrowID,
			StoragePath:   storagePath,
		})
		if err != nil {
			return nil, err
		}
		return registry.NewSessionHandle(session, machine), nil
	}
}

func escrowStorage(storageDir, escrowID string) (string, error) {
	storagePath := api.DevshardStoragePath(storageDir, escrowID)
	if err := os.MkdirAll(storagePath, storageDirectoryMode); err != nil {
		return "", fmt.Errorf("creating storage for escrow %s: %w", escrowID, err)
	}
	return storagePath, nil
}

// copySessionStorage copies regular files only: a directory below a flat SQLite set is not the escrow's.
func copySessionStorage(sourceDir, targetDir string) error {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("reading session storage %s: %w", sourceDir, err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(sourceDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(targetDir, entry.Name()), contents, storageFileMode); err != nil {
			return fmt.Errorf("writing %s: %w", entry.Name(), err)
		}
	}
	return nil
}

const chatCompressMinBytes = 4 << 10

func peerRPCClientConfig(clientConfig *transport.ClientConfig, adoption *transport.PeerRPCAdoption) *transport.ClientConfig {
	if clientConfig == nil {
		clientConfig = &transport.ClientConfig{}
	}
	clientConfig.RPCAdoption = adoption
	clientConfig.ChatCompressMinBytes = chatCompressMinBytes
	return clientConfig
}
