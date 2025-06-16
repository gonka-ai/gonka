package keeper_test

import (
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"testing"
)

func TestTrainKVRecord(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	taskId := uint64(10)
	participant := "participant1"
	keeper.SetTrainingKVRecord(ctx, &types.TrainingTaskKVRecord{
		TaskId:      taskId,
		Participant: participant,
		Key:         "key1",
		Value:       "value1",
	})
	keeper.SetTrainingKVRecord(ctx, &types.TrainingTaskKVRecord{
		TaskId:      taskId,
		Participant: participant,
		Key:         "key2",
		Value:       "value2",
	})

	records, err := keeper.ListTrainingKVRecords(ctx, taskId)
	if err != nil {
		t.Fatalf("Error listing training KV records: %s", err)
	}
	if len(records) != 2 {
		t.Fatalf("Expected 2 records, got %d", len(records))
	}
}
