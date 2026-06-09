package selfcheck

import (
	"decentralized-api/broker"
	"sync"

	"github.com/productscience/inference/x/inference/types"
)

// MockChainBridge implements broker.BrokerChainBridge with hardcoded
// responses that describe a single-participant, single-node, single-model
// network. It is the foundation for running the real broker in
// isolation during selfcheck — no chain RPC calls leave the process.
//
// All methods are safe for concurrent use.
type MockChainBridge struct {
	// ParticipantAddress is "self" — the participant address treated
	// as active in epoch group data. Must match what NewBroker is
	// constructed with via participant.CurrenParticipantInfo.
	ParticipantAddress string

	// ModelId is the single governance model used in the synthetic
	// epoch group data and reported by GetGovernanceModels.
	ModelId string

	// NodeId is the MLnode id assigned to ParticipantAddress in the
	// epoch group data. The selfcheck registers this id into the
	// broker so EpochMLNodes lookups succeed.
	NodeId string

	// EpochIndex is the epoch index reported by epoch group responses.
	EpochIndex uint64

	// SubmittedDiffs records every SubmitHardwareDiff call so the
	// Evaluator can assert the broker reported its hardware on startup.
	mu             sync.Mutex
	SubmittedDiffs []*types.MsgSubmitHardwareDiff
}

var _ broker.BrokerChainBridge = (*MockChainBridge)(nil)

func (m *MockChainBridge) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	return &types.QueryHardwareNodesResponse{
		Nodes: &types.HardwareNodes{HardwareNodes: nil},
	}, nil
}

func (m *MockChainBridge) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SubmittedDiffs = append(m.SubmittedDiffs, diff)
	return nil
}

func (m *MockChainBridge) GetBlockHash(height int64) (string, error) {
	return "selfcheck-fake-hash", nil
}

func (m *MockChainBridge) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	return &types.QueryModelsAllResponse{
		Model: []types.Model{{Id: m.ModelId}},
	}, nil
}

// GetCurrentEpochGroupData returns a parent epoch group that points at
// the single model subgroup. TotalWeight is non-zero so the v0.2.13
// broker considers the epoch populated.
func (m *MockChainBridge) GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error) {
	return &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:     m.EpochIndex,
			SubGroupModels: []string{m.ModelId},
			TotalWeight:    1,
		},
	}, nil
}

// GetEpochGroupDataByModelId returns:
//   - The parent group (modelId="") with the subgroup pointer.
//   - The model-specific subgroup with one ValidationWeights entry
//     assigning ParticipantAddress's NodeId to the model.
func (m *MockChainBridge) GetEpochGroupDataByModelId(epochIndex uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error) {
	if modelId == "" {
		return &types.QueryGetEpochGroupDataResponse{
			EpochGroupData: types.EpochGroupData{
				EpochIndex:     epochIndex,
				SubGroupModels: []string{m.ModelId},
				TotalWeight:    1,
			},
		}, nil
	}
	return &types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			EpochIndex:    epochIndex,
			ModelSnapshot: &types.Model{Id: modelId},
			ValidationWeights: []*types.ValidationWeight{
				{
					MemberAddress: m.ParticipantAddress,
					Weight:        1,
					MlNodes: []*types.MLNodeInfo{
						{NodeId: m.NodeId},
					},
				},
			},
		},
	}, nil
}

// GetPreservedNodesSnapshot returns an empty snapshot — selfcheck does
// not exercise preserved-node restoration. Added for v0.2.13 broker
// interface compatibility.
func (m *MockChainBridge) GetPreservedNodesSnapshot() (*types.QueryPreservedNodesSnapshotResponse, error) {
	return &types.QueryPreservedNodesSnapshotResponse{}, nil
}

func (m *MockChainBridge) GetParams() (*types.QueryParamsResponse, error) {
	return &types.QueryParamsResponse{
		Params: types.Params{
			EpochParams: m.epochParams(),
			// PoC params must advertise the model so the broker can resolve a
			// PoC model for the node and actually dispatch generation. Without
			// it the broker logs "Skipping PoC scheduling without resolvable
			// model" and the node never leaves STOPPED — which would make the
			// PoC-phase selfcheck stages vacuous.
			PocParams: &types.PocParams{
				Models: []*types.PoCModelConfig{
					{ModelId: m.ModelId, SeqLen: 1024},
				},
			},
		},
	}, nil
}

// epochParams produces a deliberately short PoC cycle (a handful of
// blocks per phase) so the EventDriver can step through a full epoch
// in under a second of simulated time.
func (m *MockChainBridge) epochParams() *types.EpochParams {
	return &types.EpochParams{
		EpochLength:           100,
		PocStageDuration:      10,
		PocExchangeDuration:   5,
		PocValidationDelay:    1,
		PocValidationDuration: 10,
		SetNewValidatorsDelay: 1,
	}
}

// SubmittedDiffsSnapshot returns a snapshot of recorded diffs so the
// Evaluator can inspect them without racing the broker.
func (m *MockChainBridge) SubmittedDiffsSnapshot() []*types.MsgSubmitHardwareDiff {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*types.MsgSubmitHardwareDiff, len(m.SubmittedDiffs))
	copy(out, m.SubmittedDiffs)
	return out
}
