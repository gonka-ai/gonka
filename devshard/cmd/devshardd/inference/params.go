package inference

import (
	"context"
	"log/slog"
	"sync"
	"time"

	chaintypes "github.com/productscience/inference/x/inference/types"
)

// chainParamsProvider queries inference module params and refreshes them in the
// background so long-lived devshardd processes pick up governance changes.
type chainParamsProvider struct {
	mu           sync.Mutex
	logprobsMode string
}

func NewChainParamsProvider(ctx context.Context, recorder PayloadAuthClient) ChainParamsProvider {
	p := &chainParamsProvider{logprobsMode: chaintypes.DefaultLogprobsMode}

	refresh := func() {
		qc := recorder.NewInferenceQueryClient()
		resp, err := qc.Params(ctx, &chaintypes.QueryParamsRequest{})
		if err != nil {
			slog.Warn("failed to query chain params, keeping current values", "error", err)
			return
		}
		mode := resp.Params.ValidationParams.GetLogprobsMode()
		if mode == "" {
			mode = chaintypes.DefaultLogprobsMode
		}
		p.mu.Lock()
		if mode != p.logprobsMode {
			slog.Info("logprobs_mode updated from chain", "old", p.logprobsMode, "new", mode)
			p.logprobsMode = mode
		}
		p.mu.Unlock()
	}

	refresh()

	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				refresh()
			}
		}
	}()

	return p
}

func (p *chainParamsProvider) LogprobsMode() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.logprobsMode
}
