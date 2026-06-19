package nodemanager

import (
	"context"
	"errors"
	"fmt"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/logging"
	"devshard/nodemanager/gen"

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
	broker        brokerAcquirer
	configManager *apiconfig.ConfigManager
	phaseTracker  *chainphase.ChainPhaseTracker
}

// NewServer creates a NodeManager gRPC server. configManager and phaseTracker are
// required for GetRuntimeConfig; either may be nil to disable that RPC.
func NewServer(b brokerAcquirer, configManager *apiconfig.ConfigManager, phaseTracker *chainphase.ChainPhaseTracker) *Server {
	return &Server{
		broker:        b,
		configManager: configManager,
		phaseTracker:  phaseTracker,
	}
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
	if s.configManager == nil {
		return nil, status.Error(codes.FailedPrecondition, "runtime config: config manager not configured")
	}

	maxWait := clampMaxWait(req.GetMaxWaitSeconds())
	clientHeight := req.GetClientParamsBlockHeight()

	for {
		var wake <-chan struct{}
		if maxWait > 0 {
			notifier := s.configManager.RuntimeConfigNotifier()
			if notifier == nil {
				return nil, status.Error(codes.FailedPrecondition, "runtime config: notifier not configured")
			}
			// Subscribe before reading snapshot to avoid lost wake-ups.
			wake = notifier.NotifyChan()
		}

		epochID := currentEpochID(s.phaseTracker)
		snap := s.configManager.RuntimeConfigSnapshot(epochID)

		// Full config: initial fetch, server ahead, or server has not synced params yet.
		if clientHeight == 0 || snap.ParamsBlockHeight == 0 || snap.ParamsBlockHeight > clientHeight {
			reason := "initial_fetch"
			switch {
			case clientHeight == 0:
				reason = "initial_fetch"
			case snap.ParamsBlockHeight == 0:
				reason = "server_not_synced"
			case snap.ParamsBlockHeight > clientHeight:
				reason = "server_ahead"
			}
			logging.Info("runtime_config: GetRuntimeConfig returning full config", types.Config,
				"reason", reason,
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", snap.ParamsBlockHeight,
				"epochID", epochID,
				"maxWait", maxWait,
				"devshardRequestsEnabled", snap.DevshardRequestsEnabled,
			)
			return &gen.GetRuntimeConfigResponse{
				Unchanged: false,
				Config:    runtimeConfigFromSnapshot(snap),
			}, nil
		}

		// Client is caught up (server height > 0).
		if maxWait <= 0 {
			logging.Debug("runtime_config: GetRuntimeConfig immediate unchanged", types.Config,
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", snap.ParamsBlockHeight,
				"epochID", epochID,
				"devshardRequestsEnabled", snap.DevshardRequestsEnabled,
			)
			return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
		}

		logging.Debug("runtime_config: GetRuntimeConfig long-poll waiting", types.Config,
			"clientParamsBlockHeight", clientHeight,
			"serverParamsBlockHeight", snap.ParamsBlockHeight,
			"epochID", epochID,
			"maxWait", maxWait,
		)
		timer := time.NewTimer(maxWait)
		select {
		case <-wake:
			timer.Stop()
			logging.Debug("runtime_config: GetRuntimeConfig long-poll notified, retrying", types.Config,
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", s.configManager.RuntimeParamsBlockHeight(),
				"epochID", epochID,
				"devshardRequestsEnabled", s.configManager.RuntimeConfigSnapshot(epochID).DevshardRequestsEnabled,
			)
		case <-timer.C:
			logging.Debug("runtime_config: GetRuntimeConfig long-poll timed out", types.Config,
				"clientParamsBlockHeight", clientHeight,
				"serverParamsBlockHeight", snap.ParamsBlockHeight,
				"epochID", epochID,
				"maxWait", maxWait,
			)
			return &gen.GetRuntimeConfigResponse{Unchanged: true}, nil
		case <-ctx.Done():
			timer.Stop()
			return nil, status.FromContextError(ctx.Err()).Err()
		}
	}
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
