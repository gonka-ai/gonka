package types

import (
	"strconv"
)

const (
	// Actual training task objects are stored under keys like "TrainingTask/value/{taskID}".
	TrainingTaskKeyPrefix = "TrainingTask/value/"

	TrainingTaskSequenceKey = "TrainingTask/sequence/value/"

	// Set of training tasks IDs that are queued for processing.
	QueuedTrainingTaskKeyPrefix = "TrainingTask/queued/value/"

	// Set of training tasks IDs that are being processed at the moment
	InProgressTrainingTaskKeyPrefix = "TrainingTask/inProgress/value/"
)

func TrainingTaskKey(taskId uint64) []byte {
	return StringKey(strconv.FormatUint(taskId, 10))
}

func TrainingTaskFullKey(taskId uint64) []byte {
	key := TrainingTaskKeyPrefix + strconv.FormatUint(taskId, 10)
	return StringKey(key)
}

// getQueuedKey returns the key for a queued task.
func QueuedTrainingTaskFullKey(taskId uint64) []byte {
	key := QueuedTrainingTaskKeyPrefix + strconv.FormatUint(taskId, 10)
	return StringKey(key)
}

// getInProgressKey returns the key for an in-progress task.
func InProgressTrainingTaskFullKey(taskId uint64) []byte {
	key := InProgressTrainingTaskKeyPrefix + strconv.FormatUint(taskId, 10)
	return StringKey(key)
}
