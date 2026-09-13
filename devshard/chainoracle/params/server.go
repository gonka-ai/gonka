package params

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"common/nodemanager/gen"
	commonruntimeconfig "common/runtimeconfig"
)

// Config wires the params-side NodeManager gRPC server.
type Config struct {
	Source     *CachedSource
	MaxWaitCap func() time.Duration
	Log        *slog.Logger
	// MLEndpoint is returned from AcquireMLNode (mock-openai URL in testenv).
	MLEndpoint string
	// MLNodes overrides MLEndpoint with a deterministic round-robin pool. It is
	// used by testenv load scenarios; production DAPI remains the allocator in
	// deployed environments.
	MLNodes []MLNode
}

// MLNode is one OpenAI-compatible endpoint returned by AcquireMLNode.
type MLNode struct {
	ID       string
	Endpoint string
}

// Server implements gen.NodeManagerServer for params long-poll + ML stubs.
type Server struct {
	gen.UnimplementedNodeManagerServer
	runtimeConfig *commonruntimeconfig.Server
	mlNodes       []MLNode
	nextNode      atomic.Uint64
	lockSeq       atomic.Uint64
	allocationMu  sync.Mutex
	allocations   map[string]uint64
}

// NewServer builds a params NodeManager server backed by common/runtimeconfig.
func NewServer(cfg Config) (*Server, error) {
	if cfg.Source == nil {
		return nil, errors.New("chainoracle/params: Source is required")
	}
	nodes := append([]MLNode(nil), cfg.MLNodes...)
	if len(nodes) == 0 && cfg.MLEndpoint != "" {
		nodes = []MLNode{{ID: "mock-openai", Endpoint: cfg.MLEndpoint}}
	}
	for _, node := range nodes {
		if node.ID == "" || node.Endpoint == "" {
			return nil, errors.New("chainoracle/params: ML nodes require id and endpoint")
		}
	}

	s := &Server{
		mlNodes: nodes,
		runtimeConfig: commonruntimeconfig.NewServer(commonruntimeconfig.ServerDeps{
			Source:     cfg.Source,
			Epochs:     cfg.Source,
			Notifier:   cfg.Source,
			MaxWaitCap: cfg.MaxWaitCap,
			Log:        cfg.Log,
		}),
		allocations: make(map[string]uint64, len(nodes)),
	}
	return s, nil
}

func (s *Server) GetRuntimeConfig(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
	return s.runtimeConfig.Handle(ctx, req)
}

func (s *Server) AcquireMLNode(_ context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	if len(s.mlNodes) == 0 {
		return nil, errors.New("no ML nodes configured")
	}
	excluded := make(map[string]struct{}, len(req.GetExcludedNodes()))
	for _, id := range req.GetExcludedNodes() {
		excluded[id] = struct{}{}
	}
	available := make([]MLNode, 0, len(s.mlNodes))
	for _, node := range s.mlNodes {
		if _, skip := excluded[node.ID]; !skip {
			available = append(available, node)
		}
	}
	if len(available) == 0 {
		return nil, errors.New("no ML nodes available")
	}
	node := available[(s.nextNode.Add(1)-1)%uint64(len(available))]
	id := s.lockSeq.Add(1)
	s.allocationMu.Lock()
	s.allocations[node.ID]++
	s.allocationMu.Unlock()
	return &gen.AcquireMLNodeResponse{
		LockId:   "mock-" + node.ID + "-" + req.GetModel() + "-" + itoa(id),
		Endpoint: node.Endpoint,
		NodeId:   node.ID,
	}, nil
}

func (s *Server) ReleaseMLNode(context.Context, *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
	return &gen.ReleaseMLNodeResponse{}, nil
}

// AllocationCounts returns a copy of successful AcquireMLNode calls by node.
func (s *Server) AllocationCounts() map[string]uint64 {
	s.allocationMu.Lock()
	defer s.allocationMu.Unlock()
	counts := make(map[string]uint64, len(s.allocations))
	for id, count := range s.allocations {
		counts[id] = count
	}
	return counts
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
