package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	keeper2 "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestModels(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	keeper.SetModel(ctx, &types.Model{Id: "1", ProposedBy: "user1", UnitsOfComputePerToken: 1})
	models, err := keeper.GetGovernanceModels(ctx)
	println("Models: ", models, "Error: ", err)
	modelValues, err := keeper2.PointersToValues(models)
	println("ModelValues: ", modelValues, "Error: ", err)
}

func TestSetDeleteSetModel(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)

	qwen7BModel := types.Model{
		ProposedBy:             "genesis",
		Id:                     "Qwen/Qwen2.5-7B-Instruct",
		UnitsOfComputePerToken: 100,
		HfRepo:                 "Qwen/Qwen2.5-7B-Instruct",
		HfCommit:               "a09a35458c702b33eeacc393d103063234e8bc28",
		ModelArgs:              []string{"--quantization", "fp8"},
		VRam:                   24,
		ThroughputPerNonce:     10000,
		ValidationThreshold:    &types.Decimal{Value: 85, Exponent: -2},
	}
	k.SetModel(ctx, &qwen7BModel)
	qwq32BModel := types.Model{
		ProposedBy:             "genesis",
		Id:                     "Qwen/QwQ-32B",
		UnitsOfComputePerToken: 1000,
		HfRepo:                 "Qwen/QwQ-32B",
		HfCommit:               "976055f8c83f394f35dbd3ab09a285a984907bd0",
		ModelArgs:              []string{"--quantization", "fp8", "--kv-cache-dtype", "fp8"},
		VRam:                   80,
		ThroughputPerNonce:     1000,
		ValidationThreshold:    &types.Decimal{Value: 75, Exponent: -2},
	}
	k.SetModel(ctx, &qwq32BModel)

	models, err := k.GetGovernanceModels(ctx)
	if err != nil {
		k.LogError("Failed to get governance models after setting new models", types.Upgrades, "error", err)
		t.Fatal(err)
	}
	for _, model := range models {
		k.LogInfo("Model after setting new models", types.Inferences, "model", model.Id, "VRam", model.VRam)
		if model.Id == "Qwen/Qwen2.5-7B-Instruct" && model.VRam != 24 {
			t.Errorf("Expected VRam for Qwen/Qwen2.5-7B-Instruct to be 1000, got %d", model.VRam)
		}
		if model.Id == "Qwen/QwQ-32B" && model.VRam != 80 {
			t.Errorf("Expected VRam for Qwen/QwQ-32B to be 2000, got %d", model.VRam)
		}
	}

	// Delete and set again!
	models, err = k.GetGovernanceModels(ctx)
	if err != nil {
		k.LogError("Failed to get governance models during upgrade", types.Upgrades, "error", err)
		t.Fatal(err)
	}

	k.LogInfo("Deleting all previous models", types.Inferences, "len(models)", len(models), "models", models)
	for _, model := range models {
		k.LogInfo("Deleting model", types.Inferences, "model", model.Id)
		k.DeleteGovernanceModel(ctx, model.Id)
	}

	k.LogInfo("Setting new models", types.Inferences, "models", models)
	qwen7BModel.VRam = 1000
	qwq32BModel.VRam = 2000
	k.SetModel(ctx, &qwen7BModel)
	k.SetModel(ctx, &qwq32BModel)
	models, err = k.GetGovernanceModels(ctx)
	if err != nil {
		k.LogError("Failed to get governance models after setting new models", types.Upgrades, "error", err)
		t.Fatal(err)
	}
	for _, model := range models {
		k.LogInfo("Model after setting new models", types.Inferences, "model", model.Id, "VRam", model.VRam)
		if model.Id == "Qwen/Qwen2.5-7B-Instruct" && model.VRam != 1000 {
			t.Errorf("Expected VRam for Qwen/Qwen2.5-7B-Instruct to be 1000, got %d", model.VRam)
		}
		if model.Id == "Qwen/QwQ-32B" && model.VRam != 2000 {
			t.Errorf("Expected VRam for Qwen/QwQ-32B to be 2000, got %d", model.VRam)
		}
	}
	m, found := k.GetGovernanceModel(ctx, "Qwen/Qwen2.5-7B-Instruct")
	if m == nil || !found {
		t.Fatal("Model Qwen/Qwen2.5-7B-Instruct not found after setting new models")
	} else {
		k.LogInfo("Found model Qwen/Qwen2.5-7B-Instruct", types.Inferences, "model", m.Id, "VRam", m.VRam)
	}
	if m.VRam != 1000 {
		t.Errorf("Expected VRam for Qwen/Qwen2.5-7B-Instruct to be 1000, got %d", m.VRam)
	}
	m, found = k.GetGovernanceModel(ctx, "Qwen/QwQ-32B")
	if m == nil || !found {
		t.Fatal("Model Qwen/QwQ-32B not found after setting new models")
	} else {
		k.LogInfo("Found model Qwen/QwQ-32B", types.Inferences, "model", m.Id, "VRam", m.VRam)
	}
	if m.VRam != 2000 {
		t.Errorf("Expected VRam for Qwen/QwQ-32B to be 2000, got %d", m.VRam)
	}
}
