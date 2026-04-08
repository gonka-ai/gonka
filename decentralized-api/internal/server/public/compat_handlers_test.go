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

type fakeModelsQueryServer struct {
	types.UnimplementedQueryServer
	epochGroupData      *types.EpochGroupData
	modelEpochGroupData map[string]*types.EpochGroupData
}

func (f *fakeModelsQueryServer) CurrentEpochGroupData(ctx context.Context, req *types.QueryCurrentEpochGroupDataRequest) (*types.QueryCurrentEpochGroupDataResponse, error) {
	return &types.QueryCurrentEpochGroupDataResponse{
		EpochGroupData: *f.epochGroupData,
	}, nil
}

func (f *fakeModelsQueryServer) EpochGroupData(ctx context.Context, req *types.QueryGetEpochGroupDataRequest) (*types.QueryGetEpochGroupDataResponse, error) {
	if data, ok := f.modelEpochGroupData[req.ModelId]; ok {
		return &types.QueryGetEpochGroupDataResponse{
			EpochGroupData: *data,
		}, nil
	}
	return nil, nil
}

func startTestGRPCServer(t *testing.T, srv types.QueryServer) (*grpc.ClientConn, func()) {
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

func TestModelsEndpoint_Minimal(t *testing.T) {
	fq := &fakeModelsQueryServer{
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

	conn, cleanup := startTestGRPCServer(t, fq)
	defer cleanup()

	mc := &cosmosclient.MockCosmosMessageClient{}
	mc.On("NewInferenceQueryClient").Return(types.NewQueryClient(conn))
	mc.On("GetContext").Return(context.Background())

	e := echo.New()
	s := &Server{e: e, recorder: mc}

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, s.getModels(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var resp ModelsListResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Data, 1)

	model := resp.Data[0]
	require.Equal(t, "test-model", model.ID)
	require.Equal(t, "test-model", model.Name)
	require.NotZero(t, model.Created)
	require.Equal(t, uint64(4096), model.ContextLength)
	require.Equal(t, uint64(4096), model.MaxOutputLength)
	require.Equal(t, []string{"text"}, model.InputModalities)
	require.Equal(t, []string{"text"}, model.OutputModalities)

	mc.AssertExpectations(t)
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
