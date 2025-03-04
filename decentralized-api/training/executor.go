package training

import (
	"decentralized-api/broker"
	"github.com/productscience/inference/api/inference/inference"
)

type Executor struct {
	broker *broker.Broker
	tasks  map[uint64]*inference.TrainingTask
}

func NewExecutor(nodeBroker *broker.Broker) *Executor {
	return &Executor{
		broker: nodeBroker,
		tasks:  nil,
	}
}

func (e Executor) PreassignTask(task *inference.TrainingTask) {
}

func (e *Executor) AssignTask(task *inference.TrainingTask) {
	e.tasks[task.Id] = task
}
