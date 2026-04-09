package nodemanager

import (
	"context"
	"errors"
	"fmt"

	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/nodemanager/gen"

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
	broker brokerAcquirer
}

// NewServer creates a new NodeManager gRPC server backed by b.
func NewServer(b brokerAcquirer) *Server {
	return &Server{broker: b}
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
