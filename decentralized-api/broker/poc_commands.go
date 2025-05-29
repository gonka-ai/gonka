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
	// Update intended status for all nodes
	totalNodes := len(broker.nodes)
	for _, n := range broker.nodes {
		n.State.IntendedStatus = types.HardwareNodeStatus_POC
	}

	// Execute PoC start on all nodes in parallel
	submitted, failed := broker.nodeWorkGroup.ExecuteOnAll(func(nodeId string, node *NodeWithState) func() error {
		// Capture command parameters in closure
		blockHeight := c.BlockHeight
		blockHash := c.BlockHash
		pubKey := c.PubKey
		callbackUrl := c.CallbackUrl
		totalNodeCount := totalNodes
		nodeNum := node.Node.NodeNum

		return func() error {
			client := newNodeClient(&node.Node)

			// Check if already in PoC state (idempotent)
			status, err := client.GetPowStatus()
			if err == nil && status.Status == mlnodeclient.POW_GENERATING {
				logging.Info("Node already in PoC state", types.PoC, "node_id", nodeId)
				node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
				return nil
			}

			// Stop node if needed
			nodeState, _ := client.NodeState()
			if nodeState != nil && nodeState.State != mlnodeclient.MlNodeState_STOPPED {
				err := client.Stop()
				if err != nil {
					logging.Error("Failed to send stop request to node", types.PoC,
						"node", node.Node.Host, "error", err)
					node.State.Failure("Failed to stop for PoC")
					return err
				}
				node.State.UpdateStatusNow(types.HardwareNodeStatus_STOPPED)
			}

			// Start PoC
			dto := mlnodeclient.BuildInitDto(
				blockHeight,
				pubKey,
				int64(totalNodeCount),
				int64(nodeNum),
				blockHash,
				callbackUrl,
			)
			err = client.InitGenerate(dto)
			if err != nil {
				logging.Error("Failed to send init-generate request to node", types.Nodes,
					node.Node.Host, "error", err)
				node.State.Failure("Failed to start PoC")
				return err
			}

			node.State.UpdateStatusNow(types.HardwareNodeStatus_POC)
			logging.Info("Successfully started PoC on node", types.PoC, "node_id", nodeId)
			return nil
		}
	})

	logging.Info("StartPocCommand completed", types.PoC,
		"submitted", submitted, "failed", failed, "total", totalNodes)

	c.Response <- true
}
