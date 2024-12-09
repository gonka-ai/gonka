package types

func (iwe *InferenceWithExecutor) GetInferenceDetails() *InferenceDetail {
	return &InferenceDetail{
		InferenceId:        iwe.Inference.InferenceId,
		Executor:           iwe.Executor.Address,
		ExecutorReputation: iwe.Executor.Reputation,
	}
}
