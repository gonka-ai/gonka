package inference

import (
	"context"

	"github.com/productscience/inference/x/inference/epochgroup"
	"github.com/productscience/inference/x/inference/types"
)

type executorValidationInfo struct {
	power      uint64
	reputation int32
}

func (am AppModule) processFinishedInferencesInBlock(
	ctx context.Context,
	blockHeight int64,
	effectiveEpoch *types.Epoch,
	currentEpochGroup *epochgroup.EpochGroup,
	params *types.Params,
) {
	pendingInferenceIDs, err := am.keeper.ListFinishedInferenceIDs(ctx)
	if err != nil {
		am.LogError("Failed to list finished inference IDs", types.Inferences, "error", err)
	}
	modelBlockLoads := make(map[string]uint64)
	modelBlockInferenceCounts := make(map[string]uint64)
	if len(pendingInferenceIDs) == 0 {
		if err := am.keeper.UpdateModelRollingWindows(
			ctx,
			currentEpochGroup.GroupData,
			params,
			modelBlockLoads,
			modelBlockInferenceCounts,
		); err != nil {
			am.LogError("Failed to update model rolling windows", types.Pricing, "error", err)
		}
		return
	}

	executorInfoByAddress := make(map[string]executorValidationInfo, len(currentEpochGroup.GroupData.ValidationWeights))
	for _, weight := range currentEpochGroup.GroupData.ValidationWeights {
		executorInfoByAddress[weight.MemberAddress] = executorValidationInfo{
			power:      uint64(weight.Weight),
			reputation: weight.Reputation,
		}
	}
	modelEpochGroupCache := make(map[string]*epochgroup.EpochGroup)
	processedCount := 0

	for _, inferenceID := range pendingInferenceIDs {
		inference, found := am.keeper.GetInference(ctx, inferenceID)
		if !found {
			am.LogWarn("Pending inference validation skipped: inference not found", types.Validation,
				"inference_id", inferenceID, "blockHeight", blockHeight)
			continue
		}
		if !inference.IsCompleted() {
			am.LogWarn("Pending inference validation skipped: inference not completed", types.Validation,
				"inference_id", inferenceID, "status", inference.Status.String(), "blockHeight", blockHeight)
			continue
		}
		modelBlockLoads[inference.Model] += inference.PromptTokenCount + inference.CompletionTokenCount
		modelBlockInferenceCounts[inference.Model]++

		modelEpochGroup, found := modelEpochGroupCache[inference.Model]
		if !found {
			var err error
			modelEpochGroup, err = currentEpochGroup.GetSubGroup(ctx, inference.Model)
			if err != nil {
				am.LogError("Pending inference validation skipped: unable to get model epoch group", types.EpochGroup,
					"inference_id", inferenceID, "model", inference.Model, "error", err)
				continue
			}
			modelEpochGroupCache[inference.Model] = modelEpochGroup
		}

		currentEpochGroup.GroupData.NumberOfRequests++
		trafficBasis := currentEpochGroup.GroupData.NumberOfRequests
		if currentEpochGroup.GroupData.PreviousEpochRequests > trafficBasis {
			trafficBasis = currentEpochGroup.GroupData.PreviousEpochRequests
		}

		executorInfo := executorInfoByAddress[inference.ExecutedBy]
		inferenceDetails := types.InferenceValidationDetails{
			InferenceId:          inference.InferenceId,
			ExecutorId:           inference.ExecutedBy,
			ExecutorReputation:   executorInfo.reputation,
			TrafficBasis:         uint64(trafficBasis),
			ExecutorPower:        executorInfo.power,
			EpochId:              effectiveEpoch.Index,
			Model:                inference.Model,
			TotalPower:           uint64(modelEpochGroup.GroupData.TotalWeight),
			CreatedAtBlockHeight: blockHeight,
		}
		if inferenceDetails.TotalPower == inferenceDetails.ExecutorPower {
			am.LogWarn("Executor Power equals Total Power", types.Validation,
				"model", inference.Model,
				"epoch_id", currentEpochGroup.GroupData.EpochGroupId,
				"epoch_start_block_height", currentEpochGroup.GroupData.PocStartBlockHeight,
				"group_id", modelEpochGroup.GroupData.EpochGroupId,
				"inference_id", inference.InferenceId,
				"executor_id", inferenceDetails.ExecutorId,
				"executor_power", inferenceDetails.ExecutorPower,
			)
		}

		am.LogDebug("Adding Inference Validation Details", types.Validation,
			"inference_id", inferenceDetails.InferenceId,
			"epoch_id", inferenceDetails.EpochId,
			"executor_id", inferenceDetails.ExecutorId,
			"executor_power", inferenceDetails.ExecutorPower,
			"executor_reputation", inferenceDetails.ExecutorReputation,
			"traffic_basis", inferenceDetails.TrafficBasis,
		)
		am.keeper.SetInferenceValidationDetails(ctx, inferenceDetails)
		processedCount++
	}
	if processedCount > 0 {
		am.keeper.SetEpochGroupData(ctx, *currentEpochGroup.GroupData)
	}

	am.LogInfo("Processed pending inference validations", types.Validation,
		"blockHeight", blockHeight,
		"queued", len(pendingInferenceIDs),
		"processed", processedCount,
	)
	if err := am.keeper.UpdateModelRollingWindows(
		ctx,
		currentEpochGroup.GroupData,
		params,
		modelBlockLoads,
		modelBlockInferenceCounts,
	); err != nil {
		am.LogError("Failed to update model rolling windows", types.Pricing, "error", err)
	}
}
