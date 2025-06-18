package broker

import (
	"decentralized-api/logging"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
)

// SetNodeAdminStateCommand enables or disables a node administratively
type SetNodeAdminStateCommand struct {
	NodeId   string
	Enabled  bool
	Response chan error
}

func (c SetNodeAdminStateCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c SetNodeAdminStateCommand) Execute(b *Broker) {
	// Get current epoch
	var currentEpoch uint64
	if b.phaseTracker != nil {
		currentEpoch = b.phaseTracker.GetCurrentEpochState().CurrentEpoch.Epoch
	}

	b.mu.Lock()
	node, exists := b.nodes[c.NodeId]
	if !exists {
		c.Response <- fmt.Errorf("node not found: %s", c.NodeId)
		return
	}

	// Update admin state
	node.State.AdminState.Enabled = c.Enabled
	node.State.AdminState.Epoch = currentEpoch
	b.mu.Unlock()

	logging.Info("Updated node admin state", types.Nodes,
		"node_id", c.NodeId,
		"enabled", c.Enabled,
		"epoch", currentEpoch)

	c.Response <- nil
}
