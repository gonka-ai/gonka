package training

import (
	"context"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/logging"
	"errors"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"sort"
	"time"
)

const logTagExecutor = "[training-task-executor] "

type Executor struct {
	broker       *broker.Broker
	cosmosClient cosmosclient.CosmosMessageClient
	tasks        map[uint64]struct{}
	ctx          context.Context
}

func NewExecutor(ctx context.Context, nodeBroker *broker.Broker, cosmosClient cosmosclient.CosmosMessageClient) *Executor {
	e := &Executor{
		broker:       nodeBroker,
		cosmosClient: cosmosClient,
		tasks:        make(map[uint64]struct{}),
		ctx:          ctx,
	}

	go e.checkStatusRoutine()

	return e
}

func (e Executor) PreassignTask(taskId uint64, nodeIds []string) error {
	command := broker.NewLockNodesForTrainingCommand(nodeIds)
	err := e.broker.QueueMessage(command)
	if err != nil {
		return err
	}

	success := <-command.Response

	if success {
		e.tasks[taskId] = struct{}{}
		return nil
	} else {
		return errors.New("failed to lock nodes")
	}
}

func (e *Executor) ProcessTaskAssignedEvent(taskId uint64) {
	logging.Info(logTagExecutor+"Processing task assigned event", types.Training, "taskId", taskId)
	slog.Info(logTagExecutor+"Processing task assigned event", "taskId", taskId)
	queryClient := e.cosmosClient.NewInferenceQueryClient()
	req := types.QueryTrainingTaskRequest{Id: taskId}
	resp, err := queryClient.TrainingTask(e.cosmosClient.GetContext(), &req)

	if err != nil {
		logging.Error(logTagExecutor+"Error fetching task", types.Training, "taskId", taskId, "error", err)
		slog.Error(logTagExecutor+"Error fetching task", "taskId", taskId, "error", err)
		return
	}

	if resp.Task.Assignees == nil {
		logging.Error(logTagExecutor+"No assignees found for task", types.Training, "taskId", taskId)
		slog.Error(logTagExecutor+"No assignees found for task", "taskId", taskId)
		return
	}

	myNodes := make([]string, 0)
	for _, a := range resp.Task.Assignees {
		if a.Participant != e.cosmosClient.GetAccountAddress() {
			continue
		}
		logging.Info(logTagExecutor+"Found task assigned to me", types.Training, "taskId", taskId)
		slog.Info(logTagExecutor+"Found task assigned to me", "taskId", taskId)
		for _, node := range a.NodeIds {
			myNodes = append(myNodes, node)
		}
	}

	if len(myNodes) == 0 {
		logging.Info(logTagExecutor+"The task isn't assigned to me", types.Training, "taskId", taskId)
		slog.Info(logTagExecutor+"The task isn't assigned to me", "taskId", taskId)
		return
	}

	logging.Info(logTagExecutor+"The task is assigned to me", types.Training, "taskId", taskId, "nodes", myNodes)
	slog.Info(logTagExecutor+"The task is assigned to me", "taskId", taskId, "nodes", myNodes)

	rankedNodes, err := rankNodes(resp.Task)
	if err != nil {
		logging.Error(logTagExecutor+"Error ranking nodes", types.Training, "taskId", taskId, "error", err)
		slog.Error(logTagExecutor+"Error ranking nodes", "taskId", taskId, "error", err)
		return
	}

	masterNode, err := getMasterNode(e.ctx, rankedNodes, queryClient)
	if err != nil {
		logging.Error(logTagExecutor+"Error getting master node", types.Training, "taskId", taskId, "error", err)
		slog.Error(logTagExecutor+"Error getting master node", "taskId", taskId, "error", err)
		return
	}

	nodeRanks := make(map[string]int)
	for i, n := range rankedNodes {
		if n.participant == e.cosmosClient.GetAccountAddress() {
			nodeRanks[n.nodeId] = i
		}
	}

	logging.Info(logTagExecutor+"Starting training", types.Training, "taskId", taskId, "masterNode", masterNode.Host, "nodeRanks", nodeRanks, "worldSize", len(rankedNodes))
	slog.Info(logTagExecutor+"Starting training", "taskId", taskId, "masterNode", masterNode.Host, "nodeRanks", nodeRanks, "worldSize", len(rankedNodes))
	command := broker.NewStartTrainingCommand(
		taskId,
		masterNode.Host,
		len(rankedNodes),
		nodeRanks,
	)
	err = e.broker.QueueMessage(command)
	if err != nil {
		logging.Error(logTagExecutor+"Error starting training", types.Training, "taskId", taskId, "error", err)
		slog.Error(logTagExecutor+"Error starting training", "taskId", taskId, "error", err)
		return
	}

	success := <-command.Response
	if success {
		e.tasks[taskId] = struct{}{}
		logging.Info(logTagExecutor+"Training started", types.Training, "taskId", taskId)
		slog.Info(logTagExecutor+"Training started", "taskId", taskId)
	} else {
		logging.Error(logTagExecutor+"Error starting training", types.Training, "taskId", taskId)
		slog.Error(logTagExecutor+"Error starting training", "taskId", taskId)
	}
}

type nodeWithParticipant struct {
	participant string
	nodeId      string
}

func rankNodes(task *types.TrainingTask) ([]nodeWithParticipant, error) {
	if task == nil {
		slog.Error(logTagExecutor + "No task found")
		return nil, errors.New("no task found")
	}

	if task.Assignees == nil {
		slog.Error(logTagExecutor+"No assignees found for task", "taskId", task.Id)
		return nil, errors.New("no assignees found for task")
	}

	nodes := make([]nodeWithParticipant, 0)
	for _, a := range task.Assignees {
		for _, n := range a.NodeIds {
			nodes = append(nodes, nodeWithParticipant{
				participant: a.Participant,
				nodeId:      n,
			})
		}
	}

	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].participant == nodes[j].participant {
			return nodes[i].nodeId < nodes[j].nodeId
		}
		return nodes[i].participant < nodes[j].participant
	})

	return nodes, nil
}

func getMasterNode(ctx context.Context, rankedNodes []nodeWithParticipant, queryClient types.QueryClient) (*types.HardwareNode, error) {
	if len(rankedNodes) == 0 {
		slog.Error(logTagExecutor + "len(rankedNodes) is 0, can't pick master node")
		return nil, errors.New("len(rankedNodes) is 0, can't pick master node")
	}

	resp, err := queryClient.HardwareNodes(ctx, &types.QueryHardwareNodesRequest{Participant: rankedNodes[0].participant})
	if err != nil {
		slog.Error(logTagExecutor+"Error fetching hardware nodes", "participant", rankedNodes[0].participant, "error", err)
		return nil, err
	}

	for _, n := range resp.Nodes.HardwareNodes {
		if n.LocalId == rankedNodes[0].nodeId {
			return n, nil
		}
	}

	slog.Error(logTagExecutor+"Master node not found", "participant", rankedNodes[0].participant, "nodeId", rankedNodes[0].nodeId, "response.Nodes", resp.Nodes)
	return nil, errors.New("master node not found")
}

func (e *Executor) checkStatusRoutine() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-ticker.C:
			e.checkInProgressTasksOnChain()
			e.checkStatus()
		}
	}
}

func (e *Executor) checkInProgressTasksOnChain() {
	// 1. Get in progress tasks
	queryClient := e.cosmosClient.NewInferenceQueryClient()
	resp, err := queryClient.InProgressTrainingTasks(e.ctx, &types.QueryInProgressTrainingTasksRequest{})
	if err != nil {
		logging.Error(logTagExecutor+"Error fetching in progress tasks", types.Training, "error", err)
		return
	}

	// 2. Filter tasks that are assigned to me
	tasks := make([]*types.TrainingTask, 0)
	for _, t := range resp.Tasks {
		if t.Assignees == nil {
			continue
		}
		for _, a := range t.Assignees {
			if a.Participant == e.cosmosClient.GetAccountAddress() {
				tasks = append(tasks, t)
				break
			}
		}
	}

	// 3. For each task, check if it's already in the map
	for _, t := range tasks {
		if _, ok := e.tasks[t.Id]; !ok {
			logging.Info(logTagExecutor+"Task not found in the set", types.Training, "taskId", t.Id)
			// TODO: add a task to the map
			// e.tasks[t.Id] = struct{}{}
		} else {
			logging.Info(logTagExecutor+"Task found in the set", types.Training, "taskId", t.Id)
		}
	}

	// TODO: So here's the question:
	//  If task is absent in the map do we trigger it
	// 		or
	//	 do we have a separate routine that reads through the map each X seconds
	//	 and just sends some check nodes are doing training command to the server
	// I kind of like the option#2 more, because it's more resilient to failures
}

func (e *Executor) checkStatus() {
	// TODO: the routine that goes over each task in the map and makes sure nodes are actually busy wit it
}
