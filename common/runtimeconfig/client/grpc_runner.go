package client

import (
	"context"
	"time"

	"common/nodemanager/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// grpcRunner runs the NodeManager long-poll loop against a shared baseProvider.
// When events is non-nil, poll outcomes are reported to the adaptive supervisor.
type grpcRunner struct {
	base *baseProvider
	cfg  Config
}

func newGRPCRunner(base *baseProvider, cfg Config) *grpcRunner {
	return &grpcRunner{base: base, cfg: cfg}
}

func (r *grpcRunner) run(ctx context.Context, events chan<- runnerEvent) {
	var backoff time.Duration
	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-r.cfg.Clock.After(backoff):
			}
		}

		callStart := r.cfg.Clock.Now()
		resp, err := r.pollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if status.Code(err) == codes.Unimplemented {
				r.emitEvent(ctx, events, runnerEvent{kind: runnerEventUnimplemented, err: err})
				if events == nil {
					r.cfg.Log.Warn("runtime_config: GetRuntimeConfig Unimplemented without adaptive supervisor; "+
						"long-poll loop stopping — use NewAdaptive for chain fallback", "err", err)
				}
				return
			}
			r.emitEvent(ctx, events, runnerEvent{kind: runnerEventPollError, err: err})
			backoff = nextBackoff(backoff, r.cfg.ErrorBackoffMin, r.cfg.ErrorBackoffMax)
			r.cfg.Log.Warn("runtime_config: long-poll failed", "err", err, "backoff", backoff)
			continue
		}
		backoff = 0

		if resp.GetUnchanged() {
			cur := r.base.snap.Load()
			clientHeight := int64(0)
			if cur != nil {
				clientHeight = cur.ParamsBlockHeight
			}
			r.cfg.Log.Debug("runtime_config: long-poll unchanged",
				"clientParamsBlockHeight", clientHeight,
				"devshardRequestsEnabled", curDevshardEnabled(cur),
			)
			elapsed := r.cfg.Clock.Since(callStart)
			floor := r.cfg.unchangedRetryFloor()
			if floor > 0 && elapsed < floor {
				r.grpcSleep(ctx, floor-elapsed)
			}
			continue
		}
		if resp.GetConfig() == nil {
			r.cfg.Log.Debug("runtime_config: long-poll response missing config body")
			continue
		}
		cfg := resp.GetConfig()
		r.cfg.Log.Debug("runtime_config: long-poll received config",
			"paramsBlockHeight", cfg.GetParamsBlockHeight(),
			"epochID", cfg.GetCurrentEpochId(),
			"devshardRequestsEnabled", cfg.GetDevshardRequestsEnabled(),
		)
		r.base.apply(SnapshotFromProto(cfg))
		r.emitEvent(ctx, events, runnerEvent{kind: runnerEventApplied})
		if s := r.base.snap.Load(); s != nil && s.ParamsBlockHeight == 0 {
			r.grpcSleep(ctx, r.cfg.ErrorBackoffMin)
		}
	}
}

func (r *grpcRunner) pollOnce(ctx context.Context) (*gen.GetRuntimeConfigResponse, error) {
	cur := r.base.snap.Load()
	clientHeight := int64(0)
	if cur != nil {
		clientHeight = cur.ParamsBlockHeight
	}

	callCtx, cancel := context.WithTimeout(ctx, r.cfg.clientCallDeadline())
	defer cancel()

	return r.cfg.Client.GetRuntimeConfig(callCtx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: clientHeight,
		MaxWaitSeconds:          int32(r.cfg.ServerMaxWait / time.Second),
	})
}

func (r *grpcRunner) emitEvent(ctx context.Context, events chan<- runnerEvent, ev runnerEvent) {
	if events == nil {
		return
	}
	select {
	case events <- ev:
	case <-ctx.Done():
	}
}

func (r *grpcRunner) grpcSleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-r.cfg.Clock.After(d):
	}
}

// probeGRPC issues a single non-blocking GetRuntimeConfig (MaxWaitSeconds=0).
func probeGRPC(ctx context.Context, cfg Config, timeout time.Duration) error {
	if cfg.Client == nil {
		return status.Error(codes.Unavailable, "runtimeconfig: NodeManager client is nil")
	}
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := cfg.Client.GetRuntimeConfig(probeCtx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: 0,
		MaxWaitSeconds:          0,
	})
	return err
}
