package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmosclient "decentralized-api/cosmosclient"
	"time"
)

func main() {
	config := apiconfig.ReadConfig()
	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", 5, 5*time.Second, config)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker()

	go func() {
		StartEventListener(nodeBroker, *recorder, config)
	}()

	StartInferenceServerWrapper(nodeBroker, *recorder, config)
}
