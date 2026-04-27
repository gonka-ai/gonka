package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"common/chain"
	mlnodeclient "common/nodemanager"
	"common/storage/payloads"
	"common/storage/validationlease"
	"devshard/cmd/devshardd/events"
	"devshard/cmd/devshardd/inference"
	"devshard/cmd/devshardd/session"
	chaintx "devshard/cmd/devshardd/tx"
	"devshard/signing"
	devshardstorage "devshard/storage"
	devshardtypes "devshard/types"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
)

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

	pool, err := pgxpool.New(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	closers.Add(pool.Close)

	payloadStore, err := payloads.New(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("payload store: %w", err)
	}

	leaseStore, err := validationlease.New(ctx, pool)
	if err != nil {
		return nil, fmt.Errorf("validation lease store: %w", err)
	}

	manager, err := buildHostManager(ctx, cfg, chainRuntime, mlClient, payloadStore, leaseStore, &closers)
	if err != nil {
		return nil, err
	}

	e := buildServer(chainRuntime.client)
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

	chainEvents := newChainEventBridge(ctx, nodeConfig.ChainRpcUrl, chainClient, txMgr)
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

func buildHostManager(
	ctx context.Context,
	cfg runtimeConfig,
	chainRuntime *chainRuntime,
	mlClient *mlnodeclient.Client,
	payloadStore *payloads.Store,
	leaseStore *validationlease.Store,
	closers *closeStack,
) (*session.HostManager, error) {
	chainParams := inference.NewChainParamsProvider(ctx, chainRuntime.identity)
	normalizedVersion := devshardtypes.NormalizeSessionVersion(cfg.RuntimeVersion)

	phase := chainRuntime.chainEvents.Phase()
	chainBridge := chainRuntime.chainEvents.Bridge()
	eng := inference.NewEngine(mlClient, payloadStore, chainParams, phase)

	instanceAddr := chainRuntime.identity.GetSignerAddress()

	validator := inference.NewValidator(
		chainBridge,
		chainRuntime.identity,
		eng,
		phase,
		normalizedVersion,
	)
	leaseValidator := inference.NewLeaseValidator(validator, phase, leaseStore, instanceAddr)

	storePath := filepath.Join(cfg.DataDir, "devshardd.db")
	store, err := devshardstorage.NewSQLite(storePath)
	if err != nil {
		return nil, fmt.Errorf("devshard sqlite: %w", err)
	}
	closers.Add(func() { store.Close() })

	manager := session.NewHostManager(
		store,
		chainRuntime.signer,
		eng,
		leaseValidator,
		leaseValidator,
		normalizedVersion,
		chainBridge,
		payloadStore,
		chainRuntime.identity,
	)
	chainBridge.OnEscrowCreatedHandler(manager.HandleEscrowCreated)
	chainBridge.OnSettlementFinalizedHandler(manager.HandleSettlementFinalized)

	if err := manager.RecoverSessions(); err != nil {
		slog.Warn("recover sessions failed", "error", err)
	}

	retryLoop := session.NewRetryLoop(leaseStore, validator, manager, phase, instanceAddr)
	retryLoop.WithInterval(cfg.ValidationRetryInterval)
	retryLoop.WithLeaseTTL(cfg.ValidationLeaseTTL)
	go retryLoop.Run(ctx)

	var lastCleanEpoch atomic.Uint64
	chainRuntime.chainEvents.OnNewBlock(func(bctx context.Context, e events.NewBlockEvent) {
		currentEpoch := phase.EpochID()
		if currentEpoch <= lastCleanEpoch.Load() {
			return
		}
		lastCleanEpoch.Store(currentEpoch)

		if currentEpoch >= 2 {
			go func() {
				if err := leaseStore.DeleteBeforeEpoch(context.Background(), currentEpoch); err != nil {
					slog.Warn("validation lease cleanup failed", "error", err)
				}
			}()
		}

		if currentEpoch >= 4 {
			expiredPayloadEpoch := currentEpoch - 3
			go func() {
				if err := payloadStore.DropEpoch(context.Background(), expiredPayloadEpoch); err != nil {
					slog.Warn("payload epoch cleanup failed", "error", err)
				}
			}()
		}
	})

	return manager, nil
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
