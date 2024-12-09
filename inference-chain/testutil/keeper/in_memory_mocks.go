package keeper

// Mocks for simple Keepers, just store in memory as if in the KV Store
import (
	"context"
	"github.com/productscience/inference/x/inference/types"
	"sync"
)

// InMemoryEpochGroupDataKeeper is an in-memory implementation of EpochGroupDataKeeper.
type InMemoryEpochGroupDataKeeper struct {
	data map[uint64]types.EpochGroupData
	mu   sync.RWMutex
}

// NewInMemoryEpochGroupDataKeeper creates a new instance of InMemoryEpochGroupDataKeeper.
func NewInMemoryEpochGroupDataKeeper() *InMemoryEpochGroupDataKeeper {
	return &InMemoryEpochGroupDataKeeper{
		data: make(map[uint64]types.EpochGroupData),
	}
}

// SetEpochGroupData stores or updates the given EpochGroupData.
func (keeper *InMemoryEpochGroupDataKeeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	keeper.data[epochGroupData.PocStartBlockHeight] = epochGroupData
}

// GetEpochGroupData retrieves the EpochGroupData by PocStartBlockHeight.
func (keeper *InMemoryEpochGroupDataKeeper) GetEpochGroupData(ctx context.Context, pocStartBlockHeight uint64) (val types.EpochGroupData, found bool) {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	val, found = keeper.data[pocStartBlockHeight]
	return
}

// RemoveEpochGroupData removes the EpochGroupData by PocStartBlockHeight.
func (keeper *InMemoryEpochGroupDataKeeper) RemoveEpochGroupData(ctx context.Context, pocStartBlockHeight uint64) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	delete(keeper.data, pocStartBlockHeight)
}

// GetAllEpochGroupData retrieves all stored EpochGroupData.
func (keeper *InMemoryEpochGroupDataKeeper) GetAllEpochGroupData(ctx context.Context) []types.EpochGroupData {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	allData := make([]types.EpochGroupData, 0, len(keeper.data))
	for _, value := range keeper.data {
		allData = append(allData, value)
	}
	return allData
}

func main() {
	// Example usage
	ctx := context.Background()
	keeper := NewInMemoryEpochGroupDataKeeper()

	data1 := types.EpochGroupData{PocStartBlockHeight: 100}
	data2 := types.EpochGroupData{PocStartBlockHeight: 200}

	keeper.SetEpochGroupData(ctx, data1)
	keeper.SetEpochGroupData(ctx, data2)

	// Retrieve data
	if val, found := keeper.GetEpochGroupData(ctx, 100); found {
		println("Found EpochGroupData with PocStartBlockHeight:", val.PocStartBlockHeight)
	}

	// Get all data
	allData := keeper.GetAllEpochGroupData(ctx)
	println("Total EpochGroupData count:", len(allData))

	// Remove data
	keeper.RemoveEpochGroupData(ctx, 100)

	// Verify removal
	if _, found := keeper.GetEpochGroupData(ctx, 100); !found {
		println("EpochGroupData with PocStartBlockHeight 100 not found")
	}
}

// InMemoryParticipantKeeper is an in-memory implementation of ParticipantKeeper.
type InMemoryParticipantKeeper struct {
	data map[string]types.Participant
	mu   sync.RWMutex
}

// NewInMemoryParticipantKeeper creates a new instance of InMemoryParticipantKeeper.
func NewInMemoryParticipantKeeper() *InMemoryParticipantKeeper {
	return &InMemoryParticipantKeeper{
		data: make(map[string]types.Participant),
	}
}
func (keeper *InMemoryParticipantKeeper) ParticipantAll(ctx context.Context, req *types.QueryAllParticipantRequest) (*types.QueryAllParticipantResponse, error) {
	return &types.QueryAllParticipantResponse{Participant: keeper.GetAllParticipant(ctx)}, nil
}

// SetParticipant stores or updates the given Participant.
func (keeper *InMemoryParticipantKeeper) SetParticipant(ctx context.Context, participant types.Participant) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	keeper.data[participant.Index] = participant
}

// GetParticipant retrieves the Participant by index.
func (keeper *InMemoryParticipantKeeper) GetParticipant(ctx context.Context, index string) (val types.Participant, found bool) {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	val, found = keeper.data[index]
	return
}

// GetParticipants retrieves multiple Participants by their ids.
func (keeper *InMemoryParticipantKeeper) GetParticipants(ctx context.Context, ids []string) ([]types.Participant, bool) {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	participants := make([]types.Participant, 0, len(ids))
	for _, id := range ids {
		if participant, found := keeper.data[id]; found {
			participants = append(participants, participant)
		}
	}
	return participants, len(participants) == len(ids)
}

// RemoveParticipant removes the Participant by index.
func (keeper *InMemoryParticipantKeeper) RemoveParticipant(ctx context.Context, index string) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	delete(keeper.data, index)
}

// GetAllParticipant retrieves all stored Participants.
func (keeper *InMemoryParticipantKeeper) GetAllParticipant(ctx context.Context) []types.Participant {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	allParticipants := make([]types.Participant, 0, len(keeper.data))
	for _, value := range keeper.data {
		allParticipants = append(allParticipants, value)
	}
	return allParticipants
}

type Log struct {
	Msg     string
	Level   string
	Keyvals []interface{}
}

type MockLogger struct {
	logs []Log
}

func NewMockLogger() *MockLogger {
	return &MockLogger{
		logs: make([]Log, 0),
	}
}

func (l *MockLogger) LogInfo(msg string, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "info", Keyvals: keyvals})
}

func (l *MockLogger) LogError(msg string, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "error", Keyvals: keyvals})
}

func (l *MockLogger) LogWarn(msg string, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "warn", Keyvals: keyvals})
}

func (l *MockLogger) LogDebug(msg string, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "debug", Keyvals: keyvals})
}
