package types

// CreateInferenceStatsStorage extracts the relevant fields from an Inference to create InferenceStatsStorage
func CreateInferenceStatsStorage(inference Inference) InferenceStatsStorage {
	return InferenceStatsStorage{
		Index:                    inference.Index,
		InferenceId:              inference.InferenceId,
		Status:                   inference.Status,
		StartBlockTimestamp:      inference.StartBlockTimestamp,
		EndBlockTimestamp:        inference.EndBlockTimestamp,
		EpochId:                  inference.EpochId,
		EpochPocStartBlockHeight: inference.EpochPocStartBlockHeight,
		PromptTokenCount:         inference.PromptTokenCount,
		CompletionTokenCount:     inference.CompletionTokenCount,
		ActualCost:               inference.ActualCost,
		RequestedBy:              inference.RequestedBy,
		ExecutedBy:               inference.ExecutedBy,
		TransferredBy:            inference.TransferredBy,
		Model:                    inference.Model,
	}
}

// InferenceFromStatsStorage creates an Inference from InferenceStatsStorage with empty payload fields
func InferenceFromStatsStorage(stats InferenceStatsStorage) Inference {
	return Inference{
		Index:                    stats.Index,
		InferenceId:              stats.InferenceId,
		Status:                   stats.Status,
		StartBlockTimestamp:      stats.StartBlockTimestamp,
		EndBlockTimestamp:        stats.EndBlockTimestamp,
		EpochId:                  stats.EpochId,
		EpochPocStartBlockHeight: stats.EpochPocStartBlockHeight,
		PromptTokenCount:         stats.PromptTokenCount,
		CompletionTokenCount:     stats.CompletionTokenCount,
		ActualCost:               stats.ActualCost,
		RequestedBy:              stats.RequestedBy,
		ExecutedBy:               stats.ExecutedBy,
		TransferredBy:            stats.TransferredBy,
		Model:                    stats.Model,
		// All other fields remain empty/default
	}
}
