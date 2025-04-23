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

func TrainingTaskKVRecordKey(taskId uint64, participant string, key string) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/store/%s/%s/value", taskId, participant, key))
}

func TrainingTaskKVParticipantRecordsKey(taskId uint64, participant string) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/store/%s", taskId, participant))
}

func TrainingTaskNodeEpochActivityKey(taskId uint64, epoch int32, participant string, nodeId string) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/heartbeat/%d/%s/%s", taskId, epoch, participant, nodeId))
}

func TrainingTaskNodeEpochActivityEpochPrefix(taskId uint64, epoch int32) []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/heartbeat/%d", taskId, epoch))
}

type TrainingTaskBarrierKey struct {
	BarrierId   string
	TaskId      uint64
	Participant string
	NodeId      string
	Epoch       int32
}

func (b TrainingTaskBarrierKey) ToByteKey() []byte {
	return StringKey(fmt.Sprintf("TrainingTask/sync/%d/barrier/%s/%d/%s/%s", b.TaskId, b.BarrierId, b.Epoch, b.Participant, b.NodeId))
}
