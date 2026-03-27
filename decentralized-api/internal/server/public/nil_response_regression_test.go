package public

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"decentralized-api/apiconfig"
	"decentralized-api/cosmosclient"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

// nilResponseQueryClient returns (nil, nil) for gRPC queries, simulating the
// condition when chain RPC is slow or partially responsive. This reproduces
// the exact panic from issue #876.
type nilResponseQueryClient struct {
	types.QueryClient // embed — only overridden methods are called
}

func (c *nilResponseQueryClient) Params(ctx context.Context, in *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	return nil, nil
}

func (c *nilResponseQueryClient) GetModelPerTokenPrice(ctx context.Context, in *types.QueryGetModelPerTokenPriceRequest, opts ...grpc.CallOption) (*types.QueryGetModelPerTokenPriceResponse, error) {
	return nil, nil
}

func (c *nilResponseQueryClient) InferenceParticipant(ctx context.Context, in *types.QueryInferenceParticipantRequest, opts ...grpc.CallOption) (*types.QueryInferenceParticipantResponse, error) {
	return nil, nil
}

// TestPostChat_NilGRPCResponse_NoPanic is an integration test that reproduces
// the full /v1/chat/completions failure path from issue #876. When the chain
// RPC returns nil gRPC responses, the handler must return an error — not panic.
//
// Without the fix: panics at post_chat_handler.go:250
//
//	(paramsResp.Params.DeveloperAccessParams on nil paramsResp)
//
// With the fix: returns nil (developer gate passes), continues to next step.
func TestPostChat_NilGRPCResponse_NoPanic(t *testing.T) {
	e := echo.New()

	// Build a signed-style request body
	body, _ := json.Marshal(map[string]interface{}{
		"model":      "Qwen3-235B-A22B-Instruct-2507-FP8",
		"max_tokens": 100,
		"messages":   []map[string]string{{"role": "user", "content": "hello"}},
	})

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "test-auth-key")
	req.Header.Set("X-Requester-Address", "gonka1testaddr")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Mock recorder: returns nil-response query client
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("GetAccountAddress").Return("gonka1taaddr")
	mockRecorder.On("NewInferenceQueryClient").Return(&nilResponseQueryClient{})

	s := &Server{
		e:             e,
		recorder:      mockRecorder,
		configManager: &apiconfig.ConfigManager{}, // zero-value: TA whitelist disabled
	}

	// The handler must not panic. It may return an error (e.g. participant
	// not found) but must never dereference a nil gRPC response.
	require.NotPanics(t, func() {
		_ = s.postChat(c)
	})
}

// TestEnforceDeveloperAccessGate_NilParamsResponse_NoPanic directly tests
// the function where the panic occurs. Confirms the nil guard works.
func TestEnforceDeveloperAccessGate_NilParamsResponse_NoPanic(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(&nilResponseQueryClient{})

	s := &Server{recorder: mockRecorder}

	require.NotPanics(t, func() {
		err := s.enforceDeveloperAccessGate(context.Background(), "gonka1test")
		require.NoError(t, err)
	})
}

// TestValidateRequester_NilPriceResponse_NoPanic tests that validateRequester
// does not panic when GetModelPerTokenPrice returns (nil, nil).
func TestValidateRequester_NilPriceResponse_NoPanic(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.On("NewInferenceQueryClient").Return(&nilResponseQueryClient{})

	s := &Server{recorder: mockRecorder}

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
		_ = s.validateRequester(context.Background(), request, requester, 100)
	})
}
