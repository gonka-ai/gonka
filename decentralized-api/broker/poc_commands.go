package broker

import (
	"decentralized-api/logging"

	"github.com/productscience/inference/x/inference/types"
)

type StartPocCommand struct {
	BlockHeight int64
	BlockHash   string
	PubKey      string
	CallbackUrl string
	Response    chan bool
}

func (c StartPocCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartPocCommand) Execute(broker *Broker) {
	// Update intended status for all nodes
	totalNodes := len(broker.nodes)
	for _, n := range broker.nodes {
		n.State.IntendedStatus = types.HardwareNodeStatus_POC
	}

	submitted, failed := broker.nodeWorkGroup.ExecuteOnAll(func(nodeId string, node *NodeWithState) NodeWorkerCommand {
		return StartPoCNodeCommand{
			BlockHeight: c.BlockHeight,
			BlockHash:   c.BlockHash,
			PubKey:      c.PubKey,
			CallbackUrl: c.CallbackUrl,
			TotalNodes:  totalNodes,
		}
	})

	logging.Info("StartPocCommand completed", types.PoC,
		"submitted", submitted, "failed", failed, "total", totalNodes)

	c.Response <- true
}
