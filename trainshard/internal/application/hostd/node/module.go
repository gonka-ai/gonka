package node

import (
	"context"
	"log/slog"
	"net/http"

	"trainshard/internal/application/hostd/node/api"
	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/application/hostd/node/worker"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type Deps struct {
	Probe     ports.Probe
	GPU       run.GPU
	Chain     shard.ChainReader
	Submitter shard.ChainSubmitter
	Log       *slog.Logger
}

type Module struct {
	endpoints *api.Endpoints
	optIn     *worker.OptIn
}

func New(cfg Config, deps Deps) *Module {
	readiness := usecases.NewEvaluateReadinessUseCase(
		deps.Probe,
		deps.GPU,
		deps.Chain,
		cfg.Version,
		cfg.SupportedVersion,
		cfg.MinFreeDiskBytes,
	)

	return &Module{
		endpoints: api.NewEndpoints(cfg.Nodes, cfg.Version, readiness),
		optIn: worker.NewOptIn(
			cfg.Nodes,
			usecases.NewRefreshOptInUseCase(readiness, deps.Submitter, cfg.OptInTTL),
			cfg.RefreshInterval,
			deps.Log,
		),
	}
}

func (m *Module) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	m.endpoints.Mount(mux, guard)
}

func (m *Module) Run(ctx context.Context) {
	m.optIn.Run(ctx)
}
