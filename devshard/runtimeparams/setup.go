package runtimeparams

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"common/nodemanager/gen"
	devshardpkg "devshard"
	"devshard/runtimeconfig"
)

// Provider is the full runtime-config feed (long-poll or chain poll).
type RuntimeProvider interface {
	SnapshotSource
	LogprobsMode() string
	CurrentEpochID() uint64
	OnEpochChange(runtimeconfig.EpochChangeListener) (cancel func())
}

// Managed owns a background runtime-params provider shared by devshardd and
// devshardctl. Prefer dAPI GetRuntimeConfig long-poll; fall back to chain.
type Managed struct {
	Provider     RuntimeProvider
	Source       string
	ActiveSource func() string
	cancel       context.CancelFunc
}

// BindProvider returns the live operational-params view of the runtime snapshot.
func (m *Managed) BindProvider() Provider {
	if m == nil || m.Provider == nil {
		return nil
	}
	return FromSnapshot(m.Provider)
}

// Close stops the background provider loop.
func (m *Managed) Close() {
	if m == nil {
		return
	}
	if m.cancel != nil {
		m.cancel()
	}
}

// SetupConfig configures NewManaged.
type SetupConfig struct {
	Chain       runtimeconfig.ChainParamsFetcher
	GRPCClient  gen.NodeManagerClient
	Availability *devshardpkg.AvailabilityTracker
	Logger      *slog.Logger
	Env         EnvSettings
}

// NewManaged starts the adaptive (default) or chain-only runtime params provider.
func NewManaged(parent context.Context, cfg SetupConfig) (*Managed, error) {
	if cfg.Chain == nil {
		return nil, fmt.Errorf("runtime params: chain fetcher is required")
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	env := cfg.Env
	if env.Source == "" {
		env = SettingsFromEnv()
	}

	ctx, cancel := context.WithCancel(parent)
	chainCfg := runtimeconfig.ChainConfig{
		Fetcher:         cfg.Chain,
		RefreshInterval: env.ChainRefresh,
		InitialTimeout:  env.ChainInitial,
		Availability:    cfg.Availability,
		Log:             logger,
	}

	switch {
	case env.Source == SourceChain:
		logger.Info("runtime params provider", "source", "chain_poll", "reason", "env_override")
		rc, err := runtimeconfig.NewChain(ctx, chainCfg)
		if err != nil {
			cancel()
			return nil, err
		}
		return &Managed{
			Provider: rc,
			Source:   SourceChain,
			cancel:   cancel,
		}, nil

	case env.Source == SourceGRPC:
		logger.Warn("runtime params provider: grpc source is deprecated; using adaptive prefer-grpc with chain fallback")
	}

	if cfg.GRPCClient == nil {
		logger.Warn("runtime params provider: NodeManager client unavailable; using chain poll only")
		rc, err := runtimeconfig.NewChain(ctx, chainCfg)
		if err != nil {
			cancel()
			return nil, err
		}
		return &Managed{
			Provider: rc,
			Source:   SourceChain,
			cancel:   cancel,
		}, nil
	}

	logger.Info("runtime params provider", "source", "adaptive", "policy", "prefer_grpc_chain_fallback")
	logger.Info("runtime params provider settings (adaptive)",
		"max_wait_seconds", int(env.ServerMaxWait/time.Second),
		"deadline_slack_seconds", int(env.DeadlineSlack/time.Second),
		"chain_refresh_seconds", int(env.ChainRefresh/time.Second),
		"grpc_stale_seconds", int(env.GRPCStale/time.Second),
		"grpc_reprobe_seconds", int(env.GRPCReprobe/time.Second),
		"failback_probes", env.FailbackProbes,
	)

	rc, err := runtimeconfig.NewAdaptive(ctx, runtimeconfig.AdaptiveConfig{
		GRPC: runtimeconfig.Config{
			Client:              cfg.GRPCClient,
			ServerMaxWait:       env.ServerMaxWait,
			ClientDeadlineSlack: env.DeadlineSlack,
			Availability:        cfg.Availability,
			Log:                 logger,
		},
		Chain:              chainCfg,
		GRPCStale:          env.GRPCStale,
		GRPCReprobe:        env.GRPCReprobe,
		FailbackProbes:     env.FailbackProbes,
		ProbeTimeout:       env.ProbeTimeout,
		StaleCheckInterval: env.StaleCheck,
		Availability:       cfg.Availability,
		Log:                logger,
	})
	if err != nil {
		cancel()
		return nil, err
	}

	return &Managed{
		Provider: rc,
		Source:   SourceAdaptive,
		ActiveSource: func() string {
			return rc.ActiveSource()
		},
		cancel: cancel,
	}, nil
}
