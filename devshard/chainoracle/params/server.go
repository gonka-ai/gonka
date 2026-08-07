package params

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Config wires the params-side NodeManager gRPC server.
type Config struct {
	Source     *CachedSource
	MaxWaitCap func() time.Duration
	Log        *slog.Logger
	// MLEndpoint is the single-node shorthand (MOCK_ML_ENDPOINT). Used when
	// MLNodes is empty.
	MLEndpoint string
	// MLNodes is the ordered pool for AcquireMLNode (T7). When non-empty it
	// takes precedence over MLEndpoint.
	MLNodes []MLNode
}

// Server implements gen.NodeManagerServer for params long-poll + ML stubs.
type Server struct {
	gen.UnimplementedNodeManagerServer
	runtimeConfig *commonruntimeconfig.Server

	nodes []MLNode
	rr    atomic.Uint64

	mu    sync.Mutex
	locks map[string]string // lockID → nodeID
	inUse map[string]int    // nodeID → active lock count
	seq   atomic.Uint64
}

// NewServer builds a params NodeManager server backed by common/runtimeconfig.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Source == nil {
		return nil, errors.New("chainoracle/params: Source is required")
	}
	nodes := cfg.MLNodes
	if len(nodes) == 0 && strings.TrimSpace(cfg.MLEndpoint) != "" {
		n, err := MLNodeFromEndpoint(cfg.MLEndpoint)
		if err != nil {
			return nil, err
		}
		nodes = []MLNode{n}
	}
	if len(nodes) == 0 {
		return nil, errors.New("chainoracle/params: MLNodes or MLEndpoint is required")
	}
	s := &Server{
		nodes: nodes,
		locks: make(map[string]string),
		inUse: make(map[string]int),
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
	excluded := make(map[string]struct{}, len(req.GetExcludedNodes()))
	for _, id := range req.GetExcludedNodes() {
		id = strings.TrimSpace(id)
		if id != "" {
			excluded[id] = struct{}{}
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	n := len(s.nodes)
	start := int(s.rr.Add(1)-1) % n
	for i := 0; i < n; i++ {
		node := s.nodes[(start+i)%n]
		if _, skip := excluded[node.ID]; skip {
			continue
		}
		if node.MaxConcurrent > 0 && s.inUse[node.ID] >= node.MaxConcurrent {
			continue
		}
		lockID := "mock-" + req.GetModel() + "-" + itoa(s.seq.Add(1))
		s.locks[lockID] = node.ID
		s.inUse[node.ID]++
		return &gen.AcquireMLNodeResponse{
			LockId:   lockID,
			Endpoint: node.Endpoint,
			NodeId:   node.ID,
		}, nil
	}
	return nil, status.Error(codes.ResourceExhausted, "no available ML nodes")
}

func (s *Server) ReleaseMLNode(_ context.Context, req *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
	lockID := strings.TrimSpace(req.GetLockId())
	if lockID == "" {
		return &gen.ReleaseMLNodeResponse{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	nodeID, ok := s.locks[lockID]
	if !ok {
		return &gen.ReleaseMLNodeResponse{}, nil
	}
	delete(s.locks, lockID)
	if s.inUse[nodeID] > 0 {
		s.inUse[nodeID]--
	}
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
