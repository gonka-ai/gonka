package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k Keeper) GetTrainingKVRecord(ctx sdk.Context, taskId uint64, participant string, key string) (*types.TrainingTaskKVRecord, bool) {
	var task types.TrainingTaskKVRecord
	return GetValue(&k, ctx, &task, []byte{}, types.TrainingTaskKVRecordKey(taskId, participant, key))
}

func (k Keeper) SetTrainingKVRecord(ctx sdk.Context, record *types.TrainingTaskKVRecord) {
	SetValue(k, ctx, record, []byte{}, types.TrainingTaskKVRecordKey(record.TaskId, record.Participant, record.Key))
}

func (k Keeper) ListTrainingKVRecords(ctx sdk.Context, taskId uint64, participant string) ([]*types.TrainingTaskKVRecord, error) {
	keyPrefix := types.TrainingTaskKVParticipantRecordsKey(taskId, participant)
	return GetAllValues(ctx, k, keyPrefix, func() *types.TrainingTaskKVRecord {
		return &types.TrainingTaskKVRecord{}
	})
}
