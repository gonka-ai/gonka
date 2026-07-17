package main

import (
	"context"
	"fmt"
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

// Source selectors for newParamsProvider.
const (
	paramsSourceAuto     = "auto"
	paramsSourceGRPC     = "grpc" // deprecated alias: same as auto (adaptive)
	paramsSourceChain    = "chain"
	paramsSourceAdaptive = "adaptive"
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
	RegisterEpochPrune func(store *devshardstorage.ManagedStorage, evict func(cutoff uint64)) (cancel func())
	// Source is "adaptive" or "chain"; surfaced for tests / structured logs.
	Source string
	// ActiveSource reports grpc|chain when Source is adaptive; nil for chain-only.
	ActiveSource func() string
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

func chainParamsSettingsFromEnv() (refreshInterval, initialTimeout time.Duration) {
	refreshInterval = 60 * time.Second
	initialTimeout = 5 * time.Second

	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_CHAIN_REFRESH_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			refreshInterval = time.Duration(n) * time.Second
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_CHAIN_INITIAL_TIMEOUT_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			initialTimeout = time.Duration(n) * time.Second
		}
	}
	return refreshInterval, initialTimeout
}

func adaptiveSettingsFromEnv() (grpcStale, grpcReprobe, probeTimeout, staleCheck time.Duration, failbackProbes int) {
	grpcStale = runtimeconfig.DefaultGRPCStaleSeconds()
	grpcReprobe = runtimeconfig.DefaultGRPCReprobeSeconds()
	probeTimeout = runtimeconfig.DefaultAdaptiveProbeTimeout()
	staleCheck = runtimeconfig.DefaultStaleCheckInterval()
	failbackProbes = runtimeconfig.DefaultFailbackProbes()

	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_GRPC_STALE_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			grpcStale = time.Duration(n) * time.Second
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_GRPC_REPROBE_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			grpcReprobe = time.Duration(n) * time.Second
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_GRPC_FAILBACK_PROBES")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			failbackProbes = n
		}
	}
	if s := strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_GRPC_PROBE_TIMEOUT_SECONDS")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			probeTimeout = time.Duration(n) * time.Second
		}
	}
	return grpcStale, grpcReprobe, probeTimeout, staleCheck, failbackProbes
}

func paramsSourceFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_SOURCE")))
	switch v {
	case "":
		return paramsSourceAuto
	case paramsSourceAuto, paramsSourceGRPC, paramsSourceChain:
		return v
	default:
		slog.Warn("invalid DEVSHARDD_PARAMS_SOURCE; using auto", "got", v)
		return paramsSourceAuto
	}
}

// newParamsProvider returns a runtime-params provider for devshardd.
//
// Default (auto) and deprecated grpc: prefer dapi GetRuntimeConfig long-poll;
// fall back to direct chain polling when gRPC is Unimplemented, unavailable, or
// stale (runtimeconfig.NewAdaptive).
//
// chain: chain poll only (debug / forced chain); no gRPC attempts.
//
// `recorder` and `mlClient` are both required except for chain-only override.
func newParamsProvider(
	ctx context.Context,
	recorder internaldevshard.InferenceQueryClientProvider,
	mlClient *mlnodeclient.Client,
	availability *devshardpkg.AvailabilityTracker,
	logger *slog.Logger,
) (*paramsProviderResult, error) {
	logger = normalizeLogger(logger)
	source := paramsSourceFromEnv()

	switch source {
	case paramsSourceChain:
		logger.Info("runtime params provider", "source", "chain_poll", "reason", "env_override")
		return newChainParamsResult(ctx, recorder, availability, logger)
	case paramsSourceGRPC:
		logger.Warn("runtime params provider: DEVSHARDD_PARAMS_SOURCE=grpc is deprecated; " +
			"using prefer-grpc with chain fallback (same as auto)")
	}

	return newAdaptiveParamsResult(ctx, recorder, mlClient, availability, logger)
}

func newAdaptiveParamsResult(
	ctx context.Context,
	recorder internaldevshard.InferenceQueryClientProvider,
	mlClient *mlnodeclient.Client,
	availability *devshardpkg.AvailabilityTracker,
	logger *slog.Logger,
) (*paramsProviderResult, error) {
	logger = normalizeLogger(logger)
	if recorder == nil {
		return nil, fmt.Errorf("runtime params provider (adaptive): InferenceQueryClientProvider is required")
	}
	if mlClient == nil {
		return nil, fmt.Errorf("runtime params provider (adaptive): NodeManager client is required")
	}

	serverMaxWait, deadlineSlack := runtimeConfigSettingsFromEnv()
	refresh, initial := chainParamsSettingsFromEnv()
	grpcStale, grpcReprobe, probeTimeout, staleCheck, failbackProbes := adaptiveSettingsFromEnv()

	logger.Info("runtime params provider", "source", "adaptive", "policy", "prefer_grpc_chain_fallback")
	logger.Info("runtime params provider settings (adaptive)",
		"max_wait_seconds", int(serverMaxWait/time.Second),
		"deadline_slack_seconds", int(deadlineSlack/time.Second),
		"chain_refresh_seconds", int(refresh/time.Second),
		"grpc_stale_seconds", int(grpcStale/time.Second),
		"grpc_reprobe_seconds", int(grpcReprobe/time.Second),
		"failback_probes", failbackProbes,
	)

	rc, err := runtimeconfig.NewAdaptive(ctx, runtimeconfig.AdaptiveConfig{
		GRPC: runtimeconfig.Config{
			Client:              mlClient.NodeManagerClient(),
			ServerMaxWait:       serverMaxWait,
			ClientDeadlineSlack: deadlineSlack,
			Availability:        availability,
			Log:                 logger,
		},
		Chain: runtimeconfig.ChainConfig{
			Fetcher:         internaldevshard.NewChainParamsFetcher(recorder),
			RefreshInterval: refresh,
			InitialTimeout:  initial,
			Availability:    availability,
			Log:             logger,
		},
		GRPCStale:          grpcStale,
		GRPCReprobe:        grpcReprobe,
		FailbackProbes:     failbackProbes,
		ProbeTimeout:       probeTimeout,
		StaleCheckInterval: staleCheck,
		Availability:       availability,
		Log:                logger,
	})
	if err != nil {
		return nil, err
	}

	return &paramsProviderResult{
		Provider: rc,
		RegisterEpochPrune: func(store *devshardstorage.ManagedStorage, evict func(cutoff uint64)) (cancel func()) {
			return rc.OnEpochChange(func(_, _ uint64) {
				store.PruneOnceAsync(ctx)
				if evict != nil {
					evict(store.PruneCutoff())
				}
			})
		},
		Source: paramsSourceAdaptive,
		ActiveSource: func() string {
			return rc.ActiveSource()
		},
	}, nil
}

func newChainParamsResult(
	ctx context.Context,
	recorder internaldevshard.InferenceQueryClientProvider,
	availability *devshardpkg.AvailabilityTracker,
	logger *slog.Logger,
) (*paramsProviderResult, error) {
	logger = normalizeLogger(logger)
	if recorder == nil {
		return nil, fmt.Errorf("runtime params provider (chain): InferenceQueryClientProvider is required")
	}
	refresh, initial := chainParamsSettingsFromEnv()
	logger.Info("runtime params provider settings (chain)",
		"refresh_seconds", int(refresh/time.Second),
		"initial_timeout_seconds", int(initial/time.Second),
	)

	rc, err := runtimeconfig.NewChain(ctx, runtimeconfig.ChainConfig{
		Fetcher:         internaldevshard.NewChainParamsFetcher(recorder),
		RefreshInterval: refresh,
		InitialTimeout:  initial,
		Availability:    availability,
		Log:             logger,
	})
	if err != nil {
		return nil, err
	}

	return &paramsProviderResult{
		Provider: rc,
		RegisterEpochPrune: func(store *devshardstorage.ManagedStorage, evict func(cutoff uint64)) (cancel func()) {
			return rc.OnEpochChange(func(_, _ uint64) {
				store.PruneOnceAsync(ctx)
				if evict != nil {
					evict(store.PruneCutoff())
				}
			})
		},
		Source: paramsSourceChain,
	}, nil
}

func normalizeLogger(logger *slog.Logger) *slog.Logger {
	if logger != nil {
		return logger
	}
	return slog.Default()
}
