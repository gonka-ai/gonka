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
	Grace      time.Duration
	GraceGiven bool
}

// HostCommands is one signed call per machine, at the endpoint the chain names for its nodes. The
// request id makes a repeat return the recorded answer and change nothing. An error is a failure
// of the whole call, and every node in it counts as unanswered; a per-node failure is a Fault in
// that node's result
type HostCommands interface {
	// Deploy pulls the image and builds the container, stopped, on every node in the call
	Deploy(ctx context.Context, host vo.Host, call DeployCall) ([]NodeResult, error)
	// Start starts the container on every node in the call
	Start(ctx context.Context, host vo.Host, call HostCommand) ([]NodeResult, error)
	// Stop stops the container on every node in the call and keeps the reservation and the mesh
	Stop(ctx context.Context, host vo.Host, call StopCall) ([]NodeResult, error)
	// Status returns each node as the host sees it; it reads and never changes anything
	Status(ctx context.Context, host vo.Host, call HostCommand) ([]NodeStatus, error)
}

// HostStreams reaches one node's container on the machine that serves it, and never changes it
type HostStreams interface {
	// Logs copies that node's output to out until ctx ends or the host closes the stream
	Logs(ctx context.Context, host vo.Host, req LogRequest, out io.Writer) error
	// Shell opens a session inside that node's container, never on the host, and returns when it ends
	Shell(ctx context.Context, host vo.Host, req ExecRequest, session io.ReadWriter) error
}

// HostReports reads what each run left on record; it has to be collected before the run is cleaned up
type HostReports interface {
	// Report returns every image each node ran, with its digest and time, and the last exit code;
	// it reads and never changes anything. An error fails every node in the call
	Report(ctx context.Context, host vo.Host, shardID vo.ShardID, nodes []vo.NodeRef) ([]NodeReport, error)
}
