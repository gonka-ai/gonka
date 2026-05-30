package runtimeconfig

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	devshardpkg "devshard"
	"devshard/nodemanager/gen"
)

type grpcProvider struct {
	cfg Config

	snap atomic.Pointer[Snapshot]

	listenersMu sync.Mutex
	listeners   map[uint64]EpochChangeListener
	nextID      uint64
}

// New starts the background long-poll loop. Callers see Defaults until the first
// successful fetch; the loop runs asynchronously.
func New(ctx context.Context, cfg Config) (Provider, error) {
	if err := cfg.applyDefaults(); err != nil {
		return nil, err
	}
	p := &grpcProvider{
		cfg:       cfg,
		listeners: make(map[uint64]EpochChangeListener),
	}
	defaults := cfg.Defaults
	p.snap.Store(&defaults)
	go p.run(ctx)
	return p, nil
}

func (p *grpcProvider) Snapshot() Snapshot {
	s := p.snap.Load()
	if s == nil {
		return p.cfg.Defaults
	}
	return *s
}

func (p *grpcProvider) LogprobsMode() string   { return p.Snapshot().LogprobsMode }
func (p *grpcProvider) CurrentEpochID() uint64 { return p.Snapshot().CurrentEpochID }

func (p *grpcProvider) Availability() devshardpkg.AvailabilityStatus {
	s := p.Snapshot()
	var ts int64
	if !s.ServedAt.IsZero() {
		ts = s.ServedAt.Unix()
	}
	return devshardpkg.AvailabilityStatus{
		Enabled: s.DevshardRequestsEnabled,
		Time:    ts,
		EpochID: s.CurrentEpochID,
	}
}

func (p *grpcProvider) OnEpochChange(fn EpochChangeListener) (cancel func()) {
	p.listenersMu.Lock()
	defer p.listenersMu.Unlock()
	id := p.nextID
	p.nextID++
	p.listeners[id] = fn
	return func() {
		p.listenersMu.Lock()
		delete(p.listeners, id)
		p.listenersMu.Unlock()
	}
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

func curDevshardEnabled(s *Snapshot) bool {
	if s == nil {
		return false
	}
	return s.DevshardRequestsEnabled
}

func (p *grpcProvider) apply(next Snapshot) {
	prev := p.snap.Load()
	prevEnabled := curDevshardEnabled(prev)
	nextCopy := next
	p.snap.Store(&nextCopy)

	if prev == nil || prevEnabled != next.DevshardRequestsEnabled {
		p.cfg.Log.Info("runtime_config: devshard_requests_enabled applied",
			"previous", prevEnabled,
			"current", next.DevshardRequestsEnabled,
			"paramsBlockHeight", next.ParamsBlockHeight,
			"epochID", next.CurrentEpochID,
		)
	} else {
		p.cfg.Log.Debug("runtime_config: snapshot applied",
			"devshardRequestsEnabled", next.DevshardRequestsEnabled,
			"paramsBlockHeight", next.ParamsBlockHeight,
			"epochID", next.CurrentEpochID,
		)
	}

	if p.cfg.Availability != nil {
		var ts int64
		if !next.ServedAt.IsZero() {
			ts = next.ServedAt.Unix()
		}
		p.cfg.Availability.Record(next.DevshardRequestsEnabled, ts, next.CurrentEpochID)
	}

	if prev != nil && prev.ParamsBlockHeight > 0 && prev.CurrentEpochID != next.CurrentEpochID {
		p.fireEpoch(prev.CurrentEpochID, next.CurrentEpochID)
	}
}

func (p *grpcProvider) fireEpoch(oldE, newE uint64) {
	p.listenersMu.Lock()
	snap := make([]EpochChangeListener, 0, len(p.listeners))
	for _, fn := range p.listeners {
		snap = append(snap, fn)
	}
	p.listenersMu.Unlock()

	for _, fn := range snap {
		fn := fn
		go func() {
			defer func() {
				if r := recover(); r != nil {
					p.cfg.Log.Error("epoch listener panic", "panic", r)
				}
			}()
			fn(oldE, newE)
		}()
	}
}
