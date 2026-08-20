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

func CanObserve(c Command, s Shard, height vo.Height) error {
	if c.Shard != s.ID {
		return ErrShardMismatch
	}
	if !s.IsActive(height) {
		return ErrShardClosed
	}
	if !s.Reserves(c.Node) {
		return ErrNodeNotReserved
	}
	if !c.Actor.AuthorizedFor(s) {
		return ErrNotAuthorized
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
