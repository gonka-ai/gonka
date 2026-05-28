package runtimeconfig

import (
	"context"
	"time"

	"devshard/nodemanager/gen"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type grpcProvider struct {
	*baseProvider
	cfg Config
}

// New starts the background long-poll loop. Callers see Defaults until the first
// successful fetch; the loop runs asynchronously.
func New(ctx context.Context, cfg Config) (Provider, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	p := &grpcProvider{
		baseProvider: newBase(cfg.Log, cfg.Availability, cfg.Defaults),
		cfg:          cfg,
	}
	go p.run(ctx)
	return p, nil
}

func (p *grpcProvider) run(ctx context.Context) {
	var backoff time.Duration
	for {
		if backoff > 0 {
			select {
			case <-ctx.Done():
				return
			case <-p.cfg.Clock.After(backoff):
			}
		}

		callStart := p.cfg.Clock.Now()
		resp, err := p.pollOnce(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Unimplemented is terminal: old (gm/microrelease) dapi will never
			// grow this RPC. Spinning would just log forever — operators are
			// expected to either restart devshardd to pick up the chain-poll
			// fallback or upgrade dapi.
			if status.Code(err) == codes.Unimplemented {
				p.cfg.Log.Error("runtime_config: NodeManager.GetRuntimeConfig is Unimplemented; "+
					"long-poll provider exiting — restart devshardd to switch to chain-poll fallback",
					"err", err)
				return
			}
			backoff = nextBackoff(backoff, p.cfg.ErrorBackoffMin, p.cfg.ErrorBackoffMax)
			p.cfg.Log.Warn("runtime_config: long-poll failed", "err", err, "backoff", backoff)
			continue
		}
		backoff = 0

		if resp.GetUnchanged() {
			cur := p.snap.Load()
			clientHeight := int64(0)
			if cur != nil {
				clientHeight = cur.ParamsBlockHeight
			}
			p.cfg.Log.Debug("runtime_config: long-poll unchanged",
				"clientParamsBlockHeight", clientHeight,
				"devshardRequestsEnabled", curDevshardEnabled(cur),
			)
			elapsed := p.cfg.Clock.Since(callStart)
			floor := p.cfg.unchangedRetryFloor()
			if floor > 0 && elapsed < floor {
				p.sleep(ctx, floor-elapsed)
			}
			continue
		}
		if resp.GetConfig() == nil {
			p.cfg.Log.Debug("runtime_config: long-poll response missing config body")
			continue
		}
		cfg := resp.GetConfig()
		p.cfg.Log.Debug("runtime_config: long-poll received config",
			"paramsBlockHeight", cfg.GetParamsBlockHeight(),
			"epochID", cfg.GetCurrentEpochId(),
			"devshardRequestsEnabled", cfg.GetDevshardRequestsEnabled(),
		)
		p.apply(SnapshotFromProto(cfg))
		// dapi returns immediate full config (initial_fetch) while ParamsBlockHeight
		// is still 0. Without a pause the loop would hammer NodeManager until publish.
		// It just means we are waiting while it is initing
		if s := p.snap.Load(); s != nil && s.ParamsBlockHeight == 0 {
			p.sleep(ctx, p.cfg.ErrorBackoffMin)
		}
	}
}

func (p *grpcProvider) pollOnce(ctx context.Context) (*gen.GetRuntimeConfigResponse, error) {
	cur := p.snap.Load()
	clientHeight := int64(0)
	if cur != nil {
		clientHeight = cur.ParamsBlockHeight
	}

	callCtx, cancel := context.WithTimeout(ctx, p.cfg.clientCallDeadline())
	defer cancel()

	return p.cfg.Client.GetRuntimeConfig(callCtx, &gen.GetRuntimeConfigRequest{
		ClientParamsBlockHeight: clientHeight,
		MaxWaitSeconds:          int32(p.cfg.ServerMaxWait / time.Second),
	})
}

func (p *grpcProvider) sleep(ctx context.Context, d time.Duration) {
	if d <= 0 {
		return
	}
	select {
	case <-ctx.Done():
	case <-p.cfg.Clock.After(d):
	}
}
