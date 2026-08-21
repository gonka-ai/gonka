package epochgroup

import (
	"context"
	"fmt"
	"math"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestComputeMLNodeThroughput(t *testing.T) {
	t.Run("normal case", func(t *testing.T) {
		// Kimi-scale example from spec: 95298 * 1500 / 10000 = 14294
		got := computeMLNodeThroughput(95298, 1500, 10000)
		require.Equal(t, int64(14294), got)
	})

	t.Run("zero ThroughputPerNonce", func(t *testing.T) {
		require.Equal(t, int64(0), computeMLNodeThroughput(100, 0, 1000))
	})

	t.Run("zero UnitsOfComputePerToken", func(t *testing.T) {
		require.Equal(t, int64(0), computeMLNodeThroughput(100, 1000, 0))
	})

	t.Run("zero PocWeight", func(t *testing.T) {
		require.Equal(t, int64(0), computeMLNodeThroughput(0, 1000, 1000))
	})

	t.Run("negative PocWeight", func(t *testing.T) {
		require.Equal(t, int64(0), computeMLNodeThroughput(-1, 1000, 1000))
	})

	t.Run("large product near int64 bounds truncates to MaxInt64", func(t *testing.T) {
		// math.MaxInt64 * 2 / 1 overflows int64; must not wrap, clamp to MaxInt64
		got := computeMLNodeThroughput(math.MaxInt64, 2, 1)
		require.Equal(t, int64(math.MaxInt64), got)
	})

	t.Run("large values that still fit", func(t *testing.T) {
		// 1e15 * 1000 / 1000 = 1e15, well within int64
		got := computeMLNodeThroughput(1_000_000_000_000_000, 1000, 1000)
		require.Equal(t, int64(1_000_000_000_000_000), got)
	})
}

type mockModelKeeper struct {
	models map[string]*types.Model
}

func (m *mockModelKeeper) GetGovernanceModel(ctx context.Context, id string) (*types.Model, bool) {
	model, found := m.models[id]
	return model, found
}

func (m *mockModelKeeper) GetGovernanceModels(ctx context.Context) ([]*types.Model, error) {
	list := make([]*types.Model, 0, len(m.models))
	for _, model := range m.models {
		list = append(list, model)
	}
	return list, nil
}

type mockEpochGroupDataKeeper struct {
	data map[string]types.EpochGroupData
}

func epochGroupKey(epochIndex uint64, modelId string) string {
	return fmt.Sprintf("%d|%s", epochIndex, modelId)
}

func (m *mockEpochGroupDataKeeper) SetEpochGroupData(ctx context.Context, epochGroupData types.EpochGroupData) {
	if m.data == nil {
		m.data = make(map[string]types.EpochGroupData)
	}
	m.data[epochGroupKey(epochGroupData.EpochIndex, epochGroupData.ModelId)] = epochGroupData
}

func (m *mockEpochGroupDataKeeper) GetEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) (types.EpochGroupData, bool) {
	val, found := m.data[epochGroupKey(epochIndex, modelId)]
	return val, found
}

func (m *mockEpochGroupDataKeeper) RemoveEpochGroupData(ctx context.Context, epochIndex uint64, modelId string) {
	delete(m.data, epochGroupKey(epochIndex, modelId))
}

func (m *mockEpochGroupDataKeeper) GetAllEpochGroupData(ctx context.Context) []types.EpochGroupData {
	out := make([]types.EpochGroupData, 0, len(m.data))
	for _, v := range m.data {
		out = append(out, v)
	}
	return out
}

func TestUpdateEpochGroupWithNewMember_PopulatesTotalThroughput(t *testing.T) {
	modelId := "kimi-k2"
	groupDataKeeper := &mockEpochGroupDataKeeper{data: map[string]types.EpochGroupData{}}
	modelKeeper := &mockModelKeeper{
		models: map[string]*types.Model{
			modelId: {
				Id:                     modelId,
				ThroughputPerNonce:     1500,
				UnitsOfComputePerToken: 10000,
			},
		},
	}

	initial := types.EpochGroupData{
		EpochIndex: 1,
		ModelId:    modelId,
	}
	groupDataKeeper.SetEpochGroupData(context.Background(), initial)

	eg := &EpochGroup{
		Logger:          &mockLogger{},
		ModelKeeper:     modelKeeper,
		GroupDataKeeper: groupDataKeeper,
		GroupData:       &initial,
	}

	node1 := &types.MLNodeInfo{NodeId: "n1", PocWeight: 10000}
	node2 := &types.MLNodeInfo{NodeId: "n2", PocWeight: 20000}
	member := EpochMember{
		Address: "participant-1",
		Weight:  30000,
		Models:  []string{modelId},
		MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{node1, node2}}},
	}

	eg.updateEpochGroupWithNewMember(context.Background(), member, initial)

	// 10000*1500/10000 + 20000*1500/10000 = 1500 + 3000 = 4500
	require.Equal(t, int64(4500), eg.GroupData.TotalThroughput)
	require.Equal(t, int64(1500), node1.Throughput)
	require.Equal(t, int64(3000), node2.Throughput)

	stored, found := groupDataKeeper.GetEpochGroupData(context.Background(), 1, modelId)
	require.True(t, found)
	require.Equal(t, int64(4500), stored.TotalThroughput)
}

func TestUpdateEpochGroupWithNewMember_ZeroModelParamsLeavesThroughputZero(t *testing.T) {
	modelId := "legacy-model"
	groupDataKeeper := &mockEpochGroupDataKeeper{data: map[string]types.EpochGroupData{}}
	modelKeeper := &mockModelKeeper{
		models: map[string]*types.Model{
			modelId: {
				Id:                     modelId,
				ThroughputPerNonce:     0,
				UnitsOfComputePerToken: 10000,
			},
		},
	}

	initial := types.EpochGroupData{EpochIndex: 1, ModelId: modelId}
	eg := &EpochGroup{
		Logger:          &mockLogger{},
		ModelKeeper:     modelKeeper,
		GroupDataKeeper: groupDataKeeper,
		GroupData:       &initial,
	}

	// Pre-set a stale nonzero Throughput as a preserved node from a prior epoch
	// would carry. The zero-param path must overwrite it back to 0 so it does not
	// leak into TotalThroughput and defeat the TotalWeight fallback.
	node := &types.MLNodeInfo{NodeId: "n1", PocWeight: 100, Throughput: 999}
	member := EpochMember{
		Address: "p1",
		Weight:  100,
		Models:  []string{modelId},
		MlNodes: []*types.ModelMLNodes{{MlNodes: []*types.MLNodeInfo{node}}},
	}

	eg.updateEpochGroupWithNewMember(context.Background(), member, initial)
	require.Equal(t, int64(0), eg.GroupData.TotalThroughput)
	require.Equal(t, int64(0), node.Throughput)
}
