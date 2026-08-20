package fake

import (
	"context"
	"log/slog"
	"sync"

	"trainshard/internal/domain/shared/vo"
)

type NodeManager struct {
	mu      sync.Mutex
	log     *slog.Logger
	drained map[vo.NodeRef]struct{}
}

func New(log *slog.Logger) *NodeManager {
	return &NodeManager{log: log, drained: map[vo.NodeRef]struct{}{}}
}

func (n *NodeManager) Drained(_ context.Context, node vo.NodeRef) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	_, drained := n.drained[node]
	return drained, nil
}

func (n *NodeManager) Drain(_ context.Context, node vo.NodeRef) (bool, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.drained[node] = struct{}{}
	n.log.Info("drained node", "node_id", node.NodeID)
	return true, nil
}

func (n *NodeManager) Return(_ context.Context, node vo.NodeRef) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	delete(n.drained, node)
	n.log.Info("returned node to inference", "node_id", node.NodeID)
	return nil
}
