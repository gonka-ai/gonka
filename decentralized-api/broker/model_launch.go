package broker

import (
	"context"
	"decentralized-api/mlnodeclient"
	"fmt"
	"slices"

	"github.com/productscience/inference/x/inference/types"
)

// ModelLaunchPlan is the model id and final argument list used to start
// inference on an MLnode.
type ModelLaunchPlan struct {
	ModelID string
	Args    []string
}

// StopBeforeModelLaunch applies the shared redeploy rule used before starting
// inference: an existing MLnode process must be stopped successfully first.
func StopBeforeModelLaunch(ctx context.Context, client mlnodeclient.MLNodeClient) error {
	return client.Stop(ctx)
}

// BuildModelLaunchPlan builds the final launch command for one governance
// model using the node's local model configuration.
func BuildModelLaunchPlan(model types.Model, nodeModels map[string]ModelArgs) (ModelLaunchPlan, error) {
	if model.Id == "" {
		return ModelLaunchPlan{}, fmt.Errorf("model id is empty")
	}

	var localArgs []string
	if localModelConfig, ok := nodeModels[model.Id]; ok {
		localArgs = localModelConfig.Args
	}

	return ModelLaunchPlan{
		ModelID: model.Id,
		Args:    MergeModelArgs(model.ModelArgs, localArgs),
	}, nil
}

// BuildConfiguredModelLaunchPlans builds launch plans for every model
// configured on a node. Each configured model must exist in governance.
// Plans are returned in stable model-id order.
func BuildConfiguredModelLaunchPlans(governanceModels []types.Model, nodeModels map[string]ModelArgs) ([]ModelLaunchPlan, error) {
	governanceByID := make(map[string]types.Model, len(governanceModels))
	for _, model := range governanceModels {
		governanceByID[model.Id] = model
	}

	modelIDs := make([]string, 0, len(nodeModels))
	for modelID := range nodeModels {
		modelIDs = append(modelIDs, modelID)
	}
	slices.Sort(modelIDs)

	plans := make([]ModelLaunchPlan, 0, len(modelIDs))
	for _, modelID := range modelIDs {
		model, ok := governanceByID[modelID]
		if !ok {
			return nil, fmt.Errorf("configured model %q not found in governance models", modelID)
		}
		plan, err := BuildModelLaunchPlan(model, nodeModels)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
	}
	return plans, nil
}
