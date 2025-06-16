package keeper

import (
	"encoding/binary"
	"fmt"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"strconv"
	"strings"
)

// CreateTask creates a new task, storing the full object under /tasks/{taskID}
// and adding its ID to the queued set.
func (k Keeper) CreateTask(ctx sdk.Context, task *types.TrainingTask) error {
	store := EmptyPrefixStore(ctx, &k)

	if task.Id == 0 {
		task.Id = k.GetNextTaskID(ctx)
	}

	taskKey := types.TrainingTaskFullKey(task.Id)
	if store.Has(taskKey) {
		return fmt.Errorf("task already exists. id = %d", task.Id)
	}

	bz := k.cdc.MustMarshal(task)
	store.Set(taskKey, bz)

	// Add the task ID to the queued set (we use an empty value).
	queuedKey := types.QueuedTrainingTaskFullKey(task.Id)
	store.Set(queuedKey, []byte{})

	return nil
}

// GetNextTaskID returns the next available task ID as a uint64.
// It reads the current sequence number from the KVStore, increments it,
// saves it back, and then returns the new value.
func (k Keeper) GetNextTaskID(ctx sdk.Context) uint64 {
	store := EmptyPrefixStore(ctx, &k)

	key := []byte(types.TrainingTaskSequenceKey)
	bz := store.Get(key)
	var nextId uint64
	if bz == nil {
		// Start at 1 if no sequence exists yet.
		nextId = 1
	} else {
		// Decode the current sequence and increment it.
		nextId = binary.BigEndian.Uint64(bz) + 1
	}

	// Store the new sequence number.
	newBz := make([]byte, 8)
	binary.BigEndian.PutUint64(newBz, nextId)
	store.Set(key, newBz)

	return nextId
}

// StartTask moves a task from the queued state to the in-progress state.
// It removes the task ID from the queued set and adds it to the in-progress set.
// Optionally, you can also update the task’s full object to record its new state.
func (k Keeper) StartTask(ctx sdk.Context, taskId uint64, assignees []*types.TrainingTaskAssignee) error {
	store := EmptyPrefixStore(ctx, &k)

	queuedKey := types.QueuedTrainingTaskFullKey(taskId)
	if !store.Has(queuedKey) {
		return fmt.Errorf("task is not queued. taskId = %d", taskId)
	}

	// Remove the task ID from the queued set.
	store.Delete(queuedKey)

	// Add the task ID to the in-progress set.
	inProgressKey := types.InProgressTrainingTaskFullKey(taskId)
	store.Set(inProgressKey, []byte{})

	// Optionally update the full task object to record the state change.
	taskKey := types.TrainingTaskFullKey(taskId)
	bz := store.Get(taskKey)
	if bz == nil {
		return types.ErrTrainingTaskNotFound
	}
	var task types.TrainingTask
	k.cdc.MustUnmarshal(bz, &task)

	// Here update the task object
	task.Assignees = assignees
	task.AssignedAtBlockHeight = uint64(ctx.BlockHeight())
	updatedBz := k.cdc.MustMarshal(&task)
	store.Set(taskKey, updatedBz)

	return nil
}

// CompleteTask marks a task as finished by removing it from the in-progress set.
// Optionally, you can also update the full object state to indicate completion.
func (k Keeper) CompleteTask(ctx sdk.Context, taskId uint64) error {
	store := EmptyPrefixStore(ctx, &k)

	inProgressKey := types.InProgressTrainingTaskFullKey(taskId)
	if !store.Has(inProgressKey) {
		return fmt.Errorf("task %d is not in progress", taskId)
	}

	// Remove the task ID from the in-progress set.
	store.Delete(inProgressKey)

	// Optionally update the task in the full object store to indicate completion.
	taskKey := types.TrainingTaskFullKey(taskId)
	bz := store.Get(taskKey)
	if bz == nil {
		return fmt.Errorf("task %d not found in full object store", taskId)
	}
	var task types.TrainingTask
	k.cdc.MustUnmarshal(bz, &task)

	// TODO: update the task object to mark it as "finished"
	updatedBz := k.cdc.MustMarshal(&task)
	store.Set(taskKey, updatedBz)

	return nil
}

// GetTrainingTask retrieves the full task object given its taskId.
func (k Keeper) GetTrainingTask(ctx sdk.Context, taskId uint64) (*types.TrainingTask, bool) {
	var task types.TrainingTask
	return GetValue(&k, ctx, &task, []byte(types.TrainingTaskKeyPrefix), types.TrainingTaskKey(taskId))
}

func (k Keeper) SetTrainingTask(ctx sdk.Context, task *types.TrainingTask) {
	SetValue(k, ctx, task, []byte(types.TrainingTaskKeyPrefix), types.TrainingTaskKey(task.Id))
}

// ListQueuedTasks returns all task IDs in the queued state by iterating over keys
// with the queued prefix. We assume that the task ID is stored as an 8-byte big-endian
// integer appended to the prefix.
func (k Keeper) ListQueuedTasks(ctx sdk.Context) ([]uint64, error) {
	return k.listIds(ctx, []byte(types.QueuedTrainingTaskKeyPrefix))
}

// ListInProgressTasks returns all task IDs that are in progress.
// Similar to ListQueuedTasks, we assume an 8-byte big-endian encoding.
func (k Keeper) ListInProgressTasks(ctx sdk.Context) ([]uint64, error) {
	return k.listIds(ctx, []byte(types.InProgressTrainingTaskKeyPrefix))
}

func (k Keeper) listIds(ctx sdk.Context, prefixKey []byte) ([]uint64, error) {
	store := PrefixStore(ctx, &k, prefixKey)
	iterator := store.Iterator(nil, nil)
	defer iterator.Close()

	var taskIDs []uint64
	for ; iterator.Valid(); iterator.Next() {
		keyBytes := iterator.Key()
		key := strings.TrimSuffix(string(keyBytes), "/")

		taskId, err := strconv.ParseUint(key, 10, 64)
		if err != nil {
			k.LogError("Error parsing task ID", types.Training, "key", key, "err", err)
			return nil, err
		}
		taskIDs = append(taskIDs, taskId)
	}
	return taskIDs, nil
}

func (k Keeper) GetTasks(ctx sdk.Context, ids []uint64) ([]*types.TrainingTask, error) {
	store := PrefixStore(ctx, &k, []byte(types.TrainingTaskKeyPrefix))
	tasks := make([]*types.TrainingTask, len(ids))
	for i, id := range ids {
		bz := store.Get(types.TrainingTaskKey(id))
		if bz == nil {
			return nil, fmt.Errorf("task %d not found", id)
		}
		var task types.TrainingTask
		k.cdc.MustUnmarshal(bz, &task)
		tasks[i] = &task
	}
	return tasks, nil
}

func (k Keeper) GetAllTrainingTasks(ctx sdk.Context) ([]*types.TrainingTask, error) {
	return GetAllValues(ctx, &k, []byte(types.TrainingTaskKeyPrefix), func() *types.TrainingTask {
		return &types.TrainingTask{}
	})
}
