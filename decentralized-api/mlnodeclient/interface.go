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

	// PoC v1 operations (on-chain batches, requires Stop before transitions)
	InitGenerateV1(ctx context.Context, dto InitDtoV1) error
	InitValidateV1(ctx context.Context, dto InitDtoV1) error
	ValidateBatchV1(ctx context.Context, batch ProofBatchV1) error
	GetPowStatusV1(ctx context.Context) (*PowStatusResponseV1, error)

	// PoC v2 operations (off-chain artifacts, no Stop required)
	InitGenerateV2(ctx context.Context, req PoCInitGenerateRequestV2) (*PoCInitGenerateResponseV2, error)
	GenerateV2(ctx context.Context, req PoCGenerateRequestV2) (*PoCGenerateResponseV2, error)
	GetPowStatusV2(ctx context.Context) (*PoCStatusResponseV2, error)
	StopPowV2(ctx context.Context) (*PoCStopResponseV2, error)

	// Inference operations
	InferenceHealth(ctx context.Context) (bool, error)
	InferenceUp(ctx context.Context, model string, args []string) error
	GetLoadedModels(ctx context.Context) ([]string, error)

	// GPU operations
	GetGPUDevices(ctx context.Context) (*GPUDevicesResponse, error)
	GetGPUDriver(ctx context.Context) (*DriverInfo, error)

	// Model management operations
	CheckModelStatus(ctx context.Context, model Model) (*ModelStatusResponse, error)
	DownloadModel(ctx context.Context, model Model) (*DownloadStartResponse, error)
	DeleteModel(ctx context.Context, model Model) (*DeleteResponse, error)
	ListModels(ctx context.Context) (*ModelListResponse, error)
	GetDiskSpace(ctx context.Context) (*DiskSpaceInfo, error)
}

// Ensure Client implements MLNodeClient
var _ MLNodeClient = (*Client)(nil)
