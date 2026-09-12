package shard

import (
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Command struct {
	Shard     vo.ShardID
	Node      vo.NodeRef
	Actor     Actor
	RequestID vo.RequestID
	Deadline  time.Time
}

// CanAsk holds for whoever the shard answers to at all, whether or not it is still running,
// so a retry of a request that already ran is replayed to its own actor and to nobody else
func CanAsk(shardID vo.ShardID, actor Actor, s Shard) error {
	if shardID != s.ID {
		return ErrShardMismatch
	}
	if !actor.AuthorizedFor(s) {
		return ErrNotAuthorized
	}
	return nil
}

func CanObserve(c Command, s Shard, height vo.Height) error {
	if err := CanAsk(c.Shard, c.Actor, s); err != nil {
		return err
	}
	if !s.IsActive(height) {
		return ErrShardClosed
	}
	if !s.Reserves(c.Node) {
		return ErrNodeNotReserved
	}
	return nil
}

func CanApply(c Command, s Shard, now time.Time, height vo.Height) error {
	if err := CanObserve(c, s, height); err != nil {
		return err
	}
	if c.Deadline.Before(now) {
		return ErrDeadlinePassed
	}
	return nil
}

func CanApplyMesh(c Command, s Shard, drained bool, now time.Time, height vo.Height) error {
	if err := CanApply(c, s, now, height); err != nil {
		return err
	}
	if !drained {
		return ErrNodeNotPrepared
	}
	return nil
}
