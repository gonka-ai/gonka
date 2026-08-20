package run

import (
	"context"
	"log/slog"
	"net/http"

	"trainshard/internal/application/hostd/run/api"
	usecases "trainshard/internal/application/hostd/run/use_cases"
	"trainshard/internal/application/hostd/run/worker"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type Deps struct {
	Chain        shard.ChainReader
	Reservations run.Reservations
	Submitter    shard.ChainSubmitter
	Watcher      shard.ChainWatcher
	Runs         run.RunStore
	Requests     run.RequestLog
	Store        mesh.Store
	Network      mesh.Network
	Machine      run.Machine
	Clock        ports.Clock
	Log          *slog.Logger
}

type Module struct {
	endpoints  *api.Endpoints
	admin      *api.Admin
	reconciler *worker.Reconciler
}

func New(cfg Config, deps Deps) *Module {
	reconcile := usecases.NewReconcileUseCase(deps.Reservations, deps.Runs, deps.Machine, deps.Clock, cfg.Patience)

	return &Module{
		admin: api.NewAdmin(cfg.Participant, usecases.NewAbortUseCase(deps.Chain, deps.Submitter)),
		endpoints: api.NewEndpoints(cfg.Participant, api.UseCases{
			Deploy:     usecases.NewDeployUseCase(deps.Chain, deps.Runs, deps.Requests, deps.Machine.Containers, reconcile, deps.Clock, cfg.Limits),
			Start:      usecases.NewStartUseCase(deps.Chain, deps.Runs, deps.Requests, deps.Machine.Containers, reconcile, deps.Clock),
			Stop:       usecases.NewStopUseCase(deps.Chain, deps.Runs, deps.Requests, deps.Machine.Containers, reconcile, deps.Clock),
			Status:     usecases.NewStatusUseCase(deps.Chain, deps.Runs, deps.Machine, deps.Clock),
			Report:     usecases.NewReportUseCase(deps.Chain, deps.Runs, deps.Machine),
			Mesh:       usecases.NewApplyMeshUseCase(deps.Chain, deps.Requests, deps.Store, deps.Machine.Control, reconcile, deps.Clock),
			Identities: usecases.NewCollectIdentitiesUseCase(deps.Chain, deps.Store, cfg.Nodes),
			Probe:      usecases.NewProbeMeshUseCase(deps.Chain, deps.Store, deps.Network),
		}),
		reconciler: worker.NewReconciler(cfg.Nodes, reconcile, deps.Watcher, cfg.Interval, deps.Log),
	}
}

func (m *Module) Mount(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	m.endpoints.Mount(mux, guard)
}

func (m *Module) MountAdmin(mux *http.ServeMux) {
	m.admin.Mount(mux)
}

func (m *Module) Run(ctx context.Context) {
	m.reconciler.Run(ctx)
}
