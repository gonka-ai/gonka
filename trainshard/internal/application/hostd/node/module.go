package node

import (
	"context"
	"log/slog"
	"net/http"

	"trainshard/internal/application/hostd/node/api"
	usecases "trainshard/internal/application/hostd/node/use_cases"
	"trainshard/internal/application/hostd/node/worker"
	"trainshard/internal/domain/readiness"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type Deps struct {
	Probe     ports.Probe
	GPU       run.GPU
	Chain     shard.ChainReader
	Submitter shard.ChainSubmitter
	Clock     ports.Clock
	Log       *slog.Logger
}

type Module struct {
	endpoints *api.Endpoints
	optIn     *worker.OptIn
}

func New(cfg Config, deps Deps) *Module {
	spec := readiness.Spec{
		Version:          cfg.Version,
		SupportedVersion: cfg.SupportedVersion,
		MinFreeDiskBytes: cfg.MinFreeDiskBytes,
	}
	prover := readiness.NewProver(deps.Probe, deps.Clock)

	return &Module{
		endpoints: api.NewEndpoints(cfg.Nodes, cfg.Version,
			usecases.NewEvaluateReadinessUseCase(prover, deps.GPU, deps.Chain, spec)),
		optIn: worker.NewOptIn(
			cfg.Nodes,
			usecases.NewRefreshOptInUseCase(prover, deps.GPU, deps.Chain, deps.Submitter, spec, cfg.OptInTTL),
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
