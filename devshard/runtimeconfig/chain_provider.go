package runtimeconfig

import (
	"context"
	"errors"
	"log/slog"
	"time"

	devshardpkg "devshard"
)

const (
	defaultChainRefreshInterval = 60 * time.Second
	defaultChainInitialTimeout  = 5 * time.Second
)

// ChainParamsFetcher is the chain-query surface the chain-poll provider
// depends on. The adapter that wraps the cosmos inferencetypes.QueryClient
// lives in decentralized-api/internal/devshard so this package stays free
// of inference-chain imports (devshard go.mod intentionally does not depend
// on inference-chain).
type ChainParamsFetcher interface {
	// FetchSnapshot performs one Params + EpochInfo query pair and returns a
	// fully-populated Snapshot. Implementations must NOT block for governance
	// changes; this is a point-in-time read.
	FetchSnapshot(ctx context.Context) (Snapshot, error)
}

// ChainConfig configures the chain-poll runtime config provider used as
// a fallback when dapi's NodeManager.GetRuntimeConfig is unavailable
// (gm/microrelease and older).
type ChainConfig struct {
	// Fetcher reads chain params + epoch info and builds a Snapshot.
	Fetcher ChainParamsFetcher

	// RefreshInterval is how often the background loop calls Fetcher.
	// Defaults to 60s (matches gm/microrelease's chainParamsProvider).
	RefreshInterval time.Duration

	// InitialTimeout bounds the synchronous first refresh at construction.
	// Failure is non-fatal; the snapshot stays at Defaults and the
	// background loop continues to retry.
	InitialTimeout time.Duration

	// Availability is the shared tracker the host gate reads. apply() writes
	// it on every successful refresh.
	Availability *devshardpkg.AvailabilityTracker

	// Defaults seeds the initial Snapshot before the first refresh. Same
	// shape as Config.Defaults (long-poll path).
	Defaults Snapshot

	Log   *slog.Logger
	Clock Clock
}

func (c *ChainConfig) applyDefaults() error {
	if c.Fetcher == nil {
		return errors.New("runtimeconfig: chain Fetcher is required")
	}
	if c.RefreshInterval <= 0 {
		c.RefreshInterval = defaultChainRefreshInterval
	}
	if c.InitialTimeout <= 0 {
		c.InitialTimeout = defaultChainInitialTimeout
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Defaults.LogprobsMode == "" {
		c.Defaults.LogprobsMode = defaultLogprobsMode
	}
	return nil
}

type chainProvider struct {
	*baseProvider
	cfg ChainConfig
}

// NewChain starts the chain-poll provider. The initial fetch is best-effort:
// on failure, Snapshot() returns Defaults until the next refresh succeeds.
// Mirrors gm/microrelease's chainParamsProvider boot path but writes through
// the shared baseProvider so engine/validator/HostManager see identical
// semantics regardless of which provider is feeding them.
func NewChain(ctx context.Context, cfg ChainConfig) (Provider, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	p := &chainProvider{
		baseProvider: newBase(cfg.Log, cfg.Availability, cfg.Defaults),
		cfg:          cfg,
	}
	runner := newChainRunner(p.baseProvider, cfg)

	initCtx, cancel := context.WithTimeout(ctx, cfg.InitialTimeout)
	runner.refresh(initCtx)
	cancel()

	go runner.run(ctx)
	return p, nil
}
