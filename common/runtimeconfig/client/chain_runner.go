package client

import "context"

// chainRunner polls chain params on an interval and applies snapshots to a
// shared baseProvider.
type chainRunner struct {
	base *baseProvider
	cfg  ChainConfig
}

func newChainRunner(base *baseProvider, cfg ChainConfig) *chainRunner {
	return &chainRunner{base: base, cfg: cfg}
}

func (r *chainRunner) refresh(ctx context.Context) {
	snap, err := r.cfg.Fetcher.FetchSnapshot(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		r.cfg.Log.Warn("chain runtime config: fetch failed; keeping previous snapshot", "err", err)
		return
	}
	if snap.LogprobsMode == "" {
		snap.LogprobsMode = defaultLogprobsMode
	}
	if snap.ServedAt.IsZero() {
		snap.ServedAt = r.cfg.Clock.Now()
	}
	r.cfg.Log.Debug("chain runtime config: snapshot fetched",
		"paramsBlockHeight", snap.ParamsBlockHeight,
		"epochID", snap.CurrentEpochID,
		"devshardRequestsEnabled", snap.DevshardRequestsEnabled,
	)
	r.base.apply(snap)
}

func (r *chainRunner) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.cfg.Clock.After(r.cfg.RefreshInterval):
			r.refresh(ctx)
		}
	}
}
