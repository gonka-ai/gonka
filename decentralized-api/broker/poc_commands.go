package broker

import (
	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
)

type StartPocCommand struct {
	Response chan bool
}

func (c StartPocCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartPocCommand) Execute(broker *Broker) {
	nodes := broker.nodes

	totalNodes := len(nodes)
	for _, n := range nodes {
		client, err := NewNodeClient(&n.Node)
		if err != nil {
			logging.Error("Failed to create node client", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}

		err = client.Stop()
		if err != nil {
			logging.Error("Failed to send stop request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}
		n.State.IntendedStatus = types.HardwareNodeStatus_STOPPED
		n.State.Status = types.HardwareNodeStatus_STOPPED

		// TODO: analyze response somehow?
		_, err = o.sendInitGenerateRequest(n.Node, int64(totalNodes), blockHeight, blockHash)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.Nodes, n.Node.Host, "error", err)
			continue
		}
		n.State.IntendedStatus = types.HardwareNodeStatus_POC
		n.State.Status = types.HardwareNodeStatus_POC
	}
}
