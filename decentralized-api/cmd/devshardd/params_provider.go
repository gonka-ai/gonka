package main

import (
	"context"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	internaldevshard "decentralized-api/internal/devshard"

	devshardpkg "devshard"
	mlnodeclient "devshard/mlnode"
	"devshard/runtimeconfig"
	devshardstorage "devshard/storage"
)

// epochParamsProvider supplies logprobs mode, epoch, and runtime snapshot to
// engine, validator, storage, and bind-time grace (ChainBridge defaults).
type epochParamsProvider interface {
	internaldevshard.ChainParamsProvider
	devshardstorage.EpochProvider
	internaldevshard.RuntimeConfigSnapshotSource
}

// paramsProviderResult holds the active provider and optional epoch-prune hook.
type paramsProviderResult struct {
	Provider           epochParamsProvider
	RegisterEpochPrune func(store *devshardstorage.ManagedStorage) (cancel func())
}

func runtimeConfigSettingsFromEnv() (serverMaxWait, deadlineSlack time.Duration) {
	serverMaxWait = 60 * time.Second
	deadlineSlack = 5 * time.Second

	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_RUNTIME_CONFIG_MAX_WAIT_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			serverMaxWait = time.Duration(n) * time.Second
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_RUNTIME_CONFIG_CLIENT_DEADLINE_SLACK_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			deadlineSlack = time.Duration(n) * time.Second
		}
	}
	return serverMaxWait, deadlineSlack
}

func newParamsProvider(
	ctx context.Context,
	_ internaldevshard.PayloadAuthClient,
	mlClient *mlnodeclient.Client,
	availability *devshardpkg.AvailabilityTracker,
) (*paramsProviderResult, error) {
	serverMaxWait, deadlineSlack := runtimeConfigSettingsFromEnv()
	slog.Info("runtime params provider", "source", "dapi_grpc",
		"max_wait_seconds", int(serverMaxWait/time.Second),
		"deadline_slack_seconds", int(deadlineSlack/time.Second),
	)

	rc, err := runtimeconfig.New(ctx, runtimeconfig.Config{
		Client:              mlClient.NodeManagerClient(),
		ServerMaxWait:       serverMaxWait,
		ClientDeadlineSlack: deadlineSlack,
		Availability:        availability,
		Log:                 slog.Default(),
	})
	if err != nil {
		return nil, err
	}

	return &paramsProviderResult{
		Provider: rc,
		RegisterEpochPrune: func(store *devshardstorage.ManagedStorage) (cancel func()) {
			return rc.OnEpochChange(func(_, _ uint64) {
				store.PruneOnceAsync(ctx)
			})
		},
	}, nil
}
