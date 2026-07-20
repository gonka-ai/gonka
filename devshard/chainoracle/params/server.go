package params

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"
)

// Config wires the params-side NodeManager gRPC server.
type Config struct {
	Source   *CachedSource
	MaxWaitCap func() time.Duration
	Log      *slog.Logger
	// MLEndpoint is returned from AcquireMLNode (mock-openai URL in testenv).
	MLEndpoint string
}

// Server implements gen.NodeManagerServer for params long-poll + ML stubs.
type Server struct {
	gen.UnimplementedNodeManagerServer
	runtimeConfig *commonruntimeconfig.Server
	mlEndpoint    string
	lockSeq       atomic.Uint64
}

// NewServer builds a params NodeManager server backed by common/runtimeconfig.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Source == nil {
		return nil, errors.New("chainoracle/params: Source is required")
	}
	s := &Server{
		mlEndpoint: cfg.MLEndpoint,
		runtimeConfig: commonruntimeconfig.NewServer(commonruntimeconfig.ServerDeps{
			Source:     cfg.Source,
			Epochs:     cfg.Source,
			Notifier:   cfg.Source,
			MaxWaitCap: cfg.MaxWaitCap,
			Log:        cfg.Log,
		}),
	}
	return s, nil
}

func (s *Server) GetRuntimeConfig(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
	return s.runtimeConfig.Handle(ctx, req)
}

func (s *Server) AcquireMLNode(_ context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	if s.mlEndpoint == "" {
		return nil, errors.New("ml endpoint not configured")
	}
	id := s.lockSeq.Add(1)
	return &gen.AcquireMLNodeResponse{
		LockId:   "mock-" + req.GetModel() + "-" + itoa(id),
		Endpoint: s.mlEndpoint,
		NodeId:   "mock-openai",
	}, nil
}

func (s *Server) ReleaseMLNode(context.Context, *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
	return &gen.ReleaseMLNodeResponse{}, nil
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
