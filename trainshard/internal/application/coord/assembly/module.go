package assembly

import (
	"context"
	"io"
	"time"

	"trainshard/internal/application/coord/assembly/cli"
	usecases "trainshard/internal/application/coord/assembly/use_cases"
	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/ports"
)

type Config struct {
	Poll time.Duration
}

type Deps struct {
	Chain     shard.ChainReader
	Hosts     mesh.Hosts
	Verifier  ports.Verifier
	Submitter shard.ChainSubmitter
	Clock     ports.Clock
}

type Module struct {
	commands *cli.Commands
}

func New(cfg Config, deps Deps, out io.Writer) *Module {
	prepare := usecases.NewPrepareMeshUseCase(deps.Chain, deps.Hosts, deps.Verifier, deps.Submitter, deps.Clock, cfg.Poll)
	return &Module{commands: cli.New(prepare, deps.Clock, out)}
}

func (m *Module) Register(commands map[string]func(context.Context, []string) error) {
	m.commands.Register(commands)
}
