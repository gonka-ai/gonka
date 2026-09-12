package shard

import "trainshard/internal/domain/shared"

var (
	ErrShardUnknown    = shared.New("SHARD_UNKNOWN", shared.ErrNotFound, "chain has no such shard")
	ErrShardMismatch   = shared.New("SHARD_MISMATCH", shared.ErrValidation, "request targets another shard")
	ErrShardClosed     = shared.New("SHARD_CLOSED", shared.ErrConflict, "shard is closed")
	ErrNodeNotReserved = shared.New("NODE_NOT_RESERVED", shared.ErrConflict, "node is not reserved in this shard")
	ErrNotAuthorized   = shared.New("NOT_AUTHORIZED", shared.ErrForbidden, "request is not authorized for this run")
	ErrDeadlinePassed  = shared.New("DEADLINE_PASSED", shared.ErrValidation, "request deadline has passed")
	ErrNodeNotPrepared = shared.New("NODE_NOT_PREPARED", shared.ErrConflict, "node is not prepared")
	ErrReleasePending  = shared.New("RELEASE_PENDING", shared.ErrUnavailable, "chain still reserves a node that was released")
)
