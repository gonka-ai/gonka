package usecases

import (
	"time"

	"trainshard/internal/domain/shard"
	"trainshard/internal/domain/shared/vo"
)

type SessionCommand struct {
	Shard vo.ShardID
	Node  vo.NodeRef
	Actor shard.Actor
}

type LogsCommand struct {
	SessionCommand
	Since time.Time
	Tail  int
}

func (c SessionCommand) command() shard.Command {
	return shard.Command{Shard: c.Shard, Node: c.Node, Actor: c.Actor}
}
