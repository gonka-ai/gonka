package public

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"decentralized-api/cosmosclient"

	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/test/bufconn"
)

type fakeOpenRouterQueryServer struct {
	types.UnimplementedQueryServer
	epochGroupData      *types.EpochGroupData
	modelEpochGroupData map[string]*types.EpochGroupData
}

func (f *fakeOpenRouterQueryServer) CurrentEpochGroupData(ctx context.Context, req *types.QueryCurrentEpochGroupDataRequest) (*types.QueryCurrentEpochGroupDataResponse, error) {
	return &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: *f.epochGroupData,
	}, nil
}

func (f *fakeOpenRouterQueryServer) EpochGroupData(ctx context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	if data, ok := f.modelEpochGroupData[req.ModelId]; ok {
		return &types.QueryGetEpochGroupDataResponse{
			EpochGroupData: *data,
		}, nil
	}
	return nil, nil
}

func startOpenRouterGRPCServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
	t.Helper()
	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	types.RegisterQueryServer(server, srv)
	go func() { _ = server.Serve(listener) }()
	dialer := func(context.Context, string) (net.Conn, error) { return listener.Dial() }
	conn, err := grpc.DialContext(context.Background(), "bufnet", grpc.WithContextDialer(dialer), grpc.WithInsecure())
	require.NoError(t, err)
	cleanup := func() { server.Stop(); _ = listener.Close(); _ = conn.Close() }
	return conn, cleanup
}

func TestOpenRouterModelsEndpoint_Minimal(t *testing.T) {
	fq := &fakeOpenRouterQueryServer{
		epochGroupData: &types.EpochGroupData{
			EpochIndex:     1,
			SubGroupModels: []string{"test-model"},
		},
		modelEpochGroupData: map[string]*types.EpochGroupData{
			"test-model": {
				ModelSnapshot: &types.Model{
					Id:            "test-model",
					ContextWindow: 4096,
				},
			},
		},
	}

	conn, cleanup := startOpenRouterGRPCServer(t, fq)
	defer cleanup()

	mc := &cosmosclient.MockCosmosMessageClient{}
	mc.On("NewInferenceQueryClient").Return(types.NewQueryClient(conn))
	mc.On("GetContext").Return(context.Background())

	e := echo.New()
	s := &Server{e: e, recorder: mc}

	req := httptest.NewRequest(http.MethodGet, "/openrouter/api/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, s.getModelsOpenRouter(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp OpenRouterModelsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)

	model := resp.Data[0]
	require.Equal(t, "test-model", model.ID)
	require.Equal(t, "test-model", model.Name)
	require.Equal(t, uint64(4096), model.ContextLength)
	require.Equal(t, uint64(4096), model.MaxOutputLength)
	require.Equal(t, []string{"text"}, model.InputModalities)
	require.Equal(t, []string{"text"}, model.OutputModalities)
	require.NotNil(t, model.Pricing)
	require.Equal(t, openRouterSupportedSamplingParameters, model.SupportedSamplingParameters)
	require.Equal(t, openRouterSupportedFeatures, model.SupportedFeatures)

	mc.AssertExpectations(t)
}

func TestTransformCompletionsToChatRequest_Basic(t *testing.T) {
	req := &OpenRouterCompletionsRequest{
		Model:  "test-model",
		Prompt: StringOrArray{"Hello, world!"},
	}

	result := transformCompletionsToChatRequest(req)

	require.Equal(t, "test-model", result["model"])

	messages := result["messages"].([]map[string]string)
	require.Len(t, messages, 1)
	require.Equal(t, "user", messages[0]["role"])
	require.Equal(t, "Hello, world!", messages[0]["content"])
}

func TestStringOrArray_Unmarshal(t *testing.T) {
	var s StringOrArray

	err := json.Unmarshal([]byte(`"single"`), &s)
	require.NoError(t, err)
	require.Equal(t, StringOrArray{"single"}, s)

	err = json.Unmarshal([]byte(`["a", "b"]`), &s)
	require.NoError(t, err)
	require.Equal(t, StringOrArray{"a", "b"}, s)

	err = json.Unmarshal([]byte(`123`), &s)
	require.Error(t, err)
}

func TestTransformChatToCompletionResponse(t *testing.T) {
	chatResponse := `{
		"id": "chatcmpl-123",
		"object": "chat.completion",
		"created": 1234567890,
		"model": "test-model",
		"choices": [
			{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "Hello, world!"
				},
				"finish_reason": "stop"
			}
		],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15
		}
	}`

	result, err := transformChatToCompletionResponse([]byte(chatResponse))
	require.NoError(t, err)

	require.Equal(t, "chatcmpl-123", result.ID)
	require.Equal(t, "text_completion", result.Object)
	require.Equal(t, int64(1234567890), result.Created)
	require.Equal(t, "test-model", result.Model)

	require.Len(t, result.Choices, 1)
	require.Equal(t, "Hello, world!", result.Choices[0].Text)
	require.Equal(t, 0, result.Choices[0].Index)
	require.Equal(t, "stop", result.Choices[0].FinishReason)
	require.Nil(t, result.Choices[0].Logprobs)

	require.NotNil(t, result.Usage)
	require.Equal(t, 10, result.Usage.PromptTokens)
	require.Equal(t, 5, result.Usage.CompletionTokens)
	require.Equal(t, 15, result.Usage.TotalTokens)
}

func TestTransformChatChunkToCompletionChunk(t *testing.T) {
	chatChunk := `{
		"id": "chatcmpl-123",
		"object": "chat.completion.chunk",
		"created": 1234567890,
		"model": "test-model",
		"choices": [
			{
				"index": 0,
				"delta": {
					"content": "Hello"
				},
				"finish_reason": null
			}
		]
	}`

	result, err := transformChatChunkToCompletionChunk(chatChunk)
	require.NoError(t, err)

	var completionChunk CompletionChunk
	err = json.Unmarshal([]byte(result), &completionChunk)
	require.NoError(t, err)

	require.Equal(t, "chatcmpl-123", completionChunk.ID)
	require.Equal(t, "text_completion", completionChunk.Object)
	require.Len(t, completionChunk.Choices, 1)
	require.Equal(t, "Hello", completionChunk.Choices[0].Text)
	require.Equal(t, 0, completionChunk.Choices[0].Index)
	require.Nil(t, completionChunk.Choices[0].FinishReason)
}

func TestTransformChatChunkToCompletionChunk_WithUsage(t *testing.T) {
	chatChunk := `{
		"id": "chatcmpl-123",
		"object": "chat.completion.chunk",
		"created": 1234567890,
		"model": "test-model",
		"choices": [],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 20,
			"total_tokens": 30
		}
	}`

	result, err := transformChatChunkToCompletionChunk(chatChunk)
	require.NoError(t, err)

	var completionChunk CompletionChunk
	err = json.Unmarshal([]byte(result), &completionChunk)
	require.NoError(t, err)

	require.Equal(t, "text_completion", completionChunk.Object)
	require.NotNil(t, completionChunk.Usage)
	require.Equal(t, 10, completionChunk.Usage.PromptTokens)
	require.Equal(t, 20, completionChunk.Usage.CompletionTokens)
	require.Equal(t, 30, completionChunk.Usage.TotalTokens)
}
