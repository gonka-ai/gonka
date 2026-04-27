package devshard

import (
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	inferenceTypes "github.com/productscience/inference/x/inference/types"
)

// InferenceQueryClientProvider is the narrow chain query surface the devshard
// runtime needs from dapi's cosmos client.
type InferenceQueryClientProvider interface {
	NewInferenceQueryClient() inferenceTypes.QueryClient
}

// PayloadAuthClient is the narrow signing/query surface used by payload
// request authentication and payload response signing.
type PayloadAuthClient interface {
	InferenceQueryClientProvider
	GetAccountAddress() string
	GetSignerAddress() string
	GetKeyring() *keyring.Keyring
}

// ChainParamsProvider exposes chain validation parameters needed by the
// devshard execution and validation paths. Implementations handle caching
// and default-value fallback; callers can use returned values directly.
type ChainParamsProvider interface {
	LogprobsMode() string
}
