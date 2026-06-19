package runtimeconfig

import "context"

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
	go newGRPCRunner(p.baseProvider, cfg).run(ctx, nil)
	return p, nil
}
