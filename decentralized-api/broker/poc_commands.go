package broker

import (
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
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
	nodes := broker.nodes

	totalNodes := len(nodes)
	for _, n := range nodes {
		n.State.IntendedStatus = types.HardwareNodeStatus_POC

		client := newNodeClient(&n.Node)

		err := client.Stop()
		if err != nil {
			logging.Error("Failed to send stop request to node", types.PoC, "node", n.Node.Host, "error", err)
			continue
		}

		n.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)

		dto := mlnodeclient.BuildInitDto(c.BlockHeight, c.PubKey, int64(totalNodes), int64(n.Node.NodeNum), c.BlockHash, c.CallbackUrl)
		err = client.InitGenerate(dto)
		if err != nil {
			logging.Error("Failed to send init-generate request to node", types.Nodes, n.Node.Host, "error", err)
			continue
		}

		n.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
	}

	c.Response <- true
}
