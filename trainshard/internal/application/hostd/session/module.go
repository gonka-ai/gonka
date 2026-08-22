package session

import (
	"net/http"

	"trainshard/internal/application/hostd/session/api"
	usecases "trainshard/internal/application/hostd/session/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
	"trainshard/internal/utils/signedhttp"
)

type Deps struct {
	Chain    shard.ChainReader
	Streams  run.Streams
	Sessions run.SessionLog
	Clock    ports.Clock
}

type Module struct {
	endpoints *api.Endpoints
}

func New(cfg Config, deps Deps) *Module {
	return &Module{
		endpoints: api.NewEndpoints(cfg.Participant, api.UseCases{
			Logs:  usecases.NewStreamLogsUseCase(deps.Chain, deps.Streams),
			Shell: usecases.NewOpenShellUseCase(deps.Chain, deps.Streams, deps.Sessions, deps.Clock),
		}, signedhttp.NewOnce(deps.Clock, cfg.Window)),
	}
}

func (m *Module) Mount(mux *http.ServeMux, boundary func(http.Handler) http.Handler) {
	m.endpoints.Mount(mux, boundary)
}
