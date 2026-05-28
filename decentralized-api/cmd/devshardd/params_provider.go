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
	"devshard/nodemanager/gen"
	"devshard/runtimeconfig"
	devshardstorage "devshard/storage"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Source selectors for newParamsProvider.
const (
	paramsSourceAuto  = "auto"
	paramsSourceGRPC  = "grpc"
	paramsSourceChain = "chain"

	// fallbackProbeTimeout bounds the one-shot GetRuntimeConfig probe used by
	// the "auto" selector. Short so misconfigured deployments fail fast; long
	// enough to absorb a single TCP handshake + first byte.
	fallbackProbeTimeout = 3 * time.Second
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
	// Source is "grpc" or "chain"; surfaced for tests / structured logs.
	Source string
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

func paramsSourceFromEnv() string {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("DEVSHARDD_PARAMS_SOURCE")))
	switch v {
	case "":
		return paramsSourceAuto
	case paramsSourceAuto, paramsSourceGRPC, paramsSourceChain:
		return v
	default:
		// Unknown value: log and fall back to auto so a typo does not pin the
		// host to one provider silently.
		slog.Warn("invalid DEVSHARDD_PARAMS_SOURCE; using auto", "got", v)
		return paramsSourceAuto
	}
}

// newParamsProvider selects between the dapi NodeManager long-poll
// (runtimeconfig.New) and the direct-chain poll fallback
// (runtimeconfig.NewChain). Selection is controlled by DEVSHARDD_PARAMS_SOURCE:
//
//	auto (default) — probe NodeManager.GetRuntimeConfig once; on
//	  codes.Unimplemented use the chain provider, otherwise use the grpc provider.
//	grpc — always use the dapi long-poll provider.
//	chain — always use the chain-poll provider.
//
// `recorder` is required when the resolved source is "chain" (it provides the
// inferencetypes.QueryClient). When source is "grpc" `recorder` may be nil
// (preserves existing test ergonomics).
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
		logger.Info("runtime params provider", "source", "dapi_grpc", "reason", "env_override")
		return newGRPCParamsResult(ctx, mlClient, availability, logger)
	}

	// auto
	if probeUnimplemented(ctx, mlClient) {
		logger.Warn("runtime params provider: dapi NodeManager.GetRuntimeConfig unimplemented; "+
			"falling back to direct chain polling",
			"source", "chain_poll")
		return newChainParamsResult(ctx, recorder, availability, logger)
	}
	logger.Info("runtime params provider", "source", "dapi_grpc", "reason", "probe_ok_or_transient")
	return newGRPCParamsResult(ctx, mlClient, availability, logger)
}

// probeUnimplemented issues a single non-blocking GetRuntimeConfig
// (MaxWaitSeconds=0 ⇒ immediate reply per nodemanager.proto wire contract).
// Returns true ONLY for codes.Unimplemented; any other error (Unavailable,
// DeadlineExceeded, …) is treated as transient and we keep the long-poll path
// so devshardd self-heals once dapi is reachable again.
func probeUnimplemented(ctx context.Context, mlClient *mlnodeclient.Client) bool {
	if mlClient == nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, fallbackProbeTimeout)
	defer cancel()
	_, err := mlClient.NodeManagerClient().GetRuntimeConfig(probeCtx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 0,
		MaxWaitSeconds:          0,
	})
	return status.Code(err) == codes.Unimplemented
}

func newGRPCParamsResult(
	ctx context.Context,
	mlClient *mlnodeclient.Client,
	availability *devshardpkg.AvailabilityTracker,
	logger *slog.Logger,
) (*paramsProviderResult, error) {
	logger = normalizeLogger(logger)
	if mlClient == nil {
		return nil, fmt.Errorf("runtime params provider (grpc): NodeManager client is required")
	}
	serverMaxWait, deadlineSlack := runtimeConfigSettingsFromEnv()
	logger.Info("runtime params provider settings (grpc)",
		"max_wait_seconds", int(serverMaxWait/time.Second),
		"deadline_slack_seconds", int(deadlineSlack/time.Second),
	)

	rc, err := runtimeconfig.New(ctx, runtimeconfig.Config{
		Client:              mlClient.NodeManagerClient(),
		ServerMaxWait:       serverMaxWait,
		ClientDeadlineSlack: deadlineSlack,
		Availability:        availability,
		Log:                 logger,
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
		Source: paramsSourceGRPC,
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
		RegisterEpochPrune: func(store *devshardstorage.ManagedStorage) (cancel func()) {
			return rc.OnEpochChange(func(_, _ uint64) {
				store.PruneOnceAsync(ctx)
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
