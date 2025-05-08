package types

import (
	"fmt"
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

	TrainingTaskKvRecordKeyPrefix = "TrainingTask/kvRecord/value/"
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

func TrainingTaskKVRecordKey(taskId uint64, key string) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/store/%s/value", taskId, key))
}

func TrainingTaskAllKVRecordsKey(taskId uint64) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/store", taskId))
}

func TrainingTaskNodeEpochActivityKey(taskId uint64, outerStep int32, participant string, nodeId string) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/heartbeat/%d/%s/%s", taskId, outerStep, participant, nodeId))
}

func TrainingTaskNodeEpochActivityEpochPrefix(taskId uint64, outerStep int32) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/heartbeat/%d", taskId, outerStep))
}

type TrainingTaskBarrierKey struct {
	TaskId      uint64
	BarrierId   string
	OuterStep   int32
	Participant string
	NodeId      string
}

func (b TrainingTaskBarrierKey) ToByteKey() []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/barrier/%s/%d/%s/%s/value", b.TaskId, b.BarrierId, b.OuterStep, b.Participant, b.NodeId))
}

type TrainingTaskBarrierEpochKey struct {
	TaskId    uint64
	BarrierId string
	OuterStep int32
}

func (b TrainingTaskBarrierEpochKey) ToByteKey() []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/barrier/%s/%d", b.TaskId, b.BarrierId, b.OuterStep))
}
