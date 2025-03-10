package training

import (
	"context"
	"decentralized-api/api/model"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"errors"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
)

const logTagExecutor = "[training-task-executor] "

type Executor struct {
	broker       *broker.Broker
	cosmosClient cosmosclient.CosmosMessageClient
	tasks        map[uint64]struct{}
	ctx          context.Context
}

func NewExecutor(ctx context.Context, nodeBroker *broker.Broker, cosmosClient cosmosclient.CosmosMessageClient) *Executor {
	return &Executor{
		broker:       nodeBroker,
		cosmosClient: cosmosClient,
		tasks:        make(map[uint64]struct{}),
		ctx:          ctx,
	}
}

func (e Executor) PreassignTask(nodes model.LockTrainingNodesDto) error {
	command := broker.NewLockNodesForTrainingCommand(nodes.NodeIds)
	err := e.broker.QueueMessage(command)
	if err != nil {
		return err
	}

	success := <-command.Response

	if success {
		e.tasks[nodes.TrainingTaskId] = struct{}{}
		return nil
	} else {
		return errors.New("failed to lock nodes")
	}
}

func (e *Executor) ProcessTaskAssignedEvent(taskId uint64) {
	slog.Info(logTagExecutor+"Processing task assigned event", "taskId", taskId)
	queryClient := e.cosmosClient.NewInferenceQueryClient()
	req := types.QueryTrainingTaskRequest{Id: taskId}
	resp, err := queryClient.TrainingTask(*e.cosmosClient.GetContext(), &req)

	if err != nil {
		slog.Error(logTagExecutor+"Error fetching task", "taskId", taskId, "error", err)
		return
	}

	if resp.Task.Assignees == nil {
		slog.Error(logTagExecutor+"No assignees found for task", "taskId", taskId)
		return
	}

	myNodes := make([]string, 0)
	for _, a := range resp.Task.Assignees {
		if a.Participant != e.cosmosClient.GetAddress() {
			continue
		}
		slog.Info(logTagExecutor+"Found task assigned to me", "taskId", taskId)
		for _, node := range a.NodeIds {
			myNodes = append(myNodes, node)
		}
	}

	if len(myNodes) == 0 {
		slog.Info(logTagExecutor+"The task isn't assigned to me", "taskId", taskId)
		return
	}

	slog.Info(logTagExecutor+"The task is assigned to me", "taskId", taskId, "nodes", myNodes)

	// PRTODO: FIXME: CHOOSE MASTER NODE AND STUFF!
	command := broker.NewStartTrainingCommand(
		"whatever",
		len(myNodes),
		map[string]int{},
	)
	err = e.broker.QueueMessage(command)
	if err != nil {
		slog.Error(logTagExecutor+"Error starting training", "taskId", taskId, "error", err)
		return
	}

	success := <-command.Response
	if success {
		slog.Info(logTagExecutor+"Training started", "taskId", taskId)
	} else {
		slog.Error(logTagExecutor+"Error starting training", "taskId", taskId)
	}
}

func (e *Executor) CheckStatusRoutine() {

}
