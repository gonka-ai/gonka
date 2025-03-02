package training

import (
	"decentralized-api/cosmosclient"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"time"
)

type TrainingTaskAssignmentClaimer struct {
	cosmosClient     cosmosclient.CosmosMessageClient
	tendermintClient *cosmosclient.TendermintClient
}

const logTag = "[training-task-watcher] "

func NewTrainingTaskAssignmentClaimer(client cosmosclient.CosmosMessageClient, tendermintClient *cosmosclient.TendermintClient) *TrainingTaskAssignmentClaimer {
	watcher := &TrainingTaskAssignmentClaimer{
		cosmosClient:     client,
		tendermintClient: tendermintClient,
	}

	// TODO: on startup do some quries to restore state (like tasks I was assigned)
	go watcher.watchTasks()

	return watcher
}

func (w TrainingTaskAssignmentClaimer) watchTasks() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		chainStatus, err := w.tendermintClient.Status()
		if err != nil {
			slog.Error(logTag+"Failed to query chain status", "err", err)
		}

		if chainStatus.SyncInfo.CatchingUp {
			slog.Info(logTag + "Node is catching up, skipping task query")
			continue
		}

		blockHeight := chainStatus.SyncInfo.LatestBlockHeight
		queryClient := w.cosmosClient.NewInferenceQueryClient()

		req := &types.QueryQueuedTrainingTasksRequest{}
		resp, err := queryClient.QueuedTrainingTasks(*w.cosmosClient.GetContext(), req)
		if err != nil {
			slog.Error(logTag+"Error querying for training tasks", "err", err)
			continue
		}

		task := chooseTrainingTask(resp.Tasks, blockHeight)
		if task == nil {
			slog.Info(logTag + "No training tasks to claim for assignment")
			continue
		}

		msg := inference.MsgClaimTrainingTaskForAssignment{
			TaskId: task.Id,
		}

		_, err = w.cosmosClient.ClaimTrainingTaskForAssignment(&msg)
		if err != nil {
			slog.Error(logTag+"Error claiming task for assignment", "err", err)
		}

		// TODO: set in some local state that you have a task claimed now!
	}
}

func chooseTrainingTask(tasks []*types.TrainingTask, currentBlockHeight int64) *types.TrainingTask {
	filteredTasks := make([]*types.TrainingTask, 0)
	for _, task := range tasks {
		if task.AssignedAtBlockHeight == 0 && (task.Assigner == "" || (uint64(currentBlockHeight)-task.ClaimedByAssignerAtBlockHeight) > keeper.TrainingTaskAssignmentDeadline) {
			filteredTasks = append(filteredTasks, task)
		}
	}

	if len(filteredTasks) == 0 {
		return nil
	}

	i := rand.Intn(len(filteredTasks))
	return filteredTasks[i]
}
