package run

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

// Reservation all a run needs to know about the shard it belongs to
type Reservation struct {
	Shard     vo.ShardID
	BaseImage vo.ImageDigest
	Active    bool
}

// Reservations what the chain owes this node
type Reservations interface {
	// Reserved returns the node's reservation, or none if it owes nothing
	Reserved(ctx context.Context, node vo.NodeRef) (reservation Reservation, found bool, err error)
	// Release hands the reservation back, which drops the node out of the run
	Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error
}
