package worker

import (
	"log/slog"

	"trainshard/internal/domain/run"
	"trainshard/internal/domain/shared/vo"
)

type Notices struct {
	log  *slog.Logger
	last map[vo.NodeRef]string
}

func NewNotices(log *slog.Logger) *Notices {
	return &Notices{log: log, last: make(map[vo.NodeRef]string)}
}

func (n *Notices) Note(node vo.NodeRef, found run.Outcome) {
	was := n.last[node]
	n.last[node] = found.Waiting
	if was == found.Waiting || !found.Reserved {
		return
	}
	if found.Waiting == "" {
		n.log.Info("node prepared", "node_id", node.NodeID)
		return
	}
	n.log.Info("node not prepared", "node_id", node.NodeID, "waiting_for", found.Waiting)
}
