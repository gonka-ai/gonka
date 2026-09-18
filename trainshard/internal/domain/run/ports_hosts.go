package run

import (
	"context"
	"io"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type HostCommand struct {
	Shard     vo.ShardID
	Nodes     []vo.NodeRef
	RequestID vo.RequestID
	Deadline  time.Time
}

type DeployCall struct {
	HostCommand
	Run RunSpec
}

type StopCall struct {
	HostCommand
	Grace time.Duration
}

// HostCommands is one call per machine, addressed by the host the chain names for its nodes;
// the request id makes a repeat a no-op
type HostCommands interface {
	// Deploy pulls image, creates stopped container; returns per-node results
	Deploy(ctx context.Context, host vo.Host, call DeployCall) ([]NodeResult, error)
	// Start starts the containers; returns per-node results
	Start(ctx context.Context, host vo.Host, call HostCommand) ([]NodeResult, error)
	// Stop stops them, keeps reservation and mesh; returns per-node results
	Stop(ctx context.Context, host vo.Host, call StopCall) ([]NodeResult, error)
	// Status returns each node as the host sees it
	Status(ctx context.Context, host vo.Host, call HostCommand) ([]NodeStatus, error)
}

// HostStreams logs and shell for one node, on the machine that serves it
type HostStreams interface {
	// Logs copies that node's output to out
	Logs(ctx context.Context, host vo.Host, req LogRequest, out io.Writer) error
	// Shell session in that container, not the host
	Shell(ctx context.Context, host vo.Host, req ExecRequest, session io.ReadWriter) error
}

// HostReports collect before volumes are wiped
type HostReports interface {
	// Report returns images and exit codes per node
	Report(ctx context.Context, host vo.Host, shardID vo.ShardID, nodes []vo.NodeRef) ([]NodeReport, error)
}
