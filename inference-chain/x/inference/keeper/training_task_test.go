package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestTrainingTaskLifecycle(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	checkPoolSize(t, ctx, keeper, 0, 0)

	task := types.TrainingTask{
		Id:                   0,
		RequestedBy:          "participant",
		CreatedAtBlockHeight: 10,
	}
	err := keeper.CreateTask(ctx, &task)
	if err != nil {
		t.Fatalf("Error creating task: %s", err)
	}

	checkPoolSize(t, ctx, keeper, 1, 0)

	queuedTaskIds, err := keeper.ListQueuedTasks(ctx)
	if err != nil {
		t.Fatalf("Error listing queued tasks: %s", err)
	}
	tasks, err := keeper.GetTasks(ctx, queuedTaskIds)
	if err != nil {
		t.Fatalf("Error getting tasks: %s", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("Unexpected number of tasks: %d", len(tasks))
	}
	if tasks[0].RequestedBy != task.RequestedBy {
		t.Errorf("Unexpected task: %+v", tasks[0])
	}

	assignees := []*types.TrainingTaskAssignee{
		{
			Participant: "participant",
			NodeIds:     []string{"node1", "node2"},
		},
	}

	err = keeper.StartTask(ctx, tasks[0].Id, assignees)
	if err != nil {
		t.Fatalf("Error starting task: %s", err)
	}
	checkPoolSize(t, ctx, keeper, 0, 1)
}

func checkPoolSize(t *testing.T, ctx sdk.Context, k keeper.Keeper, queuedSize, inProgressSize int) {
	if taskIds, err := k.ListQueuedTasks(ctx); err != nil {
		t.Errorf("Error listing queued tasks: %s", err)
	} else if len(taskIds) != queuedSize {
		t.Errorf("Unexpected number of tasks: %d", len(taskIds))
	}

	if taskIds, err := k.ListInProgressTasks(ctx); err != nil {
		t.Errorf("Error listing queued tasks: %s", err)
	} else if len(taskIds) != inProgressSize {
		t.Errorf("Unexpected number of tasks: %d", len(taskIds))
	}
}
