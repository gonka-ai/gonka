package inference

import (
	"context"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
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
	GetKeyring() *keyring.Keyring
}

// PayloadStore is the minimal interface for storing inference payloads.
type PayloadStore interface {
	Store(ctx context.Context, escrowId string, inferenceId, epochId uint64, promptPayload, responsePayload []byte) error
}
