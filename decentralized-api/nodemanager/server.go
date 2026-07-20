package nodemanager

import (
	"context"
	"errors"
	"fmt"

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
}

// Server implements gen.NodeManagerServer.
type Server struct {
	gen.UnimplementedNodeManagerServer
	broker          brokerAcquirer
	configManager   *apiconfig.ConfigManager
	phaseTracker    *chainphase.ChainPhaseTracker
	runtimeConfig   *runtimeconfig.Server
}

// NewServer creates a NodeManager gRPC server. configManager and phaseTracker are
// required for GetRuntimeConfig; either may be nil to disable that RPC.
func NewServer(b brokerAcquirer, configManager *apiconfig.ConfigManager, phaseTracker *chainphase.ChainPhaseTracker) *Server {
	s := &Server{
		broker:        b,
		configManager: configManager,
		phaseTracker:  phaseTracker,
	}
	if configManager != nil {
		s.runtimeConfig = newRuntimeConfigServer(configManager, phaseTracker)
	}
	return s
}

func (s *Server) AcquireMLNode(ctx context.Context, req *gen.AcquireMLNodeRequest) (*gen.AcquireMLNodeResponse, error) {
	lockID, endpoint, nodeID, err := s.broker.AcquireMLNode(ctx, req.Model, req.ExcludedNodes)
	if err == nil {
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
