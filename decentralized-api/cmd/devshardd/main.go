// Command devshardd is a standalone devshard host process. It is a temporary
// binary built out of the decentralized-api Go module so that versiond can
// run, download, and manage versioned devshard binaries without waiting for a
// full self-contained rewrite under the devshard/ module.
//
// devshardd reuses dapi's HostManager, ChainBridge, signer, and payload store
// as libraries but strips everything dapi does that a host does not need:
// no admin server, no model manager, no PoC worker, no event dispatcher, no
// block queue, no config sync, no NodeManager gRPC server, no NATS, and no
// transaction manager. devshardd never writes to mainnet.
//
// Versiond's process manager invokes this binary with `--port <N>` and
// `--data-dir <PATH>` as its contract (see versioned/internal/process/manager.go).
// Everything else is configured via env vars.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"decentralized-api/apiconfig"
	internaldevshard "decentralized-api/internal/devshard"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/payloadstorage"

	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/labstack/echo/v4"
	chaintypes "github.com/productscience/inference/x/inference/types"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	devshardbridge "devshard/bridge"
	mlnodeclient "devshard/mlnode"
	devshardstorage "devshard/storage"
	devshardtypes "devshard/types"
)

// Version is the devshardd version. Set via ldflags
// -X main.Version=... . Defaults to "dev" for local builds without an
// ldflags override.
var Version = "dev"

func main() {
	port := flag.Int("port", 9500, "HTTP listen port (set by versiond)")
	dataDir := flag.String("data-dir", "/var/lib/devshardd", "data directory for sqlite/payloads (set by versiond)")
	flag.Parse()

	prefix := os.Getenv("DEVSHARD_LOG_PREFIX")
	runtimeVersion, err := resolveRuntimeVersion(prefix, Version)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("devshardd starting",
		"build_version", Version,
		"selected_version", prefix,
		"runtime_version", runtimeVersion,
		"port", *port,
		"data-dir", *dataDir)
	if err != nil {
		slog.Error("devshardd version mismatch",
			"build_version", Version,
			"selected_version", prefix,
			"runtime_version", runtimeVersion)
		log.Fatalf("resolve runtime version: %v", err)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", *dataDir, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	nodeConfig := loadNodeConfigFromEnv()
	slog.Info("chain node", "url", nodeConfig.Url, "keyring_backend", nodeConfig.KeyringBackend, "keyring_dir", nodeConfig.KeyringDir)

	ignite, err := newIgniteClient(ctx, nodeConfig)
	if err != nil {
		log.Fatalf("ignite cosmosclient: %v", err)
	}

	apiAccount, err := buildApiAccount(ignite, nodeConfig.SignerKeyName)
	if err != nil {
		log.Fatalf("api account: %v", err)
	}

	recorder, err := newQueryOnlyCosmosClient(ctx, ignite, apiAccount)
	if err != nil {
		log.Fatalf("query-only cosmos client: %v", err)
	}

	signer, err := internaldevshard.NewSignerFromKeyring(*recorder.GetKeyring(), apiAccount.SignerAccount.Name)
	if err != nil {
		log.Fatalf("devshard signer: %v", err)
	}

	br := internaldevshard.NewChainBridge(recorder)

	nmAddr := envOr("NODE_MANAGER_ADDR", "localhost:9400")
	slog.Info("nodemanager", "addr", nmAddr)
	mlClient, err := mlnodeclient.NewClient(nmAddr)
	if err != nil {
		log.Fatalf("mlnode client: %v", err)
	}
	defer mlClient.Close()

	payloadDir := filepath.Join(*dataDir, "payloads")
	if err := os.MkdirAll(payloadDir, 0o755); err != nil {
		log.Fatalf("create payload dir: %v", err)
	}
	payloadStore := payloadstorage.NewManagedStorage(
		payloadstorage.NewPayloadStorage(ctx, payloadDir),
		3,
		3*time.Minute,
	)

	httpClient := pserver.NewNoRedirectClient(internaldevshard.MLNodeHTTPTimeout)

	chainParams := newChainParamsProvider(ctx, recorder)

	engine := newDevshardEngine(mlClient, payloadStore, httpClient, chainParams)
	validator := newDevshardValidator(mlClient, httpClient, br, recorder, engine, chainParams)

	storeDir := filepath.Join(*dataDir, "devshardd")
	legacyDB := filepath.Join(*dataDir, "devshardd.db")
	inner, err := devshardstorage.NewStorage(ctx, storeDir)
	if err != nil {
		log.Fatalf("devshard storage: %v", err)
	}
	if migrated, mErr := devshardstorage.MigrateLegacySQLite(legacyDB, inner, func(escrowID string) (uint64, error) {
		info, gErr := br.GetEscrow(escrowID)
		if gErr != nil {
			if errors.Is(gErr, devshardbridge.ErrEscrowNotFound) {
				return 0, devshardstorage.ErrSkipLegacySession
			}
			return 0, gErr
		}
		return info.EpochID, nil
	}); mErr != nil {
		slog.Error("devshardd legacy migration failed", "error", mErr)
	} else if migrated > 0 {
		slog.Info("devshardd legacy migration complete", "sessions_migrated", migrated)
	}
	store := devshardstorage.NewManagedStorage(inner, 3, 30*time.Second, chainParams)
	defer store.Close()

	manager := internaldevshard.NewHostManager(store, signer, engine, validator, devshardtypes.NormalizeSessionVersion(runtimeVersion), br, payloadStore, recorder)
	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}
	store.Start()
	manager.SetReady()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	// Mount HostManager routes at the root. Versiond strips the /<version>/
	// prefix before forwarding, so devshardd sees /sessions/:id/* directly.
	manager.Register(e.Group(""))

	addr := fmt.Sprintf(":%d", *port)
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errCh:
		slog.Error("server error", "error", err)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = e.Shutdown(shutdownCtx)
	slog.Info("devshardd stopped")
}

// loadNodeConfigFromEnv builds a ChainNodeConfig from the same env vars
// dapi's init-docker.sh already uses (NODE_HOST, KEY_NAME, KEYRING_BACKEND,
// KEYRING_PASSWORD, KEYRING_DIR). Reusing these names avoids inventing
// devshardd-only patterns: anything that exports them for dapi automatically
// configures devshardd too. Defaults match production: file keyring backend,
// /root/.inference dir.
func loadNodeConfigFromEnv() apiconfig.ChainNodeConfig {
	nodeHost := envOr("NODE_HOST", "node")
	return apiconfig.ChainNodeConfig{
		Url:             "http://" + nodeHost + ":26657",
		KeyringBackend:  envOr("KEYRING_BACKEND", "file"),
		KeyringDir:      envOr("KEYRING_DIR", "/root/.inference"),
		SignerKeyName:   envOr("KEY_NAME", ""),
		KeyringPassword: os.Getenv("KEYRING_PASSWORD"),
	}
}

// buildApiAccount constructs an apiconfig.ApiAccount for devshardd using the
// same split identity model as dapi:
//   - ACCOUNT_PUBKEY is the cold participant account recorded on chain
//   - KEY_NAME selects the signing key used by the process (warm key on joins)
//
// If ACCOUNT_PUBKEY is unset we fall back to the signer pubkey. That keeps the
// genesis test path working, where signer and account are the same key.
func buildApiAccount(ignite *igniteclient.Client, keyName string) (apiconfig.ApiAccount, error) {
	if keyName == "" {
		return apiconfig.ApiAccount{}, fmt.Errorf("KEY_NAME is required")
	}
	signer, err := ignite.AccountRegistry.GetByName(keyName)
	if err != nil {
		return apiconfig.ApiAccount{}, fmt.Errorf("get signer %q: %w", keyName, err)
	}
	signerPubKey, err := signer.Record.GetPubKey()
	if err != nil {
		return apiconfig.ApiAccount{}, fmt.Errorf("signer pubkey: %w", err)
	}

	accountKey := signerPubKey
	if accountPubKeyBase64 := os.Getenv("ACCOUNT_PUBKEY"); accountPubKeyBase64 != "" {
		pubKeyBytes, decodeErr := base64.StdEncoding.DecodeString(accountPubKeyBase64)
		if decodeErr != nil {
			return apiconfig.ApiAccount{}, fmt.Errorf("decode ACCOUNT_PUBKEY: %w", decodeErr)
		}
		accountKey = &secp256k1.PubKey{Key: pubKeyBytes}
	}

	return apiconfig.ApiAccount{
		AccountKey:    accountKey,
		SignerAccount: &signer,
		AddressPrefix: "gonka",
	}, nil
}

func resolveRuntimeVersion(selectedVersion, buildVersion string) (string, error) {
	if selectedVersion == "" {
		if buildVersion == "" {
			return "", fmt.Errorf("empty build version")
		}
		return buildVersion, nil
	}
	if buildVersion == "" {
		return "", fmt.Errorf("selected version %q provided but build version is empty", selectedVersion)
	}
	if selectedVersion != buildVersion {
		return selectedVersion, fmt.Errorf("selected version %q does not match build version %q", selectedVersion, buildVersion)
	}
	return selectedVersion, nil
}

// newIgniteClient builds an ignite cosmosclient.Client with the same options
// dapi uses minus the NATS/tx_manager setup. Uses `file` keyring backend
// handling identical to cosmosclient.updateKeyringIfNeeded so devshardd reads
// the same keyring dapi writes.
func newIgniteClient(ctx context.Context, nodeConfig apiconfig.ChainNodeConfig) (*igniteclient.Client, error) {
	keyringDir, err := expandHome(nodeConfig.KeyringDir)
	if err != nil {
		return nil, err
	}

	c, err := igniteclient.New(
		ctx,
		igniteclient.WithAddressPrefix("gonka"),
		igniteclient.WithKeyringServiceName("inferenced"),
		igniteclient.WithNodeAddress(nodeConfig.Url),
		igniteclient.WithKeyringDir(keyringDir),
		igniteclient.WithGasPrices("0ngonka"),
		igniteclient.WithFees("0ngonka"),
		igniteclient.WithGas("auto"),
		igniteclient.WithGasAdjustment(5),
	)
	if err != nil {
		return nil, fmt.Errorf("cosmosclient.New: %w", err)
	}

	// For the `file` keyring backend, replace the registry's keyring with one
	// initialized from the plaintext password so non-interactive processes
	// can sign. Mirrors cosmosclient.updateKeyringIfNeeded.
	if nodeConfig.KeyringBackend == keyring.BackendFile {
		reg := codectypes.NewInterfaceRegistry()
		cryptocodec.RegisterInterfaces(reg)
		cdc := codec.NewProtoCodec(reg)
		kr, err := keyring.New(
			"inferenced",
			nodeConfig.KeyringBackend,
			keyringDir,
			strings.NewReader(nodeConfig.KeyringPassword),
			cdc,
		)
		if err != nil {
			return nil, fmt.Errorf("file keyring: %w", err)
		}
		c.AccountRegistry.Keyring = kr
	}

	return &c, nil
}

// chainParamsProvider implements internaldevshard.ChainParamsProvider and
// devshardstorage.EpochProvider for the standalone devshardd binary. It
// queries chain params + the latest epoch on construction and refreshes in
// the background every 60s so long-lived processes pick up governance changes
// (and so the storage pruner advances even when the host is quiet) without a
// restart.
type chainParamsProvider struct {
	mu           sync.Mutex
	logprobsMode string
	currentEpoch uint64
}

func newChainParamsProvider(ctx context.Context, recorder internaldevshard.PayloadAuthClient) *chainParamsProvider {
	p := &chainParamsProvider{logprobsMode: chaintypes.DefaultLogprobsMode}

	refresh := func() {
		qc := recorder.NewInferenceQueryClient()
		resp, err := qc.Params(ctx, &chaintypes.QueryParamsRequest{})
		if err != nil {
			slog.Warn("failed to query chain params, keeping current values", "error", err)
		} else {
			mode := resp.Params.ValidationParams.GetLogprobsMode()
			if mode == "" {
				mode = chaintypes.DefaultLogprobsMode
			}
			p.mu.Lock()
			if mode != p.logprobsMode {
				slog.Info("logprobs_mode updated from chain", "old", p.logprobsMode, "new", mode)
				p.logprobsMode = mode
			}
			p.mu.Unlock()
		}

		epochResp, eErr := qc.EpochInfo(ctx, &chaintypes.QueryEpochInfoRequest{})
		if eErr != nil {
			slog.Warn("failed to query current epoch", "error", eErr)
			return
		}
		idx := epochResp.LatestEpoch.Index
		p.mu.Lock()
		if idx != p.currentEpoch {
			p.currentEpoch = idx
		}
		p.mu.Unlock()
	}

	refresh()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()

	return p
}

func (p *chainParamsProvider) LogprobsMode() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logprobsMode
}

func (p *chainParamsProvider) CurrentEpochID() uint64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.currentEpoch
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func expandHome(path string) (string, error) {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return filepath.Abs(path)
}
