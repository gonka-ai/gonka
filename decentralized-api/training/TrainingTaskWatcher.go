package training

import (
	"decentralized-api/cosmosclient"
	"github.com/cometbft/cometbft/libs/rand"
	"github.com/productscience/inference/x/inference/types"
	"log/slog"
	"time"
)

type TrainingTaskWatcher struct {
	cosmosClient     cosmosclient.CosmosMessageClient
	tendermintClient *cosmosclient.TendermintClient
}

// Number of blocks a person
const assignerDeadline = 300

func NewTrainingTaskWatcher(client cosmosclient.CosmosMessageClient, tendermintClient *cosmosclient.TendermintClient) *TrainingTaskWatcher {
	watcher := &TrainingTaskWatcher{
		cosmosClient:     client,
		tendermintClient: tendermintClient,
	}

	go watcher.watchTasks()

	return watcher
}

func (w TrainingTaskWatcher) watchTasks() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		chainStatus, err := w.tendermintClient.Status()
		if err != nil {
			slog.Error("Failed to query chain status", "err", err)
		}

		if chainStatus.SyncInfo.CatchingUp {
			slog.Info("Node is catching up, skipping task query")
			continue
		}

		blockHeight := chainStatus.SyncInfo.LatestBlockHeight
		queryClient := w.cosmosClient.NewInferenceQueryClient()

		req := &types.QueryQueuedTrainingTasksRequest{}
		resp, err := queryClient.QueuedTrainingTasks(*w.cosmosClient.GetContext(), req)
		if err != nil {
			slog.Error("Error querying for training tasks", "err", err)
			continue
		}

		task := chooseTrainingTask(resp.Tasks, blockHeight)
		_ = task

	}
}

func chooseTrainingTask(tasks []*types.TrainingTask, currentBlockHeight int64) *types.TrainingTask {
	filteredTasks := make([]*types.TrainingTask, 0)
	for _, task := range tasks {
		if task.Assigner == "" || (uint64(currentBlockHeight)-task.AssignerLockedAtBlockHeight) > assignerDeadline {
			filteredTasks = append(filteredTasks, task)
		}
	}

	if len(filteredTasks) == 0 {
		return nil
	}

	i := rand.Intn(len(filteredTasks))
	return filteredTasks[i]
}
