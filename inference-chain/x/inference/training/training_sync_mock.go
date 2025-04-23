package training

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// MockRunStore is an in-memory implementation of RunStore for testing.
type MockRunStore struct {
	mu            sync.RWMutex
	trainingTasks map[uint64]*types.TrainingTask
	// activity key: runId -> epoch -> participant -> nodeId -> activity
	activity map[uint64]map[int32]map[string]map[string]*types.TrainingTaskNodeEpochActivity
}

// NewMockRunStore creates a new instance of MockRunStore.
func NewMockRunStore() *MockRunStore {
	return &MockRunStore{
		trainingTasks: make(map[uint64]*types.TrainingTask),
		activity:      make(map[uint64]map[int32]map[string]map[string]*types.TrainingTaskNodeEpochActivity),
	}
}

// --- Helper methods for test setup ---

// SetTrainingTask adds or updates a training task in the mock store.
func (m *MockRunStore) SetTrainingTask(task *types.TrainingTask) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.trainingTasks[task.Id] = task
}

// SetParticipantActivity adds or updates a participant activity record in the mock store.
func (m *MockRunStore) SetParticipantActivity(activity *types.TrainingTaskNodeEpochActivity) {
	m.mu.Lock()
	defer m.mu.Unlock()

	runId := activity.TaskId
	epoch := activity.Epoch
	participant := activity.Participant
	nodeId := activity.NodeId

	if _, ok := m.activity[runId]; !ok {
		m.activity[runId] = make(map[int32]map[string]map[string]*types.TrainingTaskNodeEpochActivity)
	}
	if _, ok := m.activity[runId][epoch]; !ok {
		m.activity[runId][epoch] = make(map[string]map[string]*types.TrainingTaskNodeEpochActivity)
	}
	if _, ok := m.activity[runId][epoch][participant]; !ok {
		m.activity[runId][epoch][participant] = make(map[string]*types.TrainingTaskNodeEpochActivity)
	}
	m.activity[runId][epoch][participant][nodeId] = activity
}

// --- RunStore interface implementation ---

func (m *MockRunStore) GetRunState(ctx context.Context, runId uint64) (*types.TrainingTask, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	task, found := m.trainingTasks[runId]
	if !found {
		return nil, nil // Mimic keeper behavior: return nil if not found
	}
	// Return a copy to prevent modification of the stored object
	taskCopy := *task
	return &taskCopy, nil
}

func (m *MockRunStore) SaveRunState(ctx context.Context, state *types.TrainingTask) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Save a copy to prevent external modifications affecting the store
	stateCopy := *state
	m.trainingTasks[state.Id] = &stateCopy
	return nil
}

func (m *MockRunStore) GetEpochState(ctx context.Context, runId uint64, epoch int32) ([]*types.TrainingTaskNodeEpochActivity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	epochActivityMap, runFound := m.activity[runId]
	if !runFound {
		return []*types.TrainingTaskNodeEpochActivity{}, nil // Return empty slice if run not found
	}

	participantMap, epochFound := epochActivityMap[epoch]
	if !epochFound {
		return []*types.TrainingTaskNodeEpochActivity{}, nil // Return empty slice if epoch not found
	}

	var activities []*types.TrainingTaskNodeEpochActivity
	for _, nodeMap := range participantMap {
		for _, activity := range nodeMap {
			// Return copies
			activityCopy := *activity
			activities = append(activities, &activityCopy)
		}
	}

	// Sort for deterministic output (optional but good practice for tests)
	sortNodeActivity(activities)

	return activities, nil
}

func (m *MockRunStore) SaveEpochState(ctx context.Context, runId uint64, epoch int32, state []*types.TrainingTaskNodeEpochActivity) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Ensure the run and epoch maps exist
	if _, ok := m.activity[runId]; !ok {
		m.activity[runId] = make(map[int32]map[string]map[string]*types.TrainingTaskNodeEpochActivity)
	}
	// Clear existing state for this epoch before saving the new one
	m.activity[runId][epoch] = make(map[string]map[string]*types.TrainingTaskNodeEpochActivity)

	for _, activity := range state {
		if activity.Epoch != epoch || activity.TaskId != runId {
			// This indicates an inconsistency in the input data
			return fmt.Errorf("inconsistent activity record: expected run %d, epoch %d, got run %d, epoch %d",
				runId, epoch, activity.TaskId, activity.Epoch)
		}

		participant := activity.Participant
		nodeId := activity.NodeId

		if _, ok := m.activity[runId][epoch][participant]; !ok {
			m.activity[runId][epoch][participant] = make(map[string]*types.TrainingTaskNodeEpochActivity)
		}
		// Save a copy
		activityCopy := *activity
		m.activity[runId][epoch][participant][nodeId] = &activityCopy
	}
	return nil
}

func (m *MockRunStore) GetParticipantActivity(ctx context.Context, runId uint64, epoch int32, participant string, nodeId string) (*types.TrainingTaskNodeEpochActivity, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if epochMap, ok := m.activity[runId]; ok {
		if participantMap, ok := epochMap[epoch]; ok {
			if nodeMap, ok := participantMap[participant]; ok {
				if activity, found := nodeMap[nodeId]; found {
					// Return a copy
					activityCopy := *activity
					return &activityCopy, nil
				}
			}
		}
	}

	// Mimic keeper behavior: return error if not found
	return nil, nil
}

func (m *MockRunStore) SaveParticipantActivity(ctx context.Context, activity *types.TrainingTaskNodeEpochActivity) {
	// Use the dedicated helper for consistency
	m.SetParticipantActivity(activity)
}

// sortNodeActivity sorts a slice of TrainingTaskNodeEpochActivity for deterministic results.
func sortNodeActivity(activities []*types.TrainingTaskNodeEpochActivity) {
	sort.Slice(activities, func(i, j int) bool {
		if activities[i].TaskId != activities[j].TaskId {
			return activities[i].TaskId < activities[j].TaskId
		}
		if activities[i].Epoch != activities[j].Epoch {
			return activities[i].Epoch < activities[j].Epoch
		}
		if activities[i].Participant != activities[j].Participant {
			return activities[i].Participant < activities[j].Participant
		}
		return activities[i].NodeId < activities[j].NodeId
	})
}

func (m *MockRunStore) SetBarrier(ctx context.Context, barrier *types.TrainingTaskBarrier) {
	panic("implement me")
}

// Ensure MockRunStore implements RunStore interface
var _ RunStore = (*MockRunStore)(nil)
