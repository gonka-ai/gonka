package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "status" {
		config, err := apiconfig.LoadDefaultConfigManager()
		if err != nil {
			log.Fatalf("Error loading config: %v", err)
		}
		returnStatus(config)
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
		5,
		5*time.Second,
		config.GetConfig(),
	)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker()

	go func() {
		StartEventListener(nodeBroker, *recorder, config)
	}()

	StartInferenceServerWrapper(nodeBroker, recorder, config)
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
