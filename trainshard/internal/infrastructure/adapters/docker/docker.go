package docker

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"trainshard/internal/domain/shared/vo"
)

type Config struct {
	Socket string

	APIVersion string

	VolumeRoot string

	User string

	SandboxImage string

	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
	ShmBytes    int64

	LogBufferBytes int

	Timeout time.Duration
}

func (c Config) withDefaults() Config {
	if c.Socket == "" {
		c.Socket = "/var/run/docker.sock"
	}
	if c.APIVersion == "" {
		c.APIVersion = "v1.44"
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
	if c.LogBufferBytes == 0 {
		c.LogBufferBytes = 4 << 20
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	return c
}

type Client struct {
	cfg  Config
	log  *slog.Logger
	http *http.Client
}

func New(cfg Config, log *slog.Logger) *Client {
	cfg = cfg.withDefaults()
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", cfg.Socket)
	}
	return &Client{
		cfg:  cfg,
		log:  log,
		http: &http.Client{Transport: &http.Transport{DialContext: dial}},
	}
}

func containerName(shardID vo.ShardID, node vo.NodeRef) string {
	return fmt.Sprintf("trainshard-%s-%s", shardID, node.NodeID)
}

func sandboxName(shardID vo.ShardID, node vo.NodeRef) string {
	return containerName(shardID, node) + "-net"
}

const (
	labelShard = "gonka.trainshard.shard"
	labelNode  = "gonka.trainshard.node"
	labelRole  = "gonka.trainshard.role"
)

func labels(shardID vo.ShardID, node vo.NodeRef, role string) map[string]string {
	return map[string]string{
		labelShard: shardID.String(),
		labelNode:  string(node.NodeID),
		labelRole:  role,
	}
}
