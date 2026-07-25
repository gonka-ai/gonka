package chain

// Inference is a minimal chain inference record.
type Inference struct {
	ID        string
	Model     string
	Requester string
	Executor  string
	EpochID   uint64
}

// PayloadResponse holds the stored prompt/response for an inference.
type PayloadResponse struct {
	InferenceID       string
	PromptPayload     string
	ResponsePayload   string
	ExecutorSignature string
}

// ExecutorDestination identifies which validator instance should execute an inference.
type ExecutorDestination struct {
	// ValidatorAddress is the on-chain address of the selected executor validator.
	ValidatorAddress string
	// Endpoint is the public HTTP URL of the executor's inference-api instance.
	Endpoint string
}
