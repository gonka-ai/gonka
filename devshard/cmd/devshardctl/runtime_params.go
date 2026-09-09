package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"

	"common/chain"
	mlnodeclient "common/nodemanager"
	"devshard/bridge"
	"devshard/runtimeconfig"
	"devshard/runtimeparams"
)

func initGatewayRuntimeParams(ctx context.Context, chainClient *chain.Client) (*runtimeparams.Managed, func(), error) {
	env := runtimeparams.SettingsFromEnv()

	var nmClient *mlnodeclient.Client
	var nmClose func()
	if env.NodeManagerAddr != "" {
		client, err := mlnodeclient.NewClient(env.NodeManagerAddr)
		if err != nil {
			slog.Warn("runtime params: nodemanager dial failed; chain fallback only",
				"addr", env.NodeManagerAddr, "err", err)
		} else {
			nmClient = client
			nmClose = func() { _ = client.Close() }
		}
	}

	chainFetcher, err := newGatewayChainFetcher(chainClient)
	if err != nil {
		if nmClose != nil {
			nmClose()
		}
		return nil, nil, err
	}

	setup := runtimeparams.SetupConfig{
		Chain:  chainFetcher,
		Logger: slog.Default(),
		Env:    env,
	}
	if nmClient != nil {
		setup.GRPCClient = nmClient.NodeManagerClient()
	}

	managed, err := runtimeparams.NewManaged(ctx, setup)
	if err != nil {
		if nmClose != nil {
			nmClose()
		}
		return nil, nil, err
	}

	closeAll := func() {
		managed.Close()
		if nmClose != nil {
			nmClose()
		}
	}
	return managed, closeAll, nil
}

func newGatewayChainFetcher(chainClient *chain.Client) (runtimeconfig.ChainParamsFetcher, error) {
	if chainClient == nil {
		return nil, fmt.Errorf("chain gRPC client is required")
	}
	slog.Info("runtime params chain fetcher", "transport", "grpc", "shared_client", true)
	return runtimeparams.NewGRPCChainFetcher(chainClient), nil
}

func mustInitGatewayRuntimeParams(ctx context.Context, chainClient *chain.Client) (*runtimeparams.Managed, func()) {
	managed, closeFn, err := initGatewayRuntimeParams(ctx, chainClient)
	if err != nil {
		log.Fatalf("runtime params provider: %v", err)
	}
	if managed == nil {
		log.Fatal("runtime params provider: nil setup")
	}
	return managed, closeFn
}

type runtimeBuildDeps struct {
	bridge       bridge.MainnetBridge
	chainClient  *chain.Client
	defaultModel string
	perf         *PerfTracker
}

func (d runtimeBuildDeps) validate() error {
	if d.bridge == nil || d.chainClient == nil {
		return fmt.Errorf("chain gRPC client is required")
	}
	return nil
}
