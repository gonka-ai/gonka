package run

import (
	"context"
	"io"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// RunStore local run state, keyed by node
type RunStore interface {
	// Load returns state, or none
	Load(ctx context.Context, node vo.NodeRef) (state RunState, found bool, err error)
	// Update applies the change as one step; concurrent writers never lose each other
	Update(ctx context.Context, node vo.NodeRef, change func(*RunState)) error
	// Forget drops it; ok if already gone
	Forget(ctx context.Context, node vo.NodeRef) error
}

// SessionLog recorded shells into a run
type SessionLog interface {
	// Record returns a sink for the session; error means don't open
	Record(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, at time.Time) (io.WriteCloser, error)
}

// RequestLog replay by request id
type RequestLog interface {
	// Result returns the previous answer to this shard's request, or none
	Result(ctx context.Context, shardID vo.ShardID, id vo.RequestID) (results []NodeResult, found bool, err error)
	// Record stores the answer under the shard it was given for
	Record(ctx context.Context, shardID vo.ShardID, id vo.RequestID, results []NodeResult) error
}
