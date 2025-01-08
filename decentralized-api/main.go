package main

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	cosmosclient "decentralized-api/cosmosclient"
	"log/slog"
	"time"
)

func main() {
	config := apiconfig.ReadConfig()
	recorder, err := cosmosclient.NewInferenceCosmosClientWithRetry(context.Background(), "cosmos", 5, 5*time.Second, config)
	if err != nil {
		panic(err)
	}

	nodeBroker := broker.NewBroker()
	nodes := config.Nodes
	for _, node := range nodes {
		loadNodeToBroker(nodeBroker, &node)
	}

	if config.ChainNode.IsGenesis {
		slog.Info("Registering genesis participant")
		// FIXME: don't register if already exists?
		if err := cosmosclient.RegisterGenesisParticipant(recorder, &config, nodeBroker); err != nil {
			slog.Error("Failed to register genesis participant", "error", err)
			return
		}
	}

	go func() {
		StartEventListener(nodeBroker, *recorder, config)
	}()

	StartInferenceServerWrapper(nodeBroker, recorder, config)
}
