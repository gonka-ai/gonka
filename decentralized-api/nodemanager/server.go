package nodemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"common/logging"
	"common/nodemanager/gen"
	"common/runtimeconfig"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// brokerAcquirer is the subset of broker.Broker used by this server.
// broker.Broker satisfies this interface directly.
type brokerAcquirer interface {
	AcquireMLNode(ctx context.Context, model string, skipNodeIDs []string) (lockID, endpoint, nodeID string, err error)
	ReleaseMLNode(lockID string, outcome broker.InferenceResult) error
	TriggerStatusQuery(bypassDebounce bool)
	GetNodes() ([]broker.NodeResponse, error)
}

// Server implements gen.NodeManagerServer.
type Server struct {
	gen.UnimplementedNodeManagerServer
	broker        brokerAcquirer
	configManager *apiconfig.ConfigManager
	phaseTracker  *chainphase.ChainPhaseTracker
	runtimeConfig *runtimeconfig.Server
	hostEvents    *apiconfig.HostEventRing
	escrowLoad    *broker.EscrowLoadTracker
}

// ServerOption configures optional Server dependencies.
type ServerOption func(*Server)

// WithHostEventRing enables GetHostEvents. Without it the RPC returns FailedPrecondition.
func WithHostEventRing(ring *apiconfig.HostEventRing) ServerOption {
	return func(s *Server) { s.hostEvents = ring }
}

// WithEscrowLoadTracker attaches the per-escrow acquire rate tracker used to
// populate GetHostEventsResponse.escrow_load.
func WithEscrowLoadTracker(t *broker.EscrowLoadTracker) ServerOption {
	return func(s *Server) { s.escrowLoad = t }
}

// NewServer creates a NodeManager gRPC server. configManager and phaseTracker are
// required for GetRuntimeConfig; either may be nil to disable that RPC.
// Pass WithHostEventRing to enable GetHostEvents (nil / omitted → FailedPrecondition).
func NewServer(b brokerAcquirer, configManager *apiconfig.ConfigManager, phaseTracker *chainphase.ChainPhaseTracker, opts ...ServerOption) *Server {
	s := &Server{
		broker:        b,
		configManager: configManager,
		phaseTracker:  phaseTracker,
	}
	if configManager != nil {
		s.runtimeConfig = newRuntimeConfigServer(configManager, phaseTracker)
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Server) AcquireMLNode(ctx context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	lockID, endpoint, nodeID, err := s.broker.AcquireMLNode(ctx, req.Model, req.ExcludedNodes)
	if err == nil {
		if s.escrowLoad != nil {
			s.escrowLoad.Record(req.GetEscrowId())
		}
		return &gen.AcquireMLNodeResponse{LockId: lockID, Endpoint: endpoint, NodeId: nodeID}, nil
	}
	if errors.Is(err, broker.ErrNoNodesAvailable) {
		logging.Error("[NodeManager] No nodes available", types.Nodes)
		return nil, status.Error(codes.ResourceExhausted, "no nodes available")
	}
	if ctx.Err() != nil {
		logging.Error("[NodeManager] Context error", types.Nodes, "err", ctx.Err())
		return nil, status.FromContextError(ctx.Err()).Err()
	}
	// queue is full, so returning unavailable code
	return nil, status.Error(codes.Unavailable, err.Error())
}

func (s *Server) ReleaseMLNode(_ context.Context, req *gen.ReleaseMLNodeRequest) (*gen.ReleaseMLNodeResponse, error) {
	outcome := outcomeFromProto(req.Outcome)
	err := s.broker.ReleaseMLNode(req.LockId, outcome)
	if err == nil {
		if req.Outcome == gen.ReleaseOutcome_TRANSPORT_ERROR || req.Outcome == gen.ReleaseOutcome_TIMEOUT {
			s.broker.TriggerStatusQuery(false)
		}
		return &gen.ReleaseMLNodeResponse{}, nil
	}
	if errors.Is(err, broker.ErrLockNotFound) {
		logging.Error("[NodeManager] Lock not found ", types.Nodes)
		return nil, status.Error(codes.NotFound, broker.ErrLockNotFound.Error())
	}
	return nil, status.Error(codes.Internal, err.Error())
}

func (s *Server) ListNodeCapacity(_ context.Context, _ *gen.ListNodeCapacityRequest) (*gen.ListNodeCapacityResponse, error) {
	if s.broker == nil {
		return nil, status.Error(codes.FailedPrecondition, "node capacity: broker not configured")
	}
	nodes, err := s.broker.GetNodes()
	if err != nil {
		return nil, status.Errorf(codes.Internal, "node capacity: get nodes: %v", err)
	}
	out := &gen.ListNodeCapacityResponse{
		ServedAtUnix: time.Now().Unix(),
	}
	for _, nr := range nodes {
		statusStr := nr.State.CurrentStatus.String()
		maxConcurrent := int32(nr.Node.MaxConcurrent)
		lockCount := int32(nr.State.LockCount)
		if len(nr.Node.Models) == 0 {
			out.Nodes = append(out.Nodes, &gen.NodeCapacityEntry{
				NodeId:        nr.Node.Id,
				MaxConcurrent: maxConcurrent,
				LockCount:     lockCount,
				Status:        statusStr,
			})
			continue
		}
		for model := range nr.Node.Models {
			out.Nodes = append(out.Nodes, &gen.NodeCapacityEntry{
				NodeId:        nr.Node.Id,
				Model:         model,
				MaxConcurrent: maxConcurrent,
				LockCount:     lockCount,
				Status:        statusStr,
			})
		}
	}
	return out, nil
}

func (s *Server) GetRuntimeConfig(ctx context.Context, req *gen.GetRuntimeConfigRequest) (*gen.GetRuntimeConfigResponse, error) {
	if s.configManager == nil || s.runtimeConfig == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime config: config manager not configured")
	}
	return s.runtimeConfig.Handle(ctx, req)
}

func currentEpochID(pt *chainphase.ChainPhaseTracker) uint64 {
	if pt == nil {
		return 0
	}
	es := pt.GetCurrentEpochState()
	if es == nil {
		return 0
	}
	return es.LatestEpoch.EpochIndex
}

func outcomeFromProto(o gen.ReleaseOutcome) broker.InferenceResult {
	switch o {
	case gen.ReleaseOutcome_SUCCESS:
		return broker.InferenceSuccess{}
	case gen.ReleaseOutcome_TRANSPORT_ERROR:
		return broker.InferenceError{Message: "transport error"}
	case gen.ReleaseOutcome_APPLICATION_ERROR:
		return broker.InferenceError{Message: "application error"}
	case gen.ReleaseOutcome_TIMEOUT:
		return broker.InferenceError{Message: "timeout"}
	default:
		return broker.InferenceError{Message: fmt.Sprintf("unknown outcome: %v", o)}
	}
}
