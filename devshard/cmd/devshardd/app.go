package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"common/chain"
	"common/chainoracle/blocks"
	"common/httpguard"
	mlnodeclient "common/nodemanager"
	commrc "common/runtimeconfig"
	"common/storage/mode"
	"common/storage/payloads"
	devshardpkg "devshard"
	devshardbridge "devshard/cmd/devshardd/bridge"
	"devshard/cmd/devshardd/events"
	"devshard/cmd/devshardd/inference"
	"devshard/cmd/devshardd/session"
	chaintx "devshard/cmd/devshardd/tx"
	"devshard/hostevents"
	"devshard/runtimeparams"
	"devshard/signing"
	devshardstorage "devshard/storage"

	"github.com/labstack/echo/v4"
)

const sessionEpochRetain = 3

type devshardApp struct {
	server        *echo.Echo
	adminServer   *echo.Echo
	adminAddr     string
	chainEvents   *chainEventBridge
	port          int
	lifecycle     *lifecycleState
	shutdownGrace time.Duration
	storageErrors <-chan error
	close         func()
}

type chainRuntime struct {
	client      *chain.Client
	identity    *chainIdentity
	chainEvents *chainEventBridge
	signer      *signing.Secp256k1Signer
}

type closeStack []func()

func (s *closeStack) Add(fn func()) {
	*s = append(*s, fn)
}

func (s closeStack) Close() {
	for i := len(s) - 1; i >= 0; i-- {
		s[i]()
	}
}

type phaseEpochProvider struct {
	phase *chain.Phase
}

func (p phaseEpochProvider) CurrentEpochID() uint64 {
	if p.phase == nil {
		return 0
	}
	return p.phase.EpochID()
}

func buildApp(ctx context.Context, cfg runtimeConfig) (_ *devshardApp, err error) {
	if err := requireHADeploymentStorage(); err != nil {
		return nil, err
	}

	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
	}

	// Wire the dial-time SSRF guard before anything can dial out. Guarded
	// clients read the flag per dial, so this also covers the package-level
	// validation.PayloadRetrievalClient constructed at init.
	httpguard.SetAllowPrivate(cfg.AllowPrivateAddresses)
	if cfg.AllowPrivateAddresses {
		slog.Warn("SSRF guard disabled: dials to private/internal addresses are allowed",
			"env", "DEVSHARD_ALLOW_PRIVATE_ADDRESSES")
	}

	var closers closeStack
	defer func() {
		if err != nil {
			closers.Close()
		}
	}()

	chainRuntime, err := buildChainRuntime(ctx, cfg.Node)
	if err != nil {
		return nil, err
	}

	mlClient, err := buildMLNodeClient(cfg.NodeManagerAddr)
	if err != nil {
		return nil, err
	}
	closers.Add(func() { mlClient.Close() })

	payloadDir := filepath.Join(cfg.DataDir, "payloads")
	payloadStore, payloadClose, err := payloads.Open(ctx, payloads.OpenConfig{Dir: payloadDir, CompressFiles: cfg.CompressPayloadFiles})
	if err != nil {
		return nil, fmt.Errorf("payload store: %w", err)
	}
	closers.Add(payloadClose)

	manager, err := buildHostManager(ctx, cfg, chainRuntime, mlClient, payloadStore, &closers)
	if err != nil {
		return nil, err
	}

	lifecycle := newLifecycleState()
	e := buildServer(lifecycle)
	var admin *echo.Echo
	if cfg.AdminAddr != "" {
		admin = buildAdminServer(lifecycle, manager.StorageReady, manager.StorageProof)
	}
	manager.Register(e.Group(""))
	chainRuntime.chainEvents.OnReady(lifecycle.SetReady)

	return &devshardApp{
		server:        e,
		adminServer:   admin,
		adminAddr:     cfg.AdminAddr,
		chainEvents:   chainRuntime.chainEvents,
		port:          cfg.Port,
		lifecycle:     lifecycle,
		shutdownGrace: cfg.ShutdownGrace,
		storageErrors: manager.StorageFatalErrors(),
		close:         closers.Close,
	}, nil
}

func buildChainRuntime(ctx context.Context, nodeConfig ChainNodeConfig) (*chainRuntime, error) {
	slog.Info("chain node",
		"grpc_url", nodeConfig.ChainGrpcUrl,
		"rpc_url", nodeConfig.ChainRpcUrl,
		"keyring_backend", nodeConfig.KeyringBackend,
		"keyring_dir", nodeConfig.KeyringDir)

	kr, err := buildKeyring(nodeConfig)
	if err != nil {
		return nil, fmt.Errorf("keyring: %w", err)
	}

	apiAccount, err := buildApiAccount(kr, nodeConfig)
	if err != nil {
		return nil, fmt.Errorf("api account: %w", err)
	}

	chainClient, err := chain.NewWithQueryFallback(nodeConfig.ChainGrpcUrl, nodeConfig.ChainRpcUrl)
	if err != nil {
		return nil, fmt.Errorf("chain client: %w", err)
	}
	chainID, err := resolveChainID(ctx, chainClient, nodeConfig.ChainID)
	if err != nil {
		return nil, fmt.Errorf("chain id: %w", err)
	}

	identity, err := newChainIdentity(chainClient, apiAccount, kr)
	if err != nil {
		return nil, fmt.Errorf("chain identity: %w", err)
	}

	signer, err := signing.NewSignerFromKeyring(kr, apiAccount.SignerRecord.Name)
	if err != nil {
		return nil, fmt.Errorf("devshard signer: %w", err)
	}

	txMgr, err := chaintx.New(chainClient.Conn(), kr, identity.GetSignerAddress(), nodeConfig.SignerKeyName, chainID)
	if err != nil {
		return nil, fmt.Errorf("tx manager: %w", err)
	}

	chainEvents := newChainEventBridge(ctx, nodeConfig.ChainRpcUrl, chainClient, chaintx.NewDisputeSubmitter(txMgr))
	return &chainRuntime{
		client:      chainClient,
		identity:    identity,
		chainEvents: chainEvents,
		signer:      signer,
	}, nil
}

func buildMLNodeClient(addr string) (*mlnodeclient.Client, error) {
	slog.Info("nodemanager", "addr", addr)
	mlClient, err := mlnodeclient.NewClient(addr)
	if err != nil {
		return nil, fmt.Errorf("mlnode client: %w", err)
	}
	return mlClient, nil
}

func buildMLNodeManager(ctx context.Context) *mlnodeclient.Manager {
	ttl := mlnodeclient.DefaultCacheTTL
	if v := os.Getenv("MLNODE_CACHE_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			ttl = d
		}
	}
	mgr := mlnodeclient.NewManager(ttl)
	mgr.Start(ctx)
	slog.Info("mlnode cache", "ttl", ttl)
	return mgr
}

// buildMLNodeCapacityCache polls ListNodeCapacity via mlClient to bound the
// passive-fallback path (see mlnodeclient.Cache). Old dapi builds that do not
// implement the RPC leave the cache unobserved, so fallback stays unbounded.
func buildMLNodeCapacityCache(ctx context.Context, mlClient *mlnodeclient.Client) *mlnodeclient.Cache {
	cache := mlnodeclient.NewCache(mlClient.NodeManagerClient(), mlnodeclient.CacheOptions{})
	cache.Start(ctx)
	return cache
}

func buildHostManager(
	ctx context.Context,
	cfg runtimeConfig,
	chainRuntime *chainRuntime,
	mlClient *mlnodeclient.Client,
	payloadStore payloads.Storage,
	closers *closeStack,
) (*session.HostManager, error) {
	availabilityTracker := devshardpkg.NewAvailabilityTracker(true, 0, 0)
	seedAvailabilityFromChain(ctx, chainRuntime.client, availabilityTracker)

	paramsSetup, err := newParamsProvider(ctx, chainRuntime.client, mlClient, availabilityTracker, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("runtime params provider: %w", err)
	}
	closers.Add(paramsSetup.close)

	phase := chainRuntime.chainEvents.Phase()
	chainBridge := chainRuntime.chainEvents.Bridge()
	chainParams := paramsSetup.Provider
	mlNodeMgr := buildMLNodeManager(ctx)
	mlNodeCapacity := buildMLNodeCapacityCache(ctx, mlClient)
	eng := inference.NewEngine(mlClient, mlNodeMgr, mlNodeCapacity, payloadStore, chainParams, phase)

	instanceAddr := chainRuntime.identity.GetSignerAddress()

	thresholds := inference.NewValidationThresholdResolver(paramsSetup.Provider, chainBridge)
	validator := inference.NewValidator(
		chainBridge,
		chainRuntime.identity,
		eng,
		phase,
		cfg.RuntimeVersion,
		chainParams,
		thresholds,
		cfg.VoteFalseOnFetchFailure,
	)

	if err := mode.RequireHADeploymentStorage(); err != nil {
		return nil, err
	}

	innerStore, err := devshardstorage.NewStorage(ctx, cfg.DataDir)
	if err != nil {
		return nil, fmt.Errorf("devshard storage: %w", err)
	}
	store := devshardstorage.NewManagedStorage(innerStore, sessionEpochRetain, chainParams)
	if cancel := paramsSetup.RegisterEpochPrune(store); cancel != nil {
		closers.Add(cancel)
	}
	closers.Add(func() { _ = store.Close() })

	leaseValidator := inference.NewLeaseValidator(validator, phase, store, instanceAddr, cfg.ValidationLeaseTTL)

	// warmBridge lets lazy bind fall back to escrow_cache (populated by the
	// host-events long-poll warm) when the live chain escrow query is
	// unavailable. Only the session/bind read path is cache-aware; validation
	// and settlement keep using the live chainBridge.
	warmBridge := devshardbridge.NewCachingEscrowBridge(chainBridge, store, slog.Default())

	manager := session.NewHostManager(
		store,
		chainRuntime.signer,
		eng,
		leaseValidator,
		leaseValidator,
		cfg.RuntimeVersion,
		warmBridge,
		payloadStore,
		chainRuntime.identity,
	)
	manager.SetAvailabilityProvider(availabilityTracker)
	manager.SetMaxNonceProvider(runtimeparams.MaxNonceFromSnapshot(chainParams))
	manager.SetParamsProvider(runtimeparams.FromSnapshot(chainParams))
	manager.SetBinaryVersion(cfg.BinaryLogVersion)
	if err := manager.SetHeightSyncFromEnv(ctx, chainRuntime.client); err != nil {
		return nil, fmt.Errorf("height sync oracle: %w", err)
	}
	closers.Add(manager.CloseHeightSync)
	chainBridge.OnSettlementFinalizedHandler(manager.HandleSettlementFinalized)

	startHostEventsWarm(ctx, cfg, chainBridge, mlClient, store, manager.HandleSettlementFinalized, closers)

	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}
	store.Start()

	validationRetry := session.NewValidationRetryLoop(store, validator, manager, phase, instanceAddr)
	validationRetry.WithInterval(cfg.ValidationRetryInterval)
	validationRetry.WithLeaseTTL(cfg.ValidationLeaseTTL)
	validationRetryCtx, cancelValidationRetry := context.WithCancel(ctx)
	validationRetryDone := make(chan struct{})
	closers.Add(func() {
		cancelValidationRetry()
		<-validationRetryDone
	})
	go func() {
		defer close(validationRetryDone)
		validationRetry.Run(validationRetryCtx)
	}()

	var lastCleanEpoch atomic.Uint64
	chainRuntime.chainEvents.OnNewBlock(func(bctx context.Context, e events.NewBlockEvent) {
		manager.ObserveChainHeader(blocks.HashOnlyHeader(e.BlockHeight, e.Time, e.ChainID, e.BlockHash))

		currentEpoch := phase.EpochID()
		if currentEpoch <= lastCleanEpoch.Load() {
			return
		}
		lastCleanEpoch.Store(currentEpoch)

		store.PruneOnceAsync(bctx)

		if currentEpoch >= 4 {
			expiredPayloadEpoch := currentEpoch - 3
			if err := payloadStore.DropEpoch(bctx, expiredPayloadEpoch); err != nil {
				logCleanupError("payload epoch cleanup failed", err)
			}
		}
	})

	return manager, nil
}

// startHostEventsWarm launches the DAPI GetHostEvents long-poll consumer that
// prefetches escrow metadata into escrow_cache (PR #1443). It is a no-op when
// disabled, and the loop also stops cleanly against an old dapi that returns
// Unimplemented, leaving lazy escrow create as the fallback.
func startHostEventsWarm(
	ctx context.Context,
	cfg runtimeConfig,
	chainBridge *devshardbridge.ChainBridge,
	mlClient *mlnodeclient.Client,
	store devshardstorage.Storage,
	onSettled func(escrowID string) error,
	closers *closeStack,
) {
	if !cfg.HostEventsEnabled {
		slog.Info("hostevents: escrow long-poll warm disabled (DEVSHARD_HOST_EVENTS_ENABLED=false)")
		return
	}
	sink := newEscrowWarmSink(chainBridge, store, slog.Default(), onSettled)
	hostCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	closers.Add(func() {
		cancel()
		<-done
	})
	go func() {
		defer close(done)
		slog.Info("hostevents: starting escrow long-poll warm", "node_manager_addr", cfg.NodeManagerAddr)
		hostevents.Run(hostCtx, hostevents.Config{
			Client: mlClient.NodeManagerClient(),
			Log:    slog.Default(),
		}, sink)
	}()
}

const availabilitySeedTimeout = 3 * time.Second

func seedAvailabilityFromChain(ctx context.Context, chainClient *chain.Client, tracker *devshardpkg.AvailabilityTracker) {
	if chainClient == nil || tracker == nil {
		return
	}
	seedCtx, cancel := context.WithTimeout(ctx, availabilitySeedTimeout)
	defer cancel()

	snap, err := commrc.NewChainFetcher(chainClient).FetchSnapshot(seedCtx)
	if err != nil {
		slog.Warn("availability seed: chain Params query failed; keeping optimistic seed", "err", err)
		return
	}
	tracker.Record(snap.DevshardRequestsEnabled, time.Now().Unix(), 0)
	slog.Info("availability seed: applied from chain", "devshard_requests_enabled", snap.DevshardRequestsEnabled)
}

func logCleanupError(msg string, err error) {
	if errors.Is(err, context.Canceled) {
		return
	}
	slog.Warn(msg, "error", err)
}

func (a *devshardApp) Run(ctx context.Context) error {
	defer a.close()

	appCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	chainEventsErrCh := make(chan error, 1)
	go func() {
		chainEventsErrCh <- a.chainEvents.Start(appCtx)
	}()

	addr := fmt.Sprintf(":%d", a.port)
	type serverError struct {
		name string
		err  error
	}
	errCh := make(chan serverError, 2)
	startServer := func(name string, server *echo.Echo, addr string) {
		go func() {
			slog.Info("listening", "server", name, "addr", addr)
			if err := server.Start(addr); err != nil && err != http.ErrServerClosed {
				errCh <- serverError{name: name, err: err}
			}
		}()
	}
	startServer("public", a.server, addr)
	if a.adminServer != nil {
		startServer("admin", a.adminServer, a.adminAddr)
	}

	var runErr error
	chainEventsStopped := false
	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errCh:
		runErr = fmt.Errorf("%s server error: %w", err.name, err.err)
	case err := <-chainEventsErrCh:
		chainEventsStopped = true
		if err != nil {
			runErr = fmt.Errorf("chain events listener: %w", err)
		} else {
			runErr = fmt.Errorf("chain events listener stopped")
		}
	case err := <-a.storageErrors:
		runErr = fmt.Errorf("terminal storage failure: %w", err)
	}

	a.lifecycle.StartDrain()
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), a.shutdownGrace)
	defer shutdownCancel()
	_ = a.server.Shutdown(shutdownCtx)
	if a.adminServer != nil {
		_ = a.adminServer.Shutdown(shutdownCtx)
	}
	if !chainEventsStopped {
		select {
		case err := <-chainEventsErrCh:
			if err != nil && runErr == nil {
				runErr = fmt.Errorf("chain events listener: %w", err)
			}
		case <-shutdownCtx.Done():
			slog.Warn("chain events listener did not stop before shutdown timeout")
		}
	}
	slog.Info("devshardd stopped")
	return runErr
}
