package run

import (
	"context"

	"trainshard/internal/domain/shared/vo"
)

type Reservation struct {
	Shard     vo.ShardID
	BaseImage vo.ImageDigest
	Active    bool
}

// Reservations is what the chain owes this node, read fresh on every call; it is the run's only
// source of truth for whether a node still serves a shard
type Reservations interface {
	// Reserved returns the shard holding the node, with Active false once that shard is closed or
	// past its expiry height while the chain still lists it; found is false when no shard holds
	// the node, which is not an error. An error means the chain could not be read
	Reserved(ctx context.Context, node vo.NodeRef) (reservation Reservation, found bool, err error)
	// Release asks the chain to hand the reservation back, which drops the node out of the run.
	// A repeat with the same reason is a no-op on chain; nil means the request was accepted, and
	// the release itself shows up in a later Reserved
	Release(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, reason vo.ReleaseReason) error
}
