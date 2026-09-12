package usecases

import (
	"time"

	"trainshard/internal/domain/mesh"
	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type NodesCommand struct {
	Shard     vo.ShardID
	Nodes     []vo.NodeRef
	Actor     shard.Actor
	RequestID vo.RequestID
	Deadline  time.Time
}

func (c NodesCommand) request(op run.Op) run.RequestRef {
	return run.RequestRef{Op: op, Shard: c.Shard, Actor: c.Actor.Address, ID: c.RequestID}
}

func (c NodesCommand) forNode(node vo.NodeRef) shard.Command {
	return shard.Command{
		Shard:     c.Shard,
		Node:      node,
		Actor:     c.Actor,
		RequestID: c.RequestID,
		Deadline:  c.Deadline,
	}
}

type DeployCommand struct {
	NodesCommand
	Run run.RunSpec
}

type StopCommand struct {
	NodesCommand
	Grace time.Duration
}

type MeshCommand struct {
	NodesCommand
	Config mesh.Config
}
