package training

import (
	"decentralized-api/broker"
	"github.com/productscience/inference/api/inference/inference"
	"log/slog"
)

const logTagExecutor = "[training-task-executor] "

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
	e.tasks[task.Id] = task
}

func (e *Executor) AssignTask(task *inference.TrainingTask) {
	e.tasks[task.Id] = task
}

func (e *Executor) ProcessTaskAssignedEvent(taskId uint64) {
	slog.Info(logTagExecutor+"Processing task assigned event", "taskId", taskId)

}

func (e *Executor) CheckStatusRoutine() {

}
