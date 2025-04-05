package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener"
	"decentralized-api/internal/poc"
	"decentralized-api/internal/server"
	"decentralized-api/internal/validation"
	"decentralized-api/logging"
	"decentralized-api/participant_registration"
	"decentralized-api/training"
	"encoding/json"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"log"
	"os"
	"strconv"
	"strings"
	"time"
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

	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(
		context.Background(),
		"cosmos",
		10,
		5*time.Second,
		config,
	)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker(recorder)
	nodes := config.GetNodes()
	for _, node := range nodes {
		server.LoadNodeToBroker(nodeBroker, &node)
	}

	params, err := getParams(context.Background(), *recorder)
	if err != nil {
		logging.Error("Failed to get params", types.System, "error", err)
		return
	}

	if err := participant_registration.RegisterParticipantIfNeeded(recorder, config, nodeBroker); err != nil {
		logging.Error("Failed to register participant", types.Participants, "error", err)
		return
	}

	pubKey, err := recorder.Account.Record.GetPubKey()
	if err != nil {
		logging.Error("Failed to get public key", types.EventProcessing, "error", err)
		return
	}
	pubKeyString := utils.PubKeyToHexString(pubKey)

	logging.Debug("Initializing PoC orchestrator",
		types.PoC, "name", recorder.Account.Name,
		"address", recorder.Address,
		"pubkey", pubKeyString)

	pocOrchestrator := poc.NewPoCOrchestrator(pubKeyString, int(params.Params.PocParams.DefaultDifficulty))

	logging.Info("PoC orchestrator initialized", types.PoC, "pocOrchestrator", pocOrchestrator)
	go pocOrchestrator.Run()

	nodePocOrchestrator := poc.NewNodePoCOrchestrator(
		pubKeyString,
		nodeBroker,
		config.GetApiConfig().PoCCallbackUrl,
		config.GetChainNodeConfig().Url,
		recorder,
		&params.Params,
	)
	logging.Info("node PocOrchestrator orchestrator initialized", types.PoC, "nodePocOrchestrator", nodePocOrchestrator)

	tendermintClient := cosmosclient.TendermintClient{
		ChainNodeUrl: config.GetConfig().ChainNode.Url,
	}
	// FIXME: What context to pass?
	ctx := context.Background()
	training.NewAssigner(recorder, &tendermintClient, ctx)
	trainingExecutor := training.NewExecutor(ctx, nodeBroker, recorder)

	validator := validation.NewInferenceValidator(nodeBroker, config, recorder)
	listener := event_listener.NewEventListener(config, nodePocOrchestrator, nodeBroker, validator, *recorder)
	// TODO: propagate trainingExecutor
	go listener.Start(context.Background())

	// TODO: propagagte trainingExecutor
	s := server.NewServer(nodeBroker, config, validator, recorder)
	s.Start()
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
