package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/internal/event_listener"
	"decentralized-api/internal/server"
	"decentralized-api/logging"
	"decentralized-api/participant_registration"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"strconv"
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
		config.GetConfig(),
	)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker(recorder)
	nodes := config.GetConfig().Nodes
	for _, node := range nodes {
		server.LoadNodeToBroker(nodeBroker, &node)
	}

	params, err := event_listener.GetParams(context.Background(), *recorder)
	if err != nil {
		slog.Error("Failed to get params", "error", err)
		return
	}

	if err := participant_registration.RegisterParticipantIfNeeded(recorder, config, nodeBroker); err != nil {
		slog.Error("Failed to register participant", "error", err)
		return
	}

	go func() {
		event_listener.StartEventListener(nodeBroker, *recorder, config, &params.Params)
	}()

	server.StartInferenceServerWrapper(nodeBroker, recorder, config)
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
