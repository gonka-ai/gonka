package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/bls"
	"decentralized-api/internal/event_listener"
	"decentralized-api/internal/modelmanager"
	"decentralized-api/internal/nats/server"
	adminserver "decentralized-api/internal/server/admin"
	mlserver "decentralized-api/internal/server/mlnode"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/mlnodeclient"
	"decentralized-api/payloadstorage"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"
	"decentralized-api/statsstorage"
	"net"

	"decentralized-api/nodemanager"
	nmgen "devshard/nodemanager/gen"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	internaldevshard "decentralized-api/internal/devshard"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/observability"
	"decentralized-api/participant"
	devshardlogging "devshard/logging"
	devshardobservability "devshard/observability"
	devshardstorage "devshard/storage"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/productscience/inference/x/inference/types"

	devshardbridge "devshard/bridge"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "status" {
		logging.WithNoopLogger(func() (interface{}, error) {
			configManager, err := apiconfig.LoadDefaultConfigManager()
			if err != nil {
				log.Fatalf("Error loading config: %v", err)
			}
			returnStatus(configManager)
			return nil, nil
		})

		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "pre-upgrade" {
		os.Exit(1)
	}

	configManager, err := apiconfig.LoadDefaultConfigManager()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	if configManager.GetApiConfig().TestMode {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}
	devshardlogging.SetLogger(devshardlogging.NewSlogAdapter("subsystem", devshardobservability.ServiceName))
	devshardobservability.SetRuntime("api", configManager.GetCurrentNodeVersion(), "dapi_inprocess")
	devshardobservability.SetBuildInfo("api", configManager.GetCurrentNodeVersion(), "")

	natssrv := server.NewServer(configManager.GetNatsConfig())
	if err := natssrv.Start(); err != nil {
		panic(err)
	}

	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(
		context.Background(),
		"gonka",
		20,
		5*time.Second,
		configManager,
	)
	if err != nil {
		panic(err)
	}

	// Version sync is handled later in the event processing loop when blockchain is fully ready
	// This prevents EOF errors during startup from breaking the entire application

	chainPhaseTracker := &chainphase.ChainPhaseTracker{}
	// NOTE: getParams is waiting for rpc to be ready, don't add request before it
	params, err := getParams(context.Background(), *recorder)
	if err != nil {
		logging.Error("Failed to get params", types.System, "error", err)
		return
	}
	chainPhaseTracker.UpdateEpochParams(*params.Params.EpochParams)
	if params.Params.DevshardEscrowParams != nil {
		internaldevshard.SeedDevshardVersionsCache(configManager, params.Params.DevshardEscrowParams)
	}

	participantInfo, err := participant.NewCurrentParticipantInfo(recorder)
	if err != nil {
		logging.Error("Failed to get participant info", types.Participants, "error", err)
		return
	}
	chainBridge := broker.NewBrokerChainBridgeImpl(recorder, configManager.GetChainNodeConfig().Url)
	nodeBroker := broker.NewBroker(chainBridge, chainPhaseTracker, participantInfo, configManager.GetApiConfig().PoCCallbackUrl, &mlnodeclient.HttpClientFactory{}, configManager)

	nodes := configManager.GetNodes()
	for _, node := range nodes {
		responseChan := nodeBroker.LoadNodeToBroker(&node)
		if responseChan != nil {
			response := <-responseChan
			if response.Error != nil {
				logging.Error("Failed to load node to broker. Skipping", types.Nodes, "node_id", node.Id, "error", response.Error)
			} else if response.Node == nil {
				logging.Error("Failed to load node to broker, response.Node == nil and response.Error == nil. Skipping", types.Nodes, "node_id", node.Id)
			} else {
				logging.Info("Successfully loaded node to broker", types.Nodes, "node_id", response.Node.Id)
			}
		}
	}

	if err := participant.RegisterParticipantIfNeeded(recorder, configManager); err != nil {
		logging.Error("Failed to register participant", types.Participants, "error", err)
		return
	}

	logging.Debug("Initializing PoC off-chain validator",
		types.PoC, "name", recorder.GetApiAccount().SignerAccount.Name,
		"address", participantInfo.GetAddress(),
		"pubkey", participantInfo.GetPubKey())

	offChainValidator := poc.NewOffChainValidator(
		recorder,
		nodeBroker,
		chainPhaseTracker,
		configManager.GetApiConfig().PoCCallbackUrl,
		participantInfo.GetPubKey(),
		participantInfo.GetAddress(),
		configManager.GetChainNodeConfig().Url,
		poc.DefaultValidationConfig(),
	)
	logging.Info("PoC off-chain validator initialized", types.PoC)

	// Create a cancellable context for the entire system
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure resources are cleaned up

	// Initialize OpenTelemetry. Returns a noop shutdown when disabled, so it
	// is safe to defer unconditionally. Trace context propagation is wired in
	// either case so downstream services see the trace ids.
	shutdownObservability, err := observability.Init(ctx, observability.Config{
		ServiceName:        observability.ServiceName,
		ParticipantAddress: participantInfo.GetAddress(),
	})
	if err != nil {
		logging.Error("Failed to initialize observability", types.System, "error", err)
		return
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = shutdownObservability(shutdownCtx)
	}()

	// Start periodic config auto-flush of dynamic data to DB
	configManager.StartAutoFlush(ctx, 60*time.Second)

	// Optional off-chain inference stats storage (PostgreSQL-backed when PGHOST is configured).
	statsStore, err := statsstorage.NewStatsStorage(ctx)
	if err != nil {
		logging.Error("Failed to initialize stats storage", types.System, "error", err)
		return
	}
	if statsStore != nil {
		defer statsStore.Close()
	}

	validator := validation.NewInferenceValidator(nodeBroker, configManager, recorder, chainPhaseTracker)
	blsManager := bls.NewBlsManager(*recorder)
	if db := configManager.SqlDb().GetDb(); db != nil {
		if err := blsManager.SetDealerOpeningsDB(db); err != nil {
			logging.Warn("Failed to initialize dealer openings persistence", types.BLS, "error", err)
		}
	}
	listener := event_listener.NewEventListener(
		configManager,
		offChainValidator,
		nodeBroker,
		validator,
		*recorder,
		chainPhaseTracker,
		cancel,
		blsManager,
		event_listener.WithStatsStorage(statsStore),
	)
	go listener.Start(ctx)

	mlnodeBackgroundManager := modelmanager.NewMLNodeBackgroundManager(
		configManager,
		chainPhaseTracker,
		nodeBroker,
		&mlnodeclient.HttpClientFactory{},
		30*time.Minute,
	)
	go mlnodeBackgroundManager.Start(ctx)

	addr := fmt.Sprintf(":%v", configManager.GetApiConfig().PublicServerPort)
	logging.Info("start public server on addr", types.Server, "addr", addr)

	// Bridge external block queue
	blockQueue := pserver.NewBlockQueue(recorder)

	// Shared payload storage for both public and admin servers
	// Uses PostgreSQL if PGHOST is set and accessible, otherwise file-based
	// ManagedStorage provides read caching + automatic epoch pruning (retains last 3 epochs)
	payloadStore := payloadstorage.NewManagedStorage(
		payloadstorage.NewPayloadStorage(ctx, "/root/.dapi/data/inference"),
		3,             // retain current + 2 previous epochs
		3*time.Minute, // cache TTL
	)

	// Shared managed artifact store for off-chain PoC (used by both mlnode and public servers)
	// Manages per-height directories with automatic pruning (retains last 10)
	artifactStore := artifacts.NewManagedArtifactStore("/root/.dapi/data/poc-artifacts", 10)
	defer artifactStore.Close()

	// Create commit worker for time-based artifact commits and weight distribution
	// Worker owns flush lifecycle, commits periodically (not per-request), and handles distribution
	batchingCfg := configManager.GetTxBatchingConfig()
	commitInterval := time.Duration(batchingCfg.PocCommitIntervalSeconds) * time.Second
	commitWorker := poc.NewCommitWorker(artifactStore, recorder, chainPhaseTracker, participantInfo.GetAddress(), commitInterval)
	defer commitWorker.Close()

	devshardSigner, devshardSignerErr := internaldevshard.NewSignerFromKeyring(*recorder.GetKeyring(), recorder.GetApiAccount().SignerAccount.Name)
	if devshardSignerErr != nil {
		logging.Error("devshard signer init failed", types.System, "error", devshardSignerErr)
	}

	publicServer := pserver.NewServer(
		nodeBroker,
		configManager,
		recorder,
		blockQueue,
		chainPhaseTracker,
		payloadStore,
		pserver.WithArtifactStore(artifactStore),
		pserver.WithStatsStorage(statsStore),
	)

	if devshardSigner != nil {
		devshardBridge := internaldevshard.NewChainBridge(recorder)
		httpClient := pserver.NewNoRedirectClient(internaldevshard.MLNodeHTTPTimeout)
		chainParams := &configParamsProvider{cm: configManager}
		devshardEngine := internaldevshard.NewEngineAdapter(nodeBroker, configManager.GetCurrentNodeVersion(), payloadStore, chainPhaseTracker, httpClient, chainParams)
		devshardValidator := internaldevshard.NewValidationAdapter(nodeBroker, configManager.GetCurrentNodeVersion(), chainPhaseTracker, httpClient, devshardBridge, recorder, chainParams)

		// Per-epoch SQLite under /root/.dapi/data/devshard/, or shared Postgres
		// (same PG vars as payloadstorage) when PGHOST is set. ManagedStorage
		// runs the background pruner with N=3 retention.
		// TODO: move to DevshardConfig when config consolidation happens.
		const devshardDir = "/root/.dapi/data/devshard"
		const devshardLegacyDB = "/root/.dapi/data/devshard.db"
		devshardInner, storeErr := devshardstorage.NewStorage(ctx, devshardDir)
		if storeErr != nil {
			logging.Error("devshard storage init failed", types.System, "error", storeErr)
		} else {
			devshardStore := devshardstorage.NewManagedStorage(devshardInner, 3, &chainPhaseEpochProvider{tracker: chainPhaseTracker})
			defer devshardStore.Close()

			configManager.SetEpochChangeHandler(func(_, _ uint64) {
				devshardStore.PruneOnceAsync(ctx)
			})

			hostManager := internaldevshard.NewHostManager(devshardStore, devshardSigner, devshardEngine, devshardValidator, "v1", devshardBridge, payloadStore, recorder)
			hostManager.SetAvailabilityProvider(internaldevshard.NewConfigManagerAvailability(configManager, chainPhaseTracker))
			hostManager.SetMaxNonceProvider(internaldevshard.ConfigManagerMaxNonce(configManager))
			hostManager.SetRuntimeParamsProvider(internaldevshard.ConfigManagerRuntimeParams(configManager))
			hostManager.Register(publicServer.DevshardGroup())
			go func() {
				migrated, mErr := devshardstorage.MigrateLegacySQLite(devshardLegacyDB, devshardInner, func(escrowID string) (uint64, error) {
					info, err := devshardBridge.GetEscrow(escrowID)
					if err != nil {
						if errors.Is(err, devshardbridge.ErrEscrowNotFound) {
							return 0, devshardstorage.ErrSkipLegacySession
						}
						return 0, err
					}
					return info.EpochID, nil
				})
				if mErr != nil {
					logging.Error("devshard legacy migration failed", types.System, "error", mErr)
					hostManager.SetUnavailable(mErr)
					return
				}
				if migrated > 0 {
					logging.Info("devshard legacy migration complete", types.System, "sessions_migrated", migrated)
				}

				devshardStore.Start()
				hostManager.SetReady()
				if err := hostManager.RecoverSessions(); err != nil {
					logging.Error("devshard recovery failed", types.System, "error", err)
				}
			}()
		}
	}
	publicServer.Start(addr)

	addr = fmt.Sprintf(":%v", configManager.GetApiConfig().MLServerPort)
	logging.Info("start ml server on addr", types.Server, "addr", addr)
	mlServer := mlserver.NewServer(recorder, nodeBroker, mlserver.WithArtifactStore(artifactStore), mlserver.WithConfigManager(configManager))
	mlServer.Start(addr)

	addr = fmt.Sprintf(":%v", configManager.GetApiConfig().AdminServerPort)
	logging.Info("start admin server on addr", types.Server, "addr", addr)
	adminServer := adminserver.NewServer(recorder, nodeBroker, configManager, validator, blockQueue, payloadStore)
	adminServer.Start(addr)

	nmGrpcPort := configManager.GetApiConfig().NodeManagerGrpcPort
	if nmGrpcPort == 0 {
		nmGrpcPort = 9400
	}
	// Negative ports explicitly disable the NodeManager gRPC server.
	if nmGrpcPort > 0 {
		nmGrpcServer := grpc.NewServer()
		nmgen.RegisterNodeManagerServer(nmGrpcServer, nodemanager.NewServer(nodeBroker, configManager, chainPhaseTracker))
		reflection.Register(nmGrpcServer)
		nodeManagerAddr := fmt.Sprintf(":%v", nmGrpcPort)
		nmLis, err := net.Listen("tcp", nodeManagerAddr)
		if err != nil {
			log.Fatalf("node manager failed to listen on %v: %v", nodeManagerAddr, err)
		}
		go func() {
			logging.Info("start node manager gRPC server", types.Server, "nodeManagerAddr", nodeManagerAddr)
			if err := nmGrpcServer.Serve(nmLis); err != nil {
				log.Fatalf("node manager gRPC server failed: %v", err)
			}
		}()
	}

	logging.Info("Servers started", types.Server, "addr", addr)

	<-ctx.Done()

	ctxFlush, cancelFlush := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFlush()
	logging.Info("Flushing config to the DB on app exit", types.Config)
	_ = configManager.FlushNow(ctxFlush)

	// Close DB gracefully
	if db := configManager.SqlDb().GetDb(); db != nil {
		_ = db.Close()
	}

	os.Exit(1) // Exit with an error for cosmovisor to restart the process
}

func returnStatus(configManager *apiconfig.ConfigManager) {
	height := configManager.GetHeight()
	status := map[string]interface{}{
		"sync_info": map[string]string{
			"latest_block_height": strconv.FormatInt(height, 10),
		},
	}
	jsonData, err := json.MarshalIndent(status, "", "  ")
	if err != nil {
		panic(err)
	}
	fmt.Println(string(jsonData))
	os.Exit(0)
}

func getParams(ctx context.Context, transactionRecorder cosmosclient.InferenceCosmosClient) (*types.QueryParamsResponse, error) {
	var params *types.QueryParamsResponse
	var err error
	for i := 0; i < 10; i++ {
		params, err = transactionRecorder.NewInferenceQueryClient().Params(ctx, &types.QueryParamsRequest{})
		if err == nil {
			return params, nil
		}

		if strings.HasPrefix(err.Error(), "rpc error: code = Unknown desc = inference is not ready") {
			logging.Info("Inference not ready, retrying...", types.System, "attempt", i+1, "error", err)
			time.Sleep(2 * time.Second) // Try a longer wait for specific inference delays
			continue
		}
		// If not an RPC error, log and return early
		logging.Error("Failed to get chain params", types.System, "error", err)
		return nil, err
	}
	logging.Error("Exhausted all retries to get chain params", types.System, "error", err)
	return nil, err
}

// configParamsProvider implements internaldevshard.ChainParamsProvider by
// reading from dapi's ConfigManager, which syncs chain params every block.
type configParamsProvider struct {
	cm *apiconfig.ConfigManager
}

func (p *configParamsProvider) LogprobsMode() string {
	mode := p.cm.GetValidationParams().LogprobsMode
	if mode == "" {
		return types.DefaultLogprobsMode
	}
	return mode
}

// chainPhaseEpochProvider exposes the current chain epoch to ManagedStorage
// so the pruner advances the retention horizon even when the host has no
// CreateSession activity to bump max_observed_epoch from.
type chainPhaseEpochProvider struct {
	tracker *chainphase.ChainPhaseTracker
}

func (p *chainPhaseEpochProvider) CurrentEpochID() uint64 {
	if p.tracker == nil {
		return 0
	}
	st := p.tracker.GetCurrentEpochState()
	if st == nil {
		return 0
	}
	return st.LatestEpoch.EpochIndex
}
