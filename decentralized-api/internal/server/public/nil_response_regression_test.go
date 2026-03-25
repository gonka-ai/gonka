package public

import (
	"context"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// nilResponseQueryClient simulates a gRPC client that returns (nil, nil) for
// specific queries. This reproduces the conditions that cause nil pointer
// panics on mainnet when the chain RPC is slow or partially responsive.
type nilResponseQueryClient struct {
	types.QueryClient // embed — unoverridden methods will panic, but we only call overridden ones
}

func (c *nilResponseQueryClient) Params(ctx context.Context, in *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	return nil, nil // simulate nil response with no error
}

func (c *nilResponseQueryClient) GetModelPerTokenPrice(ctx context.Context, in *types.QueryGetModelPerTokenPriceRequest, opts ...grpc.CallOption) (*types.QueryGetModelPerTokenPriceResponse, error) {
	return nil, nil
}

func (c *nilResponseQueryClient) InferenceParticipant(ctx context.Context, in *types.QueryInferenceParticipantRequest, opts ...grpc.CallOption) (*types.QueryInferenceParticipantResponse, error) {
	return nil, nil
}

// TestEnforceDeveloperAccessGate_NilParamsResponse_NoPanic verifies that
// enforceDeveloperAccessGate does not panic when the Params gRPC query
// returns (nil, nil). Before the fix, paramsResp.Params.DeveloperAccessParams
// would dereference a nil pointer.
func TestEnforceDeveloperAccessGate_NilParamsResponse_NoPanic(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(&nilResponseQueryClient{})

	s := &Server{recorder: mockRecorder}

	require.NotPanics(t, func() {
		err := s.enforceDeveloperAccessGate(context.Background(), "gonka1test")
		// Should return nil (gate passes) rather than panicking
		require.NoError(t, err)
	})
}

// TestValidateRequester_NilPriceResponse_NoPanic verifies that
// validateRequester does not panic when GetModelPerTokenPrice returns (nil, nil).
// Before the fix, priceResponse.Found would dereference a nil pointer.
func TestValidateRequester_NilPriceResponse_NoPanic(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(&nilResponseQueryClient{})

	s := &Server{recorder: mockRecorder}

	// Create a minimal valid requester with enough balance
	requester := &types.QueryInferenceParticipantResponse{
		Balance: 1000000000,
		Pubkey:  "test-pubkey",
	}

	request := &ChatRequest{
		OpenAiRequest: OpenAiRequest{
			Model:     "test-model",
			MaxTokens: 100,
		},
		RequesterAddress: "gonka1test",
	}

	require.NotPanics(t, func() {
		// Should use legacy pricing fallback instead of panicking
		err := s.validateRequester(context.Background(), request, requester, 100)
		// May return error (e.g. signature validation) but must NOT panic
		_ = err
	})
}
