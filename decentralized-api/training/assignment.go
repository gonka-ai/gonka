package training

import (
	"context"
	"decentralized-api/cosmosclient"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"time"
)

type Assigner struct {
	cosmosClient     cosmosclient.CosmosMessageClient
	tendermintClient *cosmosclient.TendermintClient
	ctx              context.Context
	task             *taskToAssignState
}

type taskToAssignState struct {
	task *types.TrainingTask
}

const logTag = "[training-task-assigner] "

func NewAssigner(client cosmosclient.CosmosMessageClient, tendermintClient *cosmosclient.TendermintClient, ctx context.Context) *Assigner {
	assigner := &Assigner{
		cosmosClient:     client,
		tendermintClient: tendermintClient,
		ctx:              ctx,
		task:             nil,
	}

	// TODO: on startup do some queries to restore state (like tasks I was assigned)
	go assigner.claimTasksForAssignment()

	return assigner
}

func (a *Assigner) claimTasksForAssignment() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			if a.task == nil {
				a.tryClaimingTaskToAssign()
			}

			// Task could be assigned in the "if" above, thus we're rechecking here
			if a.task != nil {
				a.assignTask()
			}
		}
	}
}

func (a *Assigner) tryClaimingTaskToAssign() {
	chainStatus, err := a.tendermintClient.Status()
	if err != nil {
		slog.Error(logTag+"Failed to query chain status", "err", err)
	}

	if chainStatus.SyncInfo.CatchingUp {
		slog.Info(logTag + "Node is catching up, skipping task query")
		return
	}

	blockHeight := chainStatus.SyncInfo.LatestBlockHeight
	queryClient := a.cosmosClient.NewInferenceQueryClient()

	req := &types.QueryQueuedTrainingTasksRequest{}
	resp, err := queryClient.QueuedTrainingTasks(*a.cosmosClient.GetContext(), req)
	if err != nil {
		slog.Error(logTag+"Error querying for training tasks", "err", err)
		return
	}

	task := a.chooseTrainingTask(resp.Tasks, blockHeight)
	if task == nil {
		slog.Info(logTag + "No training tasks to claim for assignment")
		return
	}

	msg := inference.MsgClaimTrainingTaskForAssignment{
		TaskId: task.Id,
	}

	_, err = a.cosmosClient.ClaimTrainingTaskForAssignment(&msg)
	if err != nil {
		slog.Error(logTag+"Error claiming task for assignment", "err", err)
	}

	slog.Info(logTag+"Claimed task for assignment", "taskId", task.Id)
	a.task = &taskToAssignState{
		task: task,
	}
}

func (a *Assigner) findAlreadyClaimedTask(tasks []*types.TrainingTask) *types.TrainingTask {
	for _, task := range tasks {
		if task.Assigner == a.cosmosClient.GetAddress() {
			return task
		}
	}
	return nil
}

func (a *Assigner) chooseTrainingTask(tasks []*types.TrainingTask, currentBlockHeight int64) *types.TrainingTask {
	// This check handles the case of the network node being restarted while the task was already claimed by it
	taskAlreadyAssignedToMe := a.findAlreadyClaimedTask(tasks)
	if taskAlreadyAssignedToMe != nil {
		slog.Info(logTag+"Already claimed task found", "taskId", taskAlreadyAssignedToMe.Id)
		return taskAlreadyAssignedToMe
	}

	unclaimedTasks := make([]*types.TrainingTask, 0)
	for _, task := range tasks {
		if task.AssignedAtBlockHeight == 0 && (task.Assigner == "" || (uint64(currentBlockHeight)-task.ClaimedByAssignerAtBlockHeight) > keeper.TrainingTaskAssignmentDeadline) {
			unclaimedTasks = append(unclaimedTasks, task)
		}
	}

	if len(unclaimedTasks) == 0 {
		return nil
	}

	i := rand.Intn(len(unclaimedTasks))
	return unclaimedTasks[i]
}

func (a *Assigner) assignTask() {
	participants, err := getParticipantsWithHardwareNodes(a.ctx, a.cosmosClient.NewInferenceQueryClient())
	if err != nil {
		return
	}

	getParticipantListMatchingHardwareSpec(a.task.task.HardwareResources)
	_ = participants
}

type participantHardwareNodes struct {
	participant string
	weight      int64
	hardware    *types.HardwareNodes
}

func getParticipantsWithHardwareNodes(ctx context.Context, queryClient types.QueryClient) (map[string]participantHardwareNodes, error) {
	req := &types.QueryCurrentEpochGroupDataRequest{}
	resp, err := queryClient.CurrentEpochGroupData(ctx, req)
	if err != nil {
		slog.Error(logTag+"Error querying for current epoch group data", "err", err)
		return nil, err
	}

	participants := resp.EpochGroupData.ValidationWeights

	// FIXME: could be optimized if we queried only nodes of actual participants instead of ALL participants
	//  or maybe we should do some hardware nodes pruning
	r := &types.QueryHardwareNodesAllRequest{}
	hardwareNodes, err := queryClient.HardwareNodesAll(ctx, r)
	if err != nil {
		slog.Error(logTag+"Error querying for hardware nodes", "err", err)
		return nil, err
	}

	hardwareNodesByParticipant := make(map[string]*types.HardwareNodes)
	for _, nodes := range hardwareNodes.Nodes {
		hardwareNodesByParticipant[nodes.Participant] = nodes
	}

	participantsWithHardware := make(map[string]participantHardwareNodes)
	for _, participant := range participants {
		address := participant.MemberAddress
		participantsWithHardware[address] = participantHardwareNodes{
			participant: address,
			weight:      participant.Weight,
			hardware:    hardwareNodesByParticipant[address],
		}
	}

	return participantsWithHardware, nil
}

func getParticipantListMatchingHardwareSpec(hardwareSpec []*types.TrainingHardwareResources) {

}
