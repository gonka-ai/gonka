package mlnodeclient

import "context"

// MLNodeClient defines the interface for interacting with ML nodes
type MLNodeClient interface {
	// Training operations
	StartTraining(ctx context.Context, taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error
	GetTrainingStatus(ctx context.Context) error

	// Node state operations
	Stop(ctx context.Context) error
	NodeState(ctx context.Context) (*StateResponse, error)

	// PoC operations
	GetPowStatus(ctx context.Context) (*PowStatusResponse, error)
	InitGenerate(ctx context.Context, dto InitDto) error
	InitValidate(ctx context.Context, dto InitDto) error
	ValidateBatch(ctx context.Context, batch ProofBatch) error

	// Inference operations
	InferenceHealth(ctx context.Context) (bool, error)
	InferenceUp(ctx context.Context, model string, args []string) error
}

// Ensure Client implements MLNodeClient
var _ MLNodeClient = (*Client)(nil)
