package main

import (
	"context"
	"decentralized-api/broker"
	cosmos_client "decentralized-api/cosmos-client"
	"decentralized-api/dapi_config"
	"time"
)

func main() {
	config := dapi_config.ReadConfig()
	recorder, err := cosmos_client.NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", 5, 5*time.Second, config)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker()

	go func() {
		StartEventListener(nodeBroker, *recorder, config)
	}()

	StartInferenceServerWrapper(nodeBroker, *recorder, config)
}
