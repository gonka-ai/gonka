package run

import (
	"context"
	"io"
	"time"

	"trainshard/internal/domain/shared/vo"
)

// RunStore is the host's run state, one per node, kept on disk across restarts
type RunStore interface {
	// Load returns the node's state; none is a zero state with found false, not an error
	Load(ctx context.Context, node vo.NodeRef) (state RunState, found bool, err error)
	// Update applies the change to the stored state as one step, so concurrent writers never lose
	// each other; on error nothing was stored
	Update(ctx context.Context, node vo.NodeRef, change func(*RunState)) error
	// Forget drops the node's state; none stored is a no-op
	Forget(ctx context.Context, node vo.NodeRef) error
}

// SessionLog keeps the transcript of every shell opened into a run
type SessionLog interface {
	// Record returns the sink a session's transcript goes to; an error means the session must not
	// be opened, since it could not be recorded
	Record(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, at time.Time) (io.WriteCloser, error)
}

type Op string

const (
	OpDeploy Op = "deploy"
	OpStart  Op = "start"
	OpStop   Op = "stop"
	OpMesh   Op = "mesh"
)

// RequestRef keeps the op, shard and actor in the key: the same id sent as another command, under
// another shard or by the other actor is another request, and replaying the first answer would
// swallow it
type RequestRef struct {
	Op    Op
	Shard vo.ShardID
	Actor vo.Address
	ID    vo.RequestID
}

func (r RequestRef) String() string {
	return string(r.Op) + "/" + r.Shard.String() + "/" + string(r.Actor) + "/" + string(r.ID)
}

// RequestLog keeps the answer to every mutating request on disk, so a repeat is replayed instead of
// applied again; an answer is kept for a bounded time, past which it is not found
type RequestLog interface {
	// Result returns the recorded answer to that very request; none is found false, not an error
	Result(ctx context.Context, ref RequestRef) (results []NodeResult, found bool, err error)
	// Record stores the answer under the request that produced it, replacing any before it
	Record(ctx context.Context, ref RequestRef, results []NodeResult) error
}
