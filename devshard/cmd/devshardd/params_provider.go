package main

import (
	"context"
	"fmt"
	"log/slog"

	"common/chain"
	mlnodeclient "common/nodemanager"
	devshardpkg "devshard"
	"devshard/runtimeparams"
)

type epochParamsProvider = runtimeparams.RuntimeProvider

type paramsProviderResult struct {
	Provider     epochParamsProvider
	Source       string
	ActiveSource func() string
	close        func()
}

func newParamsProvider(
	ctx context.Context,
	chainClient *chain.Client,
	mlClient *mlnodeclient.Client,
	availability *devshardpkg.AvailabilityTracker,
	logger *slog.Logger,
) (*paramsProviderResult, error) {
	if mlClient == nil {
		return nil, fmt.Errorf("runtime params provider: NodeManager client is required")
	}
	if chainClient == nil {
		return nil, fmt.Errorf("runtime params provider: chain client is required")
	}

	managed, err := runtimeparams.NewManaged(ctx, runtimeparams.SetupConfig{
		Chain:        runtimeparams.NewGRPCChainFetcher(chainClient),
		GRPCClient:   mlClient.NodeManagerClient(),
		Availability: availability,
		Logger:       logger,
		Env:          runtimeparams.SettingsFromEnv(),
	})
	if err != nil {
		return nil, err
	}

	return &paramsProviderResult{
		Provider: managed.Provider,
		Source:   managed.Source,
		ActiveSource: func() string {
			if managed.ActiveSource != nil {
				return managed.ActiveSource()
			}
			return managed.Source
		},
		close: managed.Close,
	}, nil
}
