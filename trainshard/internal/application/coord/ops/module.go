package ops

import (
	"context"
	"io"

	"trainshard/internal/application/coord/ops/cli"
	usecases "trainshard/internal/application/coord/ops/use_cases"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type Deps struct {
	Chain   shard.ChainReader
	Hosts   run.HostCommands
	Streams run.HostStreams
	Reports run.HostReports
	Clock   ports.Clock
}

type Module struct {
	commands *cli.Commands
}

func New(cfg Config, deps Deps, out io.Writer, in io.Reader) *Module {
	uc := cli.UseCases{
		Deploy:    usecases.NewDeployUseCase(deps.Chain, deps.Hosts),
		Start:     usecases.NewStartUseCase(deps.Chain, deps.Hosts),
		Stop:      usecases.NewStopUseCase(deps.Chain, deps.Hosts),
		Status:    usecases.NewStatusUseCase(deps.Chain, deps.Hosts),
		Report:    usecases.NewCollectReportUseCase(deps.Chain, deps.Reports),
		Artifacts: usecases.NewCollectArtifactsUseCase(deps.Reports),
		Logs:      usecases.NewStreamLogsUseCase(deps.Streams),
		Shell:     usecases.NewOpenShellUseCase(deps.Streams),
	}
	return &Module{commands: cli.New(uc, deps.Clock, cfg.Timeout, out, in)}
}

func (m *Module) Register(commands map[string]func(context.Context, []string) error) {
	m.commands.Register(commands)
}
