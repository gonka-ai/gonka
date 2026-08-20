package usecases

import (
	"time"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type RunCommand struct {
	Shard     vo.ShardID
	RequestID vo.RequestID
	Deadline  time.Time
}

func (c RunCommand) hostCommand(nodes []vo.NodeRef) run.HostCommand {
	return run.HostCommand{Shard: c.Shard, Nodes: nodes, RequestID: c.RequestID, Deadline: c.Deadline}
}

type DeployCommand struct {
	RunCommand
	Run run.RunSpec
}

type StopCommand struct {
	RunCommand
	Grace time.Duration
}

type NodeCommand struct {
	Shard vo.ShardID
	Node  vo.NodeRef
	Since time.Time
	Tail  int
}
