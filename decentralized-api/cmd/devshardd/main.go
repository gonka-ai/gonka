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
// Runtime params come from dapi NodeManager long-poll (runtimeconfig) only.
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
	"syscall"
	"time"

	"decentralized-api/apiconfig"
	internaldevshard "decentralized-api/internal/devshard"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/payloadstorage"

	igniteclient "github.com/ignite/cli/v28/ignite/pkg/cosmosclient"
	"github.com/labstack/echo/v4"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"

	devshardpkg "devshard"
	devshardbridge "devshard/bridge"
	mlnodeclient "devshard/mlnode"
	devshardobservability "devshard/observability"
	devshardstorage "devshard/storage"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

// Version is the devshardd version. Set via ldflags
// -X main.Version=... . Defaults to "dev" for local builds without an
// ldflags override.
var Version = "dev"

func main() {
	port := flag.Int("port", 9500, "HTTP listen port (set by versiond)")
	dataDir := flag.String("data-dir", "/var/lib/devshardd", "data directory for sqlite/payloads (set by versiond)")
	flag.Parse()

	oracleVersion := os.Getenv("DEVSHARD_BINARY_VERSION")
	runtimeVersion, err := resolveRuntimeVersion(oracleVersion, Version)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))
	slog.Info("devshardd starting",
		"build_version", Version,
		"oracle_version", oracleVersion,
		"runtime_version", runtimeVersion,
		"port", *port,
		"data-dir", *dataDir)
	if err != nil {
		slog.Error("devshardd version mismatch",
			"build_version", Version,
			"oracle_version", oracleVersion,
			"runtime_version", runtimeVersion)
		log.Fatalf("resolve runtime version: %v", err)
	}

	if err := os.MkdirAll(*dataDir, 0o755); err != nil {
		log.Fatalf("create data dir %s: %v", *dataDir, err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()

	shutdownObservability, err := devshardobservability.Init(ctx, devshardobservability.Config{
		ServiceName:    devshardobservability.ServiceName,
		ServiceVersion: runtimeVersion,
	})
	if err != nil {
		log.Fatalf("init observability: %v", err)
	}
	devshardobservability.SetRuntime("devshardd", runtimeVersion, "standalone_devshardd")
	devshardobservability.SetBuildInfo("devshardd", Version, "")
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = shutdownObservability(shutdownCtx)
	}()

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

	availabilityTracker := devshardpkg.NewAvailabilityTracker(true, 0, 0)

	seedAvailabilityFromChain(ctx, recorder, availabilityTracker)

	paramsSetup, err := newParamsProvider(ctx, recorder, mlClient, availabilityTracker, slog.Default())
	if err != nil {
		log.Fatalf("runtime params provider: %v", err)
	}
	chainParams := paramsSetup.Provider

	br := internaldevshard.NewChainBridge(recorder)

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
	store := devshardstorage.NewManagedStorage(inner, 3, chainParams)
	defer store.Close()
	if paramsSetup.RegisterEpochPrune != nil {
		cancelEpochPrune := paramsSetup.RegisterEpochPrune(store)
		defer cancelEpochPrune()
	}

	manager := internaldevshard.NewHostManager(store, signer, engine, validator, runtimeVersion, br, payloadStore, recorder)
	manager.SetAvailabilityProvider(availabilityTracker)
	manager.SetMaxNonceProvider(internaldevshard.RuntimeConfigMaxNonce(chainParams))
	manager.SetRuntimeParamsProvider(internaldevshard.RuntimeConfigRuntimeParams(chainParams))

	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}
	store.Start()
	manager.SetReady()

	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Server.ConnState = devshardobservability.ConnState("devshardd")
	// Register Go/process collectors only here (standalone devshardd): this
	// registry is not merged with any other, so it owns the runtime metrics.
	devshardobservability.RegisterRuntimeCollectors()
	// /healthz and /metrics intentionally have no EchoMiddleware so they don't
	// emit server spans. Session routes get EchoMiddleware from
	// RegisterLazySessionRoutes inside manager.Register below.
	e.GET("/healthz", func(c echo.Context) error { return c.String(http.StatusOK, "ok") })
	e.GET("/metrics", echo.WrapHandler(devshardobservability.MetricsHandler()))
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

func resolveRuntimeVersion(oracleVersion, buildVersion string) (string, error) {
	if oracleVersion == "" {
		if buildVersion == "" {
			return "", fmt.Errorf("empty build version")
		}
		return buildVersion, nil
	}
	if buildVersion == "" {
		return "", fmt.Errorf("oracle version %q provided but build version is empty", oracleVersion)
	}
	if oracleVersion != buildVersion {
		return oracleVersion, fmt.Errorf("oracle version %q does not match build version %q", oracleVersion, buildVersion)
	}
	return oracleVersion, nil
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

// availabilitySeedTimeout bounds the synchronous chain query used to seed
// AvailabilityTracker at startup. Short so a misconfigured / unreachable chain
// does not delay devshardd boot; the long-lived params provider (grpc or
// chain) corrects the value afterwards on its normal cadence.
const availabilitySeedTimeout = 3 * time.Second

// seedAvailabilityFromChain queries chain params once and records
// DevshardRequestsEnabled into tracker. Errors are logged at warn level; we
// preserve the constructor seed (Enabled=true) so a temporary chain hiccup
// does not refuse all requests until the provider catches up.
func seedAvailabilityFromChain(ctx context.Context, qcp internaldevshard.InferenceQueryClientProvider, tracker *devshardpkg.AvailabilityTracker) {
	if qcp == nil || tracker == nil {
		return
	}
	seedCtx, cancel := context.WithTimeout(ctx, availabilitySeedTimeout)
	defer cancel()

	qc := qcp.NewInferenceQueryClient()
	resp, err := qc.Params(seedCtx, &chaintypes.QueryParamsRequest{})
	if err != nil {
		slog.Warn("availability seed: chain Params query failed; keeping optimistic seed",
			"err", err)
		return
	}
	if resp.Params.DevshardEscrowParams == nil {
		slog.Warn("availability seed: chain returned no DevshardEscrowParams; keeping optimistic seed")
		return
	}
	enabled := resp.Params.DevshardEscrowParams.DevshardRequestsEnabled
	tracker.Record(enabled, time.Now().Unix(), 0)
	slog.Info("availability seed: applied from chain", "devshard_requests_enabled", enabled)
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
