package inference

import (
	"context"

	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

// ChainParamsProvider exposes chain validation parameters.
type ChainParamsProvider interface {
	LogprobsMode() string
}

// PayloadAuthClient is the narrow signing/query surface used by payload authentication.
type PayloadAuthClient interface {
	NewInferenceQueryClient() inferenceTypes.QueryClient
	GetAccountAddress() string
	GetSignerAddress() string
	// SignBytes signs payload-auth bytes. Implementations keep the private
	// key in memory and re-read the keyring only when its file changes.
	SignBytes(data []byte) (string, error)
}

// PayloadStore is the minimal interface for storing inference payloads.
type PayloadStore interface {
	Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error
}

// PayloadReader reads a stored payload back. A store without it cannot
// recover a lost finish: RecoveryStoredFirst runs the model and
// RecoveryStoredOnly fails with ErrNoStoredResponse.
type PayloadReader interface {
	Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) (prompt, response []byte, err error)
}
