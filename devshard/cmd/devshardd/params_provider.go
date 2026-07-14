package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	mlnodeclient "common/nodemanager"
	"common/chain"
	devshardpkg "devshard"
	"devshard/runtimeparams"
	devshardstorage "devshard/storage"
)

type epochParamsProvider = runtimeparams.RuntimeProvider

type paramsProviderResult struct {
	Provider           epochParamsProvider
	RegisterEpochPrune func(store *devshardstorage.ManagedStorage) (cancel func())
	Source             string
	ActiveSource       func() string
	close              func()
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

	result := &paramsProviderResult{
		Provider: managed.Provider,
		Source:   managed.Source,
		ActiveSource: func() string {
			if managed.ActiveSource != nil {
				return managed.ActiveSource()
			}
			return managed.Source
		},
		close: managed.Close,
	}
	result.RegisterEpochPrune = func(store *devshardstorage.ManagedStorage) (cancel func()) {
		return managed.Provider.OnEpochChange(func(_, _ uint64) {
			store.PruneOnceAsync(ctx)
		})
	}
	return result, nil
}

func normalizeLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}

func hostEventsSettingsFromEnv() (serverMaxWait, deadlineSlack time.Duration) {
	serverMaxWait = 60 * time.Second
	deadlineSlack = 5 * time.Second
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_HOST_EVENTS_MAX_WAIT_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			serverMaxWait = time.Duration(n) * time.Second
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_HOST_EVENTS_CLIENT_DEADLINE_SLACK_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			deadlineSlack = time.Duration(n) * time.Second
		}
	}
	return serverMaxWait, deadlineSlack
}
