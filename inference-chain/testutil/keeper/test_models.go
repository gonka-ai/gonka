package keeper

import "github.com/productscience/inference/x/inference/types"

const (
	GenesisModelsTest_QWQ  = "Qwen/QwQ-32B"
	GenesisModelsTest_QWEN = "Qwen/Qwen2.5-7B-Instruct"
)

// TODO: move somewhere else to avoid import issues
var GenesisModelsTest = map[string]types.Model{
	GenesisModelsTest_QWQ: {
		ProposedBy:             "genesis",
		Id:                     GenesisModelsTest_QWQ,
		UnitsOfComputePerToken: 1000,
		HfRepo:                 GenesisModelsTest_QWQ,
		HfCommit:               "976055f8c83f394f35dbd3ab09a285a984907bd0",
		ModelArgs:              []string{"--quantization", "fp8", "-kv-cache-dtype", "fp8"},
		VRam:                   32,
		ThroughputPerNonce:     1000,
		ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
	},
	GenesisModelsTest_QWEN: {
		ProposedBy:             "genesis",
		Id:                     GenesisModelsTest_QWEN,
		UnitsOfComputePerToken: 100,
		HfRepo:                 GenesisModelsTest_QWEN,
		HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
		ModelArgs:              []string{"--quantization", "fp8"},
		VRam:                   16,
		ThroughputPerNonce:     10000,
		ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
	},
}

func GenesisModelsTestList() []types.Model {
	models := make([]types.Model, 0, len(GenesisModelsTest))
	for _, model := range GenesisModelsTest {
		models = append(models, model)
	}
	return models
}
