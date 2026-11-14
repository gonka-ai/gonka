package session

import (
	"context"

	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

// InferenceQueryClientProvider is the narrow chain query surface the session needs.
type InferenceQueryClientProvider interface {
	NewInferenceQueryClient() inferenceTypes.QueryClient
}

// PayloadAuthClient is the narrow signing/query surface used by payload authentication.
type PayloadAuthClient interface {
	InferenceQueryClientProvider
	GetAccountAddress() string
	GetSignerAddress() string
	// SignBytes signs payload-auth bytes. Implementations keep the private
	// key in memory and re-read the keyring only when its file changes.
	SignBytes(data []byte) (string, error)
}

// PayloadStore is the minimal interface for retrieving inference payloads.
type PayloadStore interface {
	Retrieve(ctx context.Context, escrowId string, inferenceId, epochId uint64) (promptPayload, responsePayload []byte, err error)
}
