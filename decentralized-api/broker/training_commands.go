package broker

import (
	"decentralized-api/logging"
	"github.com/productscience/inference/x/inference/types"
)

type StartTrainingCommand struct {
	taskId            uint64
	masterNodeAddress string
	worldSize         int
	nodeRanks         map[string]int
	Response          chan bool
}

func NewStartTrainingCommand(taskId uint64, masterNodeAddress string, worldSize int, nodeRanks map[string]int) StartTrainingCommand {
	return StartTrainingCommand{
		taskId:            taskId,
		masterNodeAddress: masterNodeAddress,
		worldSize:         worldSize,
		nodeRanks:         nodeRanks,
		Response:          make(chan bool, 2),
	}
}

func (c StartTrainingCommand) GetResponseChannelCapacity() int {
	return cap(c.Response)
}

func (c StartTrainingCommand) Execute(broker *Broker) {
	for nodeId, rank := range c.nodeRanks {
		node, nodeFound := broker.nodes[nodeId]
		if !nodeFound {
			logging.Error("Node not found", types.Nodes, "node_id", nodeId)
			c.Response <- false
			return
		}

		client := newNodeClient(&node.Node)

		err := client.Stop()
		if err != nil {
			logging.Error("Error stopping training", types.Nodes, "error", err)
			c.Response <- false
			return
		}

		err = client.StartTraining(c.taskId, broker.client.GetAddress(), nodeId, c.masterNodeAddress, rank, c.worldSize)
		if err != nil {
			logging.Error("Error starting training", types.Nodes, "error", err)
			c.Response <- false
			return
		}
	}

	c.Response <- true
}
