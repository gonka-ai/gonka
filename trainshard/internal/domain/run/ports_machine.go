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
	ExitCode *int
}

type ContainerSpec struct {
	Shard vo.ShardID
	Node  vo.NodeRef
	Run   RunSpec
	Hosts []PinnedHost
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

// Images local cache; proposal base stays after a run
type Images interface {
	// Has returns whether the digest is already on disk
	Has(ctx context.Context, digest vo.ImageDigest) (bool, error)
	// Pull fetches missing layers; error leaves what we had
	Pull(ctx context.Context, digest vo.ImageDigest) error
	// Layers returns the layer chain to prove it's on the base
	Layers(ctx context.Context, digest vo.ImageDigest) (vo.ImageLayers, error)
}

// Containers the run's box; isolation is fixed at create or we refuse
type Containers interface {
	// Inspect returns the container, or absent with no error
	Inspect(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (ContainerInfo, error)
	// Create makes a stopped container; error if one exists
	Create(ctx context.Context, spec ContainerSpec) error
	// Start starts it; already running is a no-op
	Start(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Stop stops with grace; ok if already gone
	Stop(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, grace time.Duration) error
	// Remove deletes the container, keeps volumes/mesh; ok if gone
	Remove(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Shards returns every shard whose labels this node still carries on the machine
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}

// Streams read only, never changes the run
type Streams interface {
	// Logs copies output to out; a slow reader gets a gap
	Logs(ctx context.Context, req LogRequest, out io.Writer) error
	// Shell unprivileged session in the container, not the host
	Shell(ctx context.Context, req ExecRequest, session io.ReadWriter) error
}

// Egress the run's way out; the mesh and nothing else until sources are allowed
type Egress interface {
	// Allow opens the declared sources, closes the rest, and returns the names it pinned to an address
	Allow(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, sources []vo.Source) ([]PinnedHost, error)
}

// Volumes run disk, quota held by the kernel
type Volumes interface {
	// Ensure creates the volume with quota, or error
	Ensure(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, quotaBytes int64) error
	// Usage returns used, quota, and whether it's there
	Usage(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (used int64, quota int64, present bool, err error)
	// Wipe deletes run data; ok if empty
	Wipe(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
	// Archive copies leftovers to out, capped by quota
	Archive(ctx context.Context, shardID vo.ShardID, node vo.NodeRef, out io.Writer) error
	// Shards returns every shard this node still has a volume for
	Shards(ctx context.Context, node vo.NodeRef) ([]vo.ShardID, error)
}

// GPU this run's work vs everything else
type GPU interface {
	// Inventory returns model and count on the machine
	Inventory(ctx context.Context, node vo.NodeRef) (vo.GPUInventory, error)
	// InUse returns how many cards are busy
	InUse(ctx context.Context, node vo.NodeRef) (int, error)
	// ForeignWork returns true if other work is still on the cards
	ForeignWork(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// TrainingProcesses returns true if this run still has leftovers
	TrainingProcesses(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) (bool, error)
	// KillTraining kills this run's leftovers only
	KillTraining(ctx context.Context, shardID vo.ShardID, node vo.NodeRef) error
}

// NodeControl drain out of inference and hand back
type NodeControl interface {
	// Drained returns whether already out of inference/PoC
	Drained(ctx context.Context, node vo.NodeRef) (bool, error)
	// Drain stops new inference; returns whether empty yet
	Drain(ctx context.Context, node vo.NodeRef) (drained bool, err error)
	// Return hands the node back; the chain's return buffer covers the model loading, so we do not wait
	Return(ctx context.Context, node vo.NodeRef) error
}
