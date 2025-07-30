package keeper

// Mocks for simple Keepers, just store in memory as if in the KV Store
import (
	"context"
	"fmt"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// InMemoryEpochGroupDataKeeper is an in-memory implementation of EpochGroupDataKeeper.
type InMemoryEpochGroupDataKeeper struct {
	data map[string]types.EpochGroupData
	mu   sync.RWMutex
}

// generateKey creates a unique key from pocStartBlockHeight and modelId.
func (keeper *InMemoryEpochGroupDataKeeper) generateKey(pocStartBlockHeight uint64, modelId string) string {
	return fmt.Sprintf("%d/%s", pocStartBlockHeight, modelId)
}

// NewInMemoryEpochGroupDataKeeper creates a new instance of InMemoryEpochGroupDataKeeper.
func NewInMemoryEpochGroupDataKeeper() *InMemoryEpochGroupDataKeeper {
	return &InMemoryEpochGroupDataKeeper{
		data: make(map[string]types.EpochGroupData),
	}
}

// SetEpochGroupData stores or updates the given EpochGroupData.
func (keeper *InMemoryEpochGroupDataKeeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	key := keeper.generateKey(epochGroupData.PocStartBlockHeight, epochGroupData.ModelId)
	keeper.data[key] = epochGroupData
}

// GetEpochGroupData retrieves the EpochGroupData by PocStartBlockHeight and modelId.
func (keeper *InMemoryEpochGroupDataKeeper) GetEpochGroupData(ctx context.Context, pocStartBlockHeight uint64, modelId string) (val types.EpochGroupData, found bool) {
	keeper.mu.RLock()
	defer keeper.mu.RUnlock()
	key := keeper.generateKey(pocStartBlockHeight, modelId)
	val, found = keeper.data[key]
	return
}

// RemoveEpochGroupData removes the EpochGroupData by PocStartBlockHeight and modelId.
func (keeper *InMemoryEpochGroupDataKeeper) RemoveEpochGroupData(ctx context.Context, pocStartBlockHeight uint64, modelId string) {
	keeper.mu.Lock()
	defer keeper.mu.Unlock()
	key := keeper.generateKey(pocStartBlockHeight, modelId)
	delete(keeper.data, key)
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

	data1 := types.EpochGroupData{PocStartBlockHeight: 100, ModelId: ""}
	data2 := types.EpochGroupData{PocStartBlockHeight: 200, ModelId: "model1"}

	keeper.SetEpochGroupData(ctx, data1)
	keeper.SetEpochGroupData(ctx, data2)

	// Retrieve data
	if val, found := keeper.GetEpochGroupData(ctx, 100, ""); found {
		println("Found EpochGroupData with PocStartBlockHeight:", val.PocStartBlockHeight)
	}

	// Get all data
	allData := keeper.GetAllEpochGroupData(ctx)
	println("Total EpochGroupData count:", len(allData))

	// Remove data
	keeper.RemoveEpochGroupData(ctx, 100, "")

	// Verify removal
	if _, found := keeper.GetEpochGroupData(ctx, 100, ""); !found {
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

func (l *MockLogger) LogInfo(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "info", Keyvals: keyvals})
}

func (l *MockLogger) LogError(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "error", Keyvals: keyvals})
}

func (l *MockLogger) LogWarn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "warn", Keyvals: keyvals})
}

func (l *MockLogger) LogDebug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	l.logs = append(l.logs, Log{Msg: msg, Level: "debug", Keyvals: keyvals})
}

type InMemoryHardwareNodeKeeper struct {
	nodes map[string]*types.HardwareNodes
}

func NewInMemoryHardwareNodeKeeper() types.HardwareNodeKeeper {
	return &InMemoryHardwareNodeKeeper{
		nodes: make(map[string]*types.HardwareNodes),
	}
}

func (k *InMemoryHardwareNodeKeeper) GetHardwareNodes(ctx context.Context, address string) (*types.HardwareNodes, bool) {
	nodes, ok := k.nodes[address]
	return nodes, ok
}

func (k *InMemoryHardwareNodeKeeper) SetHardwareNodes(address string, nodes *types.HardwareNodes) {
	k.nodes[address] = nodes
}

type InMemoryModelKeeper struct {
	models map[string]*types.Model
}

func NewInMemoryModelKeeper() types.ModelKeeper {
	return &InMemoryModelKeeper{
		models: make(map[string]*types.Model),
	}
}

func (k *InMemoryModelKeeper) GetGovernanceModel(ctx context.Context, id string) (*types.Model, bool) {
	model, ok := k.models[id]
	return model, ok
}

func (k *InMemoryModelKeeper) GetGovernanceModels(ctx context.Context) ([]*types.Model, error) {
	var modelList []*types.Model
	for _, model := range k.models {
		modelList = append(modelList, model)
	}
	return modelList, nil
}

func (k *InMemoryModelKeeper) SetModel(model *types.Model) {
	k.models[model.Id] = model
}
