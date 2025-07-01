package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener"
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

	chainPhaseTracker := chainphase.NewChainPhaseTracker()

	participantInfo, err := participant.NewCurrentParticipantInfo(recorder)
	if err != nil {
		logging.Error("Failed to get participant info", types.Participants, "error", err)
		return
	}
	chainBridge := broker.NewBrokerChainBridgeImpl(recorder, config.GetChainNodeConfig().Url)
	nodeBroker := broker.NewBroker(chainBridge, chainPhaseTracker, participantInfo, config.GetApiConfig().PoCCallbackUrl, &mlnodeclient.HttpClientFactory{})
	nodes := config.GetNodes()
	for _, node := range nodes {
		nodeBroker.LoadNodeToBroker(&node)
	}

	params, err := getParams(context.Background(), *recorder)
	if err != nil {
		logging.Error("Failed to get params", types.System, "error", err)
		return
	}
	chainPhaseTracker.UpdateEpochParams(*params.Params.EpochParams)

	if err := participant.RegisterParticipantIfNeeded(recorder, config); err != nil {
		logging.Error("Failed to register participant", types.Participants, "error", err)
		return
	}

	logging.Debug("Initializing PoC orchestrator",
		types.PoC, "name", recorder.Account.Name,
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

	training.NewAssigner(recorder, &tendermintClient, ctx)
	trainingExecutor := training.NewExecutor(ctx, nodeBroker, recorder)

	validator := validation.NewInferenceValidator(nodeBroker, config, recorder)
	listener := event_listener.NewEventListener(config, nodePocOrchestrator, nodeBroker, validator, *recorder, trainingExecutor, chainPhaseTracker, cancel)
	// TODO: propagate trainingExecutor
	go listener.Start(ctx)

	addr := fmt.Sprintf(":%v", config.GetApiConfig().PublicServerPort)
	logging.Info("start public server on addr", types.Server, "addr", addr)

	// Bridge external block queue
	blockQueue := pserver.NewBlockQueue(recorder)

	publicServer := pserver.NewServer(nodeBroker, config, recorder, trainingExecutor, blockQueue)
	publicServer.Start(addr)

	addr = fmt.Sprintf(":%v", config.GetApiConfig().MLServerPort)
	logging.Info("start ml server on addr", types.Server, "addr", addr)
	mlServer := mlserver.NewServer(recorder, nodeBroker)
	mlServer.Start(addr)

	addr = fmt.Sprintf(":%v", config.GetApiConfig().AdminServerPort)
	logging.Info("start admin server on addr", types.Server, "addr", addr)
	adminServer := adminserver.NewServer(recorder, nodeBroker, config)
	adminServer.Start(addr)

	addr = fmt.Sprintf(":%v", config.GetApiConfig().MlGrpcServerPort)
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
