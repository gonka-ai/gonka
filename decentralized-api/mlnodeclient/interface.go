package mlnodeclient

// MLNodeClient defines the interface for interacting with ML nodes
type MLNodeClient interface {
	// Training operations
	StartTraining(taskId uint64, participant string, nodeId string, masterNodeAddr string, rank int, worldSize int) error
	GetTrainingStatus() error

	// Node state operations
	Stop() error
	NodeState() (*StateResponse, error)

	// PoC operations
	GetPowStatus() (*PowStatusResponse, error)
	InitGenerate(dto InitDto) error
	InitValidate(dto InitDto) error

	// Inference operations
	InferenceHealth() (bool, error)
	InferenceUp(model string, args []string) error
}

// Ensure Client implements MLNodeClient
var _ MLNodeClient = (*Client)(nil)
