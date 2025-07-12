package keeper

import "github.com/productscience/inference/x/inference/types"

/*
ModelListItem(

	proposedBy = "genesis",
	id = "Qwen/QwQ-32B",
	unitsOfComputePerToken = "1000",
	hfRepo = "Qwen/QwQ-32B",
	hfCommit = "976055f8c83f394f35dbd3ab09a285a984907bd0",
	modelArgs = listOf("--quantization", "fp8", "-kv-cache-dtype", "fp8"),
	vRam = "32",
	throughputPerNonce = "1000"

),
ModelListItem(

	proposedBy = "genesis",
	id = "Qwen/Qwen2.5-7B-Instruct",
	unitsOfComputePerToken = "100",
	hfRepo = "Qwen/Qwen2.5-7B-Instruct",
	hfCommit = "a09a35458c702b33eeacc393d103063234e8bc28",
	modelArgs = listOf("--quantization", "fp8"),
	vRam = "16",
	throughputPerNonce = "10000"

)
*/
var GenesisModelsTest = map[string]types.Model{
	"Qwen/QwQ-32B": {
		ProposedBy:             "genesis",
		Id:                     "Qwen/QwQ-32B",
		UnitsOfComputePerToken: 1000,
		HfRepo:                 "Qwen/QwQ-32B",
		HfCommit:               "976055f8c83f394f35dbd3ab09a285a984907bd0",
		ModelArgs:              []string{"--quantization", "fp8", "-kv-cache-dtype", "fp8"},
		VRam:                   32,
		ThroughputPerNonce:     1000,
	},
	"Qwen/Qwen2.5-7B-Instruct": {
		ProposedBy:             "genesis",
		Id:                     "Qwen/Qwen2.5-7B-Instruct",
		UnitsOfComputePerToken: 100,
		HfRepo:                 "Qwen/Qwen2.5-7B-Instruct",
		HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
		ModelArgs:              []string{"--quantization", "fp8"},
		VRam:                   16,
		ThroughputPerNonce:     10000,
	},
}
