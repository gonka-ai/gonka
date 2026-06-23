package runtimeconfig

import (
	"context"
	"errors"
	"log/slog"
	"time"

	devshardpkg "devshard"
)

const (
	defaultGRPCStaleSeconds     = 90 * time.Second
	defaultGRPCReprobeSeconds   = 300 * time.Second
	defaultFailbackProbes       = 2
	defaultAdaptiveProbeTimeout = 3 * time.Second
	defaultStaleCheckInterval   = 5 * time.Second

	// Active source labels returned by AdaptiveProvider.ActiveSource().
	SourceActiveGRPC  = "grpc"
	SourceActiveChain = "chain"
)

// Default tuning for devshardd env fallbacks (see DEVSHARDD_PARAMS_*).
func DefaultGRPCStaleSeconds() time.Duration     { return defaultGRPCStaleSeconds }
func DefaultGRPCReprobeSeconds() time.Duration   { return defaultGRPCReprobeSeconds }
func DefaultFailbackProbes() int                 { return defaultFailbackProbes }
func DefaultAdaptiveProbeTimeout() time.Duration { return defaultAdaptiveProbeTimeout }
func DefaultStaleCheckInterval() time.Duration   { return defaultStaleCheckInterval }

// AdaptiveConfig configures the adaptive runtime-params supervisor (auto mode).
type AdaptiveConfig struct {
	GRPC Config
	Chain ChainConfig

	// GRPCStale is how long the gRPC runner may go without a successful apply
	// while recording poll errors before failing over to chain.
	GRPCStale time.Duration

	// GRPCReprobe is how often to probe GetRuntimeConfig while on chain.
	GRPCReprobe time.Duration

	// FailbackProbes is the number of consecutive successful gRPC probes
	// required before leaving chain for long-poll.
	FailbackProbes int

	// ProbeTimeout bounds boot and reprobe GetRuntimeConfig calls.
	ProbeTimeout time.Duration

	// StaleCheckInterval is how often the supervisor re-evaluates the stale
	// window while on gRPC.
	StaleCheckInterval time.Duration

	Availability *devshardpkg.AvailabilityTracker
	Defaults     Snapshot
	Log          *slog.Logger
	Clock        Clock
}

func (c *AdaptiveConfig) applyDefaults() error {
	if err := c.GRPC.applyDefaults(); err != nil {
		return err
	}
	if err := c.Chain.applyDefaults(); err != nil {
		return err
	}
	if c.GRPCStale <= 0 {
		c.GRPCStale = defaultGRPCStaleSeconds
	}
	if c.GRPCReprobe <= 0 {
		c.GRPCReprobe = defaultGRPCReprobeSeconds
	}
	if c.FailbackProbes <= 0 {
		c.FailbackProbes = defaultFailbackProbes
	}
	if c.ProbeTimeout <= 0 {
		c.ProbeTimeout = defaultAdaptiveProbeTimeout
	}
	if c.StaleCheckInterval <= 0 {
		c.StaleCheckInterval = defaultStaleCheckInterval
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
	// Chain and gRPC share the same availability tracker and defaults via base.
	c.GRPC.Availability = c.Availability
	c.GRPC.Defaults = c.Defaults
	c.Chain.Availability = c.Availability
	c.Chain.Defaults = c.Defaults
	if c.GRPC.Log == nil {
		c.GRPC.Log = c.Log
	}
	if c.Chain.Log == nil {
		c.Chain.Log = c.Log
	}
	if c.GRPC.Clock == nil {
		c.GRPC.Clock = c.Clock
	}
	if c.Chain.Clock == nil {
		c.Chain.Clock = c.Clock
	}
	return nil
}

// AdaptiveProvider is a Provider that switches between gRPC long-poll and
// direct chain polling under supervisor control.
type AdaptiveProvider interface {
	Provider
	ActiveSource() string
	// Wait blocks until the supervisor goroutine exits (after parent ctx cancel).
	Wait()
}

// NewAdaptive starts the adaptive supervisor. Snapshot/epoch semantics match
// the gRPC and chain providers; only the active feed changes at runtime.
func NewAdaptive(ctx context.Context, cfg AdaptiveConfig) (AdaptiveProvider, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	if cfg.Chain.Fetcher == nil {
		return nil, errors.New("runtimeconfig: adaptive Chain.Fetcher is required")
	}
	if cfg.GRPC.Client == nil {
		return nil, errors.New("runtimeconfig: adaptive GRPC.Client is required")
	}

	p := &adaptiveProvider{
		baseProvider: newBase(cfg.Log, cfg.Availability, cfg.Defaults),
		cfg:          cfg,
		events:       make(chan runnerEvent, 16),
		done:         make(chan struct{}),
	}
	go p.supervisor(ctx)
	return p, nil
}
