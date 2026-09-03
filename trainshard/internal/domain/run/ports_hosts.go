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

// HostCommands one call per host; request id makes a repeat a no-op
type HostCommands interface {
	// Deploy pulls image, creates stopped container; returns per-node results
	Deploy(ctx context.Context, participant vo.Participant, call DeployCall) ([]NodeResult, error)
	// Start starts the containers; returns per-node results
	Start(ctx context.Context, participant vo.Participant, call HostCommand) ([]NodeResult, error)
	// Stop stops them, keeps reservation and mesh; returns per-node results
	Stop(ctx context.Context, participant vo.Participant, call StopCall) ([]NodeResult, error)
	// Status returns each node as the host sees it
	Status(ctx context.Context, participant vo.Participant, call HostCommand) ([]NodeStatus, error)
}

// HostStreams logs and shell for one node
type HostStreams interface {
	// Logs copies that node's output to out
	Logs(ctx context.Context, participant vo.Participant, req LogRequest, out io.Writer) error
	// Shell session in that container, not the host
	Shell(ctx context.Context, participant vo.Participant, req ExecRequest, session io.ReadWriter) error
}

// HostReports collect before volumes are wiped
type HostReports interface {
	// Report returns images and exit codes per node
	Report(ctx context.Context, participant vo.Participant, shardID vo.ShardID, nodes []vo.NodeRef) ([]NodeReport, error)
}
