package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmosclient "decentralized-api/cosmosclient"
	"encoding/json"
	"fmt"
	"golang.org/x/exp/slog"
	"os"
	"strconv"
	"time"
)

func main() {

	if len(os.Args) >= 2 && os.Args[1] == "status" {
		apiconfig.ReadConfig(false)
		returnStatus()
	}
	if len(os.Args) >= 2 && os.Args[1] == "pre-upgrade" {
		os.Exit(1)
	}
	config := apiconfig.ReadConfig(true)

	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", 5, 5*time.Second, config)
	if err != nil {
		panic(err)
	}
	slog.Info("Starting decentralized API, v2")
	nodeBroker := broker.NewBroker()

	go func() {
		StartEventListener(nodeBroker, *recorder, config)
	}()

	StartInferenceServerWrapper(nodeBroker, recorder, config)
}

func returnStatus() {
	height := apiconfig.GetHeight()
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
