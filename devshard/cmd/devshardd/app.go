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
	mlnodeclient "common/nodemanager"
	commrc "common/runtimeconfig"
	"common/storage/payloads"
	devshardpkg "devshard"
	"devshard/cmd/devshardd/events"
	"devshard/cmd/devshardd/inference"
	"devshard/cmd/devshardd/session"
	chaintx "devshard/cmd/devshardd/tx"
	"devshard/runtimeparams"
	"devshard/signing"
	devshardstorage "devshard/storage"

	"github.com/labstack/echo/v4"
)

const sessionEpochRetain = 3

type devshardApp struct {
	server      *echo.Echo
	chainEvents *chainEventBridge
	port        int
	close       func()
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
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data dir %s: %w", cfg.DataDir, err)
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
	payloadStore, payloadClose, err := payloads.Open(ctx, payloads.OpenConfig{Dir: payloadDir})
	if err != nil {
		return nil, fmt.Errorf("payload store: %w", err)
	}
	closers.Add(payloadClose)

	manager, err := buildHostManager(ctx, cfg, chainRuntime, mlClient, payloadStore, &closers)
	if err != nil {
		return nil, err
	}

	e := buildServer()
	manager.Register(e.Group(""))

	return &devshardApp{
		server:      e,
		chainEvents: chainRuntime.chainEvents,
		port:        cfg.Port,
		close:       closers.Close,
	}, nil
}

func buildChainRuntime(ctx context.Context, nodeConfig ChainNodeConfig) (*chainRuntime, error) {
	slog.Info("chain node",
		"url", nodeConfig.ChainRpcUrl,
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

	chainClient, err := chain.New(nodeConfig.ChainGrpcUrl)
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
	eng := inference.NewEngine(mlClient, mlNodeMgr, payloadStore, chainParams, phase)

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
	)

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

	manager := session.NewHostManager(
		store,
		chainRuntime.signer,
		eng,
		leaseValidator,
		leaseValidator,
		cfg.RuntimeVersion,
		chainBridge,
		payloadStore,
		chainRuntime.identity,
	)
	manager.SetAvailabilityProvider(availabilityTracker)
	manager.SetMaxNonceProvider(runtimeparams.MaxNonceFromSnapshot(chainParams))
	chainBridge.OnSettlementFinalizedHandler(manager.HandleSettlementFinalized)

	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}
	store.Start()

	retryLoop := session.NewRetryLoop(store, validator, manager, phase, instanceAddr)
	retryLoop.WithInterval(cfg.ValidationRetryInterval)
	retryLoop.WithLeaseTTL(cfg.ValidationLeaseTTL)
	retryLoopCtx, cancelRetryLoop := context.WithCancel(ctx)
	retryLoopDone := make(chan struct{})
	closers.Add(func() {
		cancelRetryLoop()
		<-retryLoopDone
	})
	go func() {
		defer close(retryLoopDone)
		retryLoop.Run(retryLoopCtx)
	}()

	var lastCleanEpoch atomic.Uint64
	chainRuntime.chainEvents.OnNewBlock(func(bctx context.Context, e events.NewBlockEvent) {
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
	errCh := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", addr)
		if err := a.server.Start(addr); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	var runErr error
	chainEventsStopped := false
	select {
	case <-ctx.Done():
		slog.Info("shutdown requested")
	case err := <-errCh:
		runErr = fmt.Errorf("server error: %w", err)
	case err := <-chainEventsErrCh:
		chainEventsStopped = true
		if err != nil {
			runErr = fmt.Errorf("chain events listener: %w", err)
		} else {
			runErr = fmt.Errorf("chain events listener stopped")
		}
	}

	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	_ = a.server.Shutdown(shutdownCtx)
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
