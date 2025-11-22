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
	"decentralized-api/internal/poc"
	adminserver "decentralized-api/internal/server/admin"
	mlserver "decentralized-api/internal/server/mlnode"
	pserver "decentralized-api/internal/server/public"
	"decentralized-api/mlnodeclient"
	"net"

	"github.com/productscience/inference/api/inference/inference"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/participant"
	"decentralized-api/training"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "status" {
		logging.WithNoopLogger(func() (interface{}, error) {
			config, err := apiconfig.LoadDefaultConfigManager()
			if err != nil {
				log.Fatalf("Error loading config: %v", err)
			}
			returnStatus(config)
			return nil, nil
		})

		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "pre-upgrade" {
		os.Exit(1)
	}

	config, err := apiconfig.LoadDefaultConfigManager()
	if err != nil {
		log.Fatalf("Error loading config: %v", err)
	}

	if config.GetApiConfig().TestMode {
		slog.SetLogLoggerLevel(slog.LevelDebug)
	}

	natssrv := server.NewServer(config.GetNatsConfig())
	if err := natssrv.Start(); err != nil {
		panic(err)
	}

	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(
		context.Background(),
		"gonka",
		20,
		5*time.Second,
		config,
	)
	if err != nil {
		panic(err)
	}

	// Version sync is handled later in the event processing loop when blockchain is fully ready
	// This prevents EOF errors during startup from breaking the entire application

	chainPhaseTracker := chainphase.NewChainPhaseTracker()
	// NOTE: getParams is waiting for rpc to be ready, don't add request before it
	params, err := getParams(context.Background(), *recorder)
	if err != nil {
		logging.Error("Failed to get params", types.System, "error", err)
		return
	}
	chainPhaseTracker.UpdateEpochParams(*params.Params.EpochParams)

	participantInfo, err := participant.NewCurrentParticipantInfo(recorder)
	if err != nil {
		logging.Error("Failed to get participant info", types.Participants, "error", err)
		return
	}
	chainBridge := broker.NewBrokerChainBridgeImpl(recorder, config.GetChainNodeConfig().Url)
	nodeBroker := broker.NewBroker(chainBridge, chainPhaseTracker, participantInfo, config.GetApiConfig().PoCCallbackUrl, &mlnodeclient.HttpClientFactory{}, config)

	nodes := config.GetNodes()
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

	if err := participant.RegisterParticipantIfNeeded(recorder, config); err != nil {
		logging.Error("Failed to register participant", types.Participants, "error", err)
		return
	}

	logging.Debug("Initializing PoC orchestrator",
		types.PoC, "name", recorder.GetApiAccount().SignerAccount.Name,
		"address", participantInfo.GetAddress(),
		"pubkey", participantInfo.GetPubKey())

	nodePocOrchestrator := poc.NewNodePoCOrchestratorForCosmosChain(
		participantInfo.GetPubKey(),
		nodeBroker,
		config.GetApiConfig().PoCCallbackUrl,
		config.GetChainNodeConfig().Url,
		recorder,
		chainPhaseTracker,
	)
	logging.Info("node PocOrchestrator orchestrator initialized", types.PoC, "nodePocOrchestrator", nodePocOrchestrator)

	tendermintClient := cosmosclient.TendermintClient{
		ChainNodeUrl: config.GetChainNodeConfig().Url,
	}
	// Create a cancellable context for the entire system
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // Ensure resources are cleaned up

	// Start periodic config auto-flush of dynamic data to DB
	config.StartAutoFlush(ctx, 60*time.Second)

	training.NewAssigner(recorder, &tendermintClient, ctx)
	trainingExecutor := training.NewExecutor(ctx, nodeBroker, recorder)

	validator := validation.NewInferenceValidator(nodeBroker, config, recorder, chainPhaseTracker)
	blsManager := bls.NewBlsManager(*recorder)
	listener := event_listener.NewEventListener(config, nodePocOrchestrator, nodeBroker, validator, *recorder, trainingExecutor, chainPhaseTracker, cancel, blsManager)
	// TODO: propagate trainingExecutor
	go listener.Start(ctx)

	mlnodeBackgroundManager := modelmanager.NewMLNodeBackgroundManager(
		config,
		chainPhaseTracker,
		nodeBroker,
		&mlnodeclient.HttpClientFactory{},
		30*time.Minute,
	)
	go mlnodeBackgroundManager.Start(ctx)

	addr := fmt.Sprintf(":%v", config.GetApiConfig().PublicServerPort)
	logging.Info("start public server on addr", types.Server, "addr", addr)

	// Bridge external block queue
	blockQueue := pserver.NewBlockQueue(recorder)

	publicServer := pserver.NewServer(nodeBroker, config, recorder, trainingExecutor, blockQueue, chainPhaseTracker)
	publicServer.Start(addr)

	addr = fmt.Sprintf(":%v", config.GetApiConfig().MLServerPort)
	logging.Info("start ml server on addr", types.Server, "addr", addr)
	mlServer := mlserver.NewServer(recorder, nodeBroker)
	mlServer.Start(addr)

	addr = fmt.Sprintf(":%v", config.GetApiConfig().AdminServerPort)
	logging.Info("start admin server on addr", types.Server, "addr", addr)
	adminServer := adminserver.NewServer(recorder, nodeBroker, config, validator, blockQueue)
	adminServer.Start(addr)

	mlGrpcServerPort := config.GetApiConfig().MlGrpcServerPort
	if mlGrpcServerPort == 0 {
		mlGrpcServerPort = 9300
		logging.Info("ml grpc server port not set, using default port 9300", types.Server)
	}
	addr = fmt.Sprintf(":%v", mlGrpcServerPort)
	logging.Info("start training server on addr", types.Server, "addr", addr)
	grpcServer := grpc.NewServer()
	trainingServer := training.NewServer(recorder, trainingExecutor)
	inference.RegisterNetworkNodeServiceServer(grpcServer, trainingServer)
	reflection.Register(grpcServer)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	go func() {
		if err := grpcServer.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	logging.Info("Servers started", types.Server, "addr", addr)

	<-ctx.Done()

	ctxFlush, cancelFlush := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelFlush()
	logging.Info("Flushing config to the DB on app exit", types.Config)
	_ = config.FlushNow(ctxFlush)

	// Close DB gracefully
	if db := config.SqlDb().GetDb(); db != nil {
		_ = db.Close()
	}

	os.Exit(1) // Exit with an error for cosmovisor to restart the process
}

func returnStatus(config *apiconfig.ConfigManager) {
	height := config.GetHeight()
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
