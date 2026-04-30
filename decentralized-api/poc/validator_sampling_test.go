package poc

import (
	"testing"

	"decentralized-api/broker"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

func TestSampleLeafIndices_ZeroCount(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 0, 10)
	assert.Nil(t, result)
}

func TestSampleLeafIndices_ZeroSampleSize(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 1000, 0)
	assert.Nil(t, result)
}

func TestSampleLeafIndices_AllIndices(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 5, 10)
	assert.Len(t, result, 5)
	for i, idx := range result {
		assert.Equal(t, uint32(i), idx)
	}
}

func TestSampleLeafIndices_Subset(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 1000, 10)
	assert.Len(t, result, 10)
}

func TestSampleLeafIndices_NoDuplicates(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 1000, 100)
	seen := make(map[uint32]bool, len(result))
	for _, idx := range result {
		assert.False(t, seen[idx], "duplicate index: %d", idx)
		seen[idx] = true
	}
}

func TestSampleLeafIndices_ValidRange(t *testing.T) {
	var count uint32 = 500
	result := sampleLeafIndices("pk", "hash", 100, "model-a", count, 50)
	for _, idx := range result {
		assert.True(t, idx < count)
	}
}

func TestSampleLeafIndices_Deterministic(t *testing.T) {
	r1 := sampleLeafIndices("pk", "hash", 100, "model-a", 10000, 50)
	r2 := sampleLeafIndices("pk", "hash", 100, "model-a", 10000, 50)
	assert.Equal(t, r1, r2)
}

func TestSampleLeafIndices_DifferentSeed(t *testing.T) {
	r1 := sampleLeafIndices("pk1", "hash", 100, "model-a", 10000, 50)
	r2 := sampleLeafIndices("pk2", "hash", 100, "model-a", 10000, 50)
	assert.NotEqual(t, r1, r2)
}

func TestSampleLeafIndices_DifferentModel(t *testing.T) {
	r1 := sampleLeafIndices("pk", "hash", 100, "model-a", 10000, 50)
	r2 := sampleLeafIndices("pk", "hash", 100, "model-b", 10000, 50)
	assert.NotEqual(t, r1, r2)
}

func TestBuildValidationCallbackURL(t *testing.T) {
	url := buildValidationCallbackURL("http://localhost:8080/callback", "org/model-b")
	assert.Equal(t, "http://localhost:8080/callback/v2/poc-batches/org%252Fmodel-b", url)
}

func TestSampleLeafIndices_LargeCount(t *testing.T) {
	result := sampleLeafIndices("pk", "hash", 100, "model-a", 100_000_000, 200)
	assert.Len(t, result, 200)

	seen := make(map[uint32]bool, len(result))
	for _, idx := range result {
		assert.True(t, idx < 100_000_000)
		assert.False(t, seen[idx])
		seen[idx] = true
	}
}

func TestFilterValidationNodesForModel_UsesExplicitMembership(t *testing.T) {
	nodes := []broker.NodeResponse{
		{
			Node: broker.Node{Id: "node-a"},
			State: broker.NodeState{
				EpochMLNodes: map[string]types.MLNodeInfo{
					"model-a": {NodeId: "node-a"},
				},
			},
		},
		{
			Node: broker.Node{Id: "node-b"},
			State: broker.NodeState{
				EpochMLNodes: map[string]types.MLNodeInfo{
					"model-b": {NodeId: "node-b"},
				},
			},
		},
		{
			Node: broker.Node{Id: "node-c"},
			State: broker.NodeState{
				EpochMLNodes: map[string]types.MLNodeInfo{
					"model-a": {NodeId: "node-c"},
					"model-b": {NodeId: "node-c"},
				},
			},
		},
	}

	filtered := filterValidationNodesForModel(nodes, "model-a")
	if assert.Len(t, filtered, 2) {
		assert.Equal(t, "node-a", filtered[0].Node.Id)
		assert.Equal(t, "node-c", filtered[1].Node.Id)
	}
}

func TestFilterValidationNodesForModel_FallsBackToNodeModelsWithoutEpochData(t *testing.T) {
	nodes := []broker.NodeResponse{
		{
			Node: broker.Node{
				Id: "node-a",
				Models: map[string]broker.ModelArgs{
					"model-a": {},
				},
			},
			State: broker.NodeState{
				EpochModels: map[string]types.Model{
					"model-a": {Id: "model-a"},
				},
				EpochMLNodes: map[string]types.MLNodeInfo{},
			},
		},
	}

	filtered := filterValidationNodesForModel(nodes, "model-a")
	if assert.Len(t, filtered, 1) {
		assert.Equal(t, "node-a", filtered[0].Node.Id)
	}
}

func TestFilterValidationNodesForModel_ExcludesNodeWithDifferentModel(t *testing.T) {
	nodes := []broker.NodeResponse{
		{
			Node: broker.Node{
				Id: "node-a",
				Models: map[string]broker.ModelArgs{
					"model-a": {},
				},
			},
			State: broker.NodeState{
				EpochMLNodes: map[string]types.MLNodeInfo{
					"model-b": {NodeId: "node-a"},
				},
			},
		},
	}

	filtered := filterValidationNodesForModel(nodes, "model-a")
	assert.Empty(t, filtered)
}

func TestHasModelList(t *testing.T) {
	assert.False(t, hasModelList(false, map[string]*modelSamplingData{}))
	assert.False(t, hasModelList(true, map[string]*modelSamplingData{}))
	assert.True(t, hasModelList(true, map[string]*modelSamplingData{
		"model-a": {totalWeight: 10},
	}))
}
