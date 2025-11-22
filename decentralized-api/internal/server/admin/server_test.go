package admin

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/file"
	"github.com/labstack/echo/v4"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"google.golang.org/grpc"
)

type mockParticipantInfo struct {
	mock.Mock
}

func (m *mockParticipantInfo) GetAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *mockParticipantInfo) GetPubKey() string {
	args := m.Called()
	return args.String(0)
}

// mockInferenceQueryClient is a mock implementation of the inference QueryClient for testing
type mockInferenceQueryClient struct {
	types.QueryClient
	mock.Mock
}

func (m *mockInferenceQueryClient) ModelsAll(ctx context.Context, in *types.QueryModelsAllRequest, opts ...grpc.CallOption) (*types.QueryModelsAllResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryModelsAllResponse), args.Error(1)
}

func setupTestServer(t *testing.T) (*Server, *apiconfig.ConfigManager, *mlnodeclient.MockClientFactory) {
	// 1. Config Manager
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	assert.NoError(t, err)
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	_, err = tmpFile.Write([]byte("nodes: []"))
	assert.NoError(t, err)
	tmpFile.Close()

	configManager := &apiconfig.ConfigManager{
		KoanProvider:   file.Provider(tmpFile.Name()),
		WriterProvider: apiconfig.NewFileWriteCloserProvider(tmpFile.Name()),
	}
	err = configManager.Load()
	assert.NoError(t, err)

	// 2. Broker Dependencies
	mockCosmos := &cosmosclient.MockCosmosMessageClient{}
	mockQueryClient := &mockInferenceQueryClient{}
	mockQueryClient.On("ModelsAll", mock.Anything, mock.Anything).Return(&types.QueryModelsAllResponse{
		Model: []types.Model{
			{Id: "test-model"},
		},
	}, nil)
	mockCosmos.On("NewInferenceQueryClient").Return(mockQueryClient)
	bridge := broker.NewBrokerChainBridgeImpl(mockCosmos, "")
	mockParticipant := &mockParticipantInfo{}
	mockClientFactory := mlnodeclient.NewMockClientFactory()

	mockParticipant.On("GetAddress").Return("test-participant")
	mockCosmos.On("GetContext").Return(context.Background())

	// 3. Broker
	nodeBroker := broker.NewBroker(bridge, nil, mockParticipant, "", mockClientFactory, configManager)

	// 4. Server
	s := NewServer(mockCosmos, nodeBroker, configManager, nil, nil)

	return s, configManager, mockClientFactory
}

func TestGetUpgradeStatus(t *testing.T) {
	s, configManager, _ := setupTestServer(t)

	t.Run("no upgrade plan", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/upgrade-status", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{"message":"No upgrade plan active"}`, rec.Body.String())
	})

	t.Run("with upgrade plan", func(t *testing.T) {
		version := "v1.2.3"
		configManager.SetUpgradePlan(apiconfig.UpgradePlan{NodeVersion: version})
		defer configManager.SetUpgradePlan(apiconfig.UpgradePlan{})

		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/upgrade-status", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.JSONEq(t, `{}`, rec.Body.String()) // No nodes, so empty report
	})
}

func TestPostVersionStatus(t *testing.T) {
	s, configManager, mockClientFactory := setupTestServer(t)

	nodeConfig := apiconfig.InferenceNodeConfig{
		Id:               "node-1",
		Host:             "localhost",
		InferencePort:    8080,
		InferenceSegment: "/api/v1",
		PoCPort:          8081,
		PoCSegment:       "/api/v1",
		MaxConcurrent:    3,
		Models: map[string]apiconfig.ModelConfig{
			"test-model": {Args: []string{}},
		},
	}
	nodes := configManager.GetNodes()
	nodes = append(nodes, nodeConfig)
	err := configManager.SetNodes(nodes)
	assert.NoError(t, err)
	respChan := s.nodeBroker.LoadNodeToBroker(&nodeConfig)
	select {
	case response := <-respChan:
		if response.Error != nil || response.Node == nil {
			t.Fatal("failed to register node - node validation failed")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for node to register")
	}

	t.Run("valid request", func(t *testing.T) {
		version := "v1.2.4"
		reqBody, _ := json.Marshal(versionStatusRequest{Version: version})
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/version-status", bytes.NewReader(reqBody))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()

		// Pre-configure the mock client to return an error
		pocURL := "http://localhost:8081/v1.2.4/api/v1"
		mockClient := mockClientFactory.CreateClient(pocURL, "").(*mlnodeclient.MockClient)
		mockClient.NodeStateError = errors.New("connection failed")

		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		expected := `{"node-1":{"is_alive":false,"error":"connection failed"}}`
		assert.JSONEq(t, expected, rec.Body.String())
	})

	t.Run("missing version", func(t *testing.T) {
		reqBody, _ := json.Marshal(versionStatusRequest{})
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/version-status", bytes.NewReader(reqBody))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		rec := httptest.NewRecorder()

		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusBadRequest, rec.Code)
	})
}
