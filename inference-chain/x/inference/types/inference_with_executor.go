package types

// TODO: This is always going to be inaccurate, inferenceDetail needs to come from EpochGroup
func (iwe *InferenceWithExecutor) GetInferenceDetails() *InferenceDetail {
	return &InferenceDetail{
		InferenceId: iwe.Inference.InferenceId,
		Executor:    iwe.Executor.Address,
	}
}
