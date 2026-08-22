package docker

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"

	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Socket string

	APIVersion string

	VolumeRoot string

	User string

	SandboxImage string

	GPUKind string

	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
	ShmBytes    int64
	TmpBytes    int64

	LogBufferBytes int
	LogFileBytes   int64
	LogFiles       int

	Timeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Socket == "" {
		c.Socket = "/var/run/docker.sock"
	}
	if c.VolumeRoot == "" {
		c.VolumeRoot = "/var/lib/trainshardd/volumes"
	}
	if c.User == "" {
		c.User = "1000:1000"
	}
	if c.SandboxImage == "" {
		c.SandboxImage = "registry.k8s.io/pause:3.9"
	}
	if c.PidsLimit == 0 {
		c.PidsLimit = 4096
	}
	if c.ShmBytes == 0 {
		c.ShmBytes = 1 << 30
	}
	if c.TmpBytes == 0 {
		c.TmpBytes = 512 << 20
	}
	if c.LogBufferBytes == 0 {
		c.LogBufferBytes = 4 << 20
	}
	if c.LogFileBytes == 0 {
		c.LogFileBytes = 64 << 20
	}
	if c.LogFiles == 0 {
		c.LogFiles = 3
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	return c
}

type Client struct {
	cfg    Config
	log    *slog.Logger
	engine *client.Client
}

// New holds no timeout on the engine client: a log follow, a shell session and an image pull
// all outlive cfg.Timeout, which is applied per call by the operations that do return
func New(cfg Config, log *slog.Logger) (*Client, error) {
	cfg = cfg.withDefaults()

	options := []client.Opt{client.WithHost("unix://" + cfg.Socket)}
	if version := strings.TrimPrefix(cfg.APIVersion, "v"); version != "" {
		options = append(options, client.WithAPIVersion(version))
	} else {
		options = append(options, client.WithAPIVersionNegotiation())
	}

	engine, err := client.New(options...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{cfg: cfg, log: log, engine: engine}, nil
}

func (c *Client) Close() error { return c.engine.Close() }

func (c *Client) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.cfg.Timeout)
}

// settled reports an engine answer that leaves the machine in the state we asked for:
// the container is already there, already gone, or already in that state
func settled(err error) bool {
	return err == nil || cerrdefs.IsNotFound(err) || cerrdefs.IsNotModified(err)
}

func containerName(shardID vo.ShardID, node vo.NodeRef) string {
	return fmt.Sprintf("trainshard-%s-%s", shardID, node.NodeID)
}

func sandboxName(shardID vo.ShardID, node vo.NodeRef) string {
	return containerName(shardID, node) + "-net"
}

const (
	labelShard    = "gonka.trainshard.shard"
	labelNode     = "gonka.trainshard.node"
	labelRole     = "gonka.trainshard.role"
	labelRevision = "gonka.trainshard.revision"
)

func labels(shardID vo.ShardID, node vo.NodeRef, role string) map[string]string {
	return map[string]string{
		labelShard: shardID.String(),
		labelNode:  string(node.NodeID),
		labelRole:  role,
	}
}
