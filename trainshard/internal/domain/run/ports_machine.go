package run

import (
	"context"
	"io"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type ContainerInfo struct {
	State    vo.ContainerState
	Image    vo.ImageDigest
	Revision int
	ExitCode *int
}

type ContainerSpec struct {
	Shard    vo.ShardID
	Node     vo.NodeRef
	Run      RunSpec
	Revision int
	Hosts    []PinnedHost
}

type PinnedHost struct {
	Name string
	IP   string
}

type LogRequest struct {
	Shard vo.ShardID
	Node  vo.NodeRef
	Since time.Time
	Tail  int
}

type ExecRequest struct {
	Shard vo.ShardID
	Node  vo.NodeRef
}

// Images is the machine's local image cache, addressed by digest only; the proposal's base image
// stays in it after a run
type Images interface {
	// Has returns whether the digest is in the cache; absent is false, not an error
	Has(ctx context.Context, digest vo.ImageDigest) (bool, error)
	// Pull fetches only the layers the cache lacks. Idempotent; a failed pull leaves the cache and
	// every container as they were
	Pull(ctx context.Context, digest vo.ImageDigest) error
	// Layers returns the layer chain of a cached image, so it can be checked against the base; an
	// image not in the cache is an error, never an empty chain
	Layers(ctx context.Context, digest vo.ImageDigest) (vo.ImageLayers, error)
}

// Containers is the run's container, one per shard and node; isolation is fixed at create, and a
// part that cannot be applied fails the create rather than loosening the box
type Containers interface {
	// Inspect returns the container; none is ContainerAbsent with no error
	Inspect(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (ContainerInfo, error)
	// Create makes the container stopped, labelled with the shard, node and revision; an error if
	// one already exists
	Create(ctx context.Context, spec ContainerSpec) error
	// Start runs a created container; one already running is a no-op
	Start(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Stop waits up to grace before killing; a container already stopped or gone is a no-op
	Stop(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, grace time.Duration) error
	// Remove deletes the container and leaves the volumes and the mesh; one already gone is a no-op
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard a container labelled with this node still exists for, running or not
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}

// Streams reads from and attaches to the run's container and never changes it
type Streams interface {
	// Logs copies the container's output to out through a bounded buffer; a reader too slow for it
	// gets a reported gap
	Logs(ctx context.Context, req LogRequest, out io.Writer) error
	// Shell opens an unprivileged session inside the container, never on the host, and returns when
	// the session ends
	Shell(ctx context.Context, req ExecRequest, session io.ReadWriter) error
}

// Egress is the run's way out: the mesh, the sources it declared, and nothing else
type Egress interface {
	// Allow fixes the ruleset to the declared sources, resolved once, and returns the names it
	// pinned to an address; an error means the sources are not in force and no container may be
	// created on that network
	Allow(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, sources []vo.Source) ([]PinnedHost, error)
	// Fenced returns whether the run's network still holds a ruleset; false once the network was
	// rebuilt or is gone, not an error
	Fenced(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
}

// Volumes is the run's disk, held to its quota by the kernel
type Volumes interface {
	// Ensure creates the volume and sets its quota; idempotent, and a repeat sets the quota again.
	// A quota the kernel cannot hold is an error, never a volume without a limit
	Ensure(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, quotaBytes int64) error
	// Usage returns bytes used, the quota, and whether the volume exists; absent is not an error
	Usage(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (used int64, quota int64, present bool, err error)
	// Wipe deletes the volume, its data and its quota; one already gone is a no-op
	Wipe(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard this node still has a volume for
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}

// GPU tells this run's work on the node's cards from everything else. An error means the cards
// could not be read, never that they are free
type GPU interface {
	// Inventory returns the model and count of cards the node has
	Inventory(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
	// InUse returns how many of the node's cards are busy, whoever holds them
	InUse(ctx context.Context, node vo.NodeRef) (int, error)
	// ForeignWork returns whether a process that is not the shard's run holds one of the cards
	ForeignWork(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// TrainingProcesses returns whether a process of the shard's run is still on the cards
	TrainingProcesses(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// KillTraining kills the shard's run processes and nothing else; none left is a no-op
	KillTraining(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
}

// NodeControl takes the node out of inference and PoC and hands it back, through the dAPI
type NodeControl interface {
	// Drained returns whether the node is disabled, stopped with nothing loaded, and holding no work
	Drained(ctx context.Context, node vo.NodeRef) (bool, error)
	// Drain disables the node and stops its mlnode so the cards are freed, and returns whether that
	// has happened yet. Idempotent
	Drain(ctx context.Context, node vo.NodeRef) (drained bool, err error)
	// Return starts and enables the node if needed, including one the operator had stopped.
	// Idempotent, and it does not wait for the model: the chain's return buffer covers loading
	Return(ctx context.Context, node vo.NodeRef) error
}
