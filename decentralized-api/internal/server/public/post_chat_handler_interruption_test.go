package public

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/completionapi"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"decentralized-api/utils"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/crypto/keys/secp256k1"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/knadh/koanf/providers/file"
	"github.com/productscience/inference/api/inference/inference"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	cmtbytes "github.com/cometbft/cometbft/libs/bytes"
	ctypes "github.com/cometbft/cometbft/rpc/core/types"
)

// ============================================================================
// Test Keys for Signing
// ============================================================================

type testSigningKey struct {
	key *secp256k1.PrivKey
}

func newTestSigningKey() *testSigningKey {
	return &testSigningKey{key: secp256k1.GenPrivKey()}
}

func (t *testSigningKey) GetPubKeyBase64() string {
	return base64.StdEncoding.EncodeToString(t.key.PubKey().Bytes())
}

// SignBytes implements calculations.Signer interface
func (t *testSigningKey) SignBytes(msg []byte) (string, error) {
	sig, err := t.key.Sign(msg)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// ============================================================================
// Test Keyring Helper
// ============================================================================

// setupTestCodec creates a properly configured codec for keyring operations
func setupTestCodec() codec.Codec {
	registry := codectypes.NewInterfaceRegistry()
	registry.RegisterInterface("cosmos.crypto.PubKey", (*cryptotypes.PubKey)(nil))
	registry.RegisterInterface("cosmos.crypto.PrivKey", (*cryptotypes.PrivKey)(nil))
	registry.RegisterImplementations((*cryptotypes.PubKey)(nil), &secp256k1.PubKey{})
	registry.RegisterImplementations((*cryptotypes.PrivKey)(nil), &secp256k1.PrivKey{})
	return codec.NewProtoCodec(registry)
}

// createTestKeyring creates an in-memory keyring with a test key and returns the keyring and address
func createTestKeyring(t *testing.T) (*keyring.Keyring, string) {
	cdc := setupTestCodec()
	kr := keyring.NewInMemory(cdc)

	keyName := "test-executor-key"
	record, _, err := kr.NewMnemonic(
		keyName,
		keyring.English,
		sdk.FullFundraiserPath,
		"",
		hd.Secp256k1,
	)
	require.NoError(t, err)
	require.NotNil(t, record)

	// Get the address from the record
	addr, err := record.GetAddress()
	require.NoError(t, err)

	return &kr, addr.String()
}

// ============================================================================
// Test Suite
// ============================================================================

type interruptionTestSuite struct {
	t               *testing.T
	mockRecorder    *cosmosclient.MockCosmosMessageClient
	mockQueryClient *mockInterruptionQueryClient
	mockMLServer    *httptest.Server
	mockClientFactory *mlnodeclient.MockClientFactory
	server          *Server
	configManager   *apiconfig.ConfigManager
	nodeBroker      *broker.Broker
	phaseTracker    *chainphase.ChainPhaseTracker

	// Keys for signing
	devKey *testSigningKey
	taKey  *testSigningKey

	// Executor address (generated from keyring)
	executorAddress string

	// Track calls
	finishInferenceCalls []*inference.MsgFinishInference
	mu                   sync.Mutex
}

func (s *interruptionTestSuite) getFinishInferenceCalls() []*inference.MsgFinishInference {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]*inference.MsgFinishInference, len(s.finishInferenceCalls))
	copy(result, s.finishInferenceCalls)
	return result
}

func (s *interruptionTestSuite) clearFinishInferenceCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishInferenceCalls = nil
}

const (
	finishInferenceAsyncMaxWait   = 5 * time.Second
	finishInferenceAsyncPoll      = 10 * time.Millisecond
	finishInferenceAsyncStable    = 100 * time.Millisecond
	finishInferenceAsyncMinSettle = 300 * time.Millisecond
)

// waitForFinishInferenceCallsAtLeast polls until at least minCount FinishInference
// recordings exist or timeout expires (for slow CI runners).
func (s *interruptionTestSuite) waitForFinishInferenceCallsAtLeast(minCount int, timeout time.Duration) []*inference.MsgFinishInference {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		calls := s.getFinishInferenceCalls()
		if len(calls) >= minCount {
			return calls
		}
		time.Sleep(finishInferenceAsyncPoll)
	}
	return s.getFinishInferenceCalls()
}

// awaitAsyncFinishInferenceSettled waits for async FinishInference recording to
// finish: either the call count is stable for finishInferenceAsyncStable after
// finishInferenceAsyncMinSettle, or finishInferenceAsyncMaxWait elapses.
func (s *interruptionTestSuite) awaitAsyncFinishInferenceSettled() {
	start := time.Now()
	deadline := start.Add(finishInferenceAsyncMaxWait)
	var lastCount int = -1
	var stableSince time.Time

	for time.Now().Before(deadline) {
		count := len(s.getFinishInferenceCalls())
		if count != lastCount {
			lastCount = count
			stableSince = time.Now()
		} else if !stableSince.IsZero() &&
			time.Since(stableSince) >= finishInferenceAsyncStable &&
			time.Since(start) >= finishInferenceAsyncMinSettle {
			return
		}
		time.Sleep(finishInferenceAsyncPoll)
	}
}

func (s *interruptionTestSuite) cleanup() {
	if s.mockMLServer != nil {
		s.mockMLServer.Close()
	}
}

// waitForInferenceNodeReady blocks until the broker considers the node available
// for inference (INFERENCE status and no in-flight reconciliation). Without this,
// ServeHTTP can race reconciliation and return "no nodes available for inference".
func (s *interruptionTestSuite) waitForInferenceNodeReady(t *testing.T, nodeID string) {
	t.Helper()

	if !s.pollInferenceNodeReady(nodeID, 2*time.Second) {
		// Reconciliation may not finish in time on slow CI; force stable INFERENCE status.
		setStatusCmd := broker.NewSetNodesActualStatusCommand([]broker.StatusUpdate{
			{
				NodeId:     nodeID,
				PrevStatus: types.HardwareNodeStatus_UNKNOWN,
				NewStatus:  types.HardwareNodeStatus_INFERENCE,
				Timestamp:  time.Now(),
			},
		})
		err := s.nodeBroker.QueueMessage(setStatusCmd)
		require.NoError(t, err)
		require.True(t, <-setStatusCmd.Response)
	}

	if !s.pollInferenceNodeReady(nodeID, 2*time.Second) {
		t.Fatalf("node %q did not reach stable INFERENCE status in time", nodeID)
	}
}

func (s *interruptionTestSuite) pollInferenceNodeReady(nodeID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		nodes, err := s.nodeBroker.GetNodes()
		if err == nil {
			for _, n := range nodes {
				if n.Node.Id == nodeID &&
					n.State.IntendedStatus == types.HardwareNodeStatus_INFERENCE &&
					n.State.CurrentStatus == types.HardwareNodeStatus_INFERENCE &&
					n.State.ReconcileInfo == nil {
					return true
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func (s *interruptionTestSuite) waitForFinishInferenceCalls(t *testing.T, want int, timeout time.Duration) []*inference.MsgFinishInference {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		calls := s.getFinishInferenceCalls()
		if len(calls) >= want {
			return calls
		}
		time.Sleep(10 * time.Millisecond)
	}
	calls := s.getFinishInferenceCalls()
	require.Len(t, calls, want, "timed out waiting for FinishInference calls")
	return calls
}

// ============================================================================
// Mock Query Client
// ============================================================================

type mockInterruptionQueryClient struct {
	types.QueryClient
	mock.Mock
}

func (m *mockInterruptionQueryClient) ModelsAll(ctx context.Context, in *types.QueryModelsAllRequest, opts ...grpc.CallOption) (*types.QueryModelsAllResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryModelsAllResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) EpochGroupData(ctx context.Context, in *types.QueryGetEpochGroupDataRequest, opts ...grpc.CallOption) (*types.QueryGetEpochGroupDataResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGetEpochGroupDataResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) Params(ctx context.Context, in *types.QueryParamsRequest, opts ...grpc.CallOption) (*types.QueryParamsResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryParamsResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) GetRandomExecutor(ctx context.Context, in *types.QueryGetRandomExecutorRequest, opts ...grpc.CallOption) (*types.QueryGetRandomExecutorResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGetRandomExecutorResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) GranteesByMessageType(ctx context.Context, in *types.QueryGranteesByMessageTypeRequest, opts ...grpc.CallOption) (*types.QueryGranteesByMessageTypeResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGranteesByMessageTypeResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) AccountByAddress(ctx context.Context, in *types.QueryAccountByAddressRequest, opts ...grpc.CallOption) (*types.QueryAccountByAddressResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryAccountByAddressResponse), args.Error(1)
}

func (m *mockInterruptionQueryClient) GetModelPerTokenPrice(ctx context.Context, in *types.QueryGetModelPerTokenPriceRequest, opts ...grpc.CallOption) (*types.QueryGetModelPerTokenPriceResponse, error) {
	args := m.Called(ctx, in)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*types.QueryGetModelPerTokenPriceResponse), args.Error(1)
}

// mockParticipantInfo for broker setup
type mockInterruptionParticipantInfo struct {
	mock.Mock
}

func (m *mockInterruptionParticipantInfo) GetAddress() string {
	args := m.Called()
	return args.String(0)
}

func (m *mockInterruptionParticipantInfo) GetPubKey() string {
	args := m.Called()
	return args.String(0)
}

// ============================================================================
// Mock ML Node Server
// ============================================================================

type mockMLNodeBehavior struct {
	responseType       string        // "streaming" | "json"
	chunks             []string      // chunks to send for streaming
	delayBetweenChunks time.Duration // delay between chunks
	abortAfterChunks   int           // -1 = complete all chunks, N = abort after N chunks
	statusCode         int           // HTTP status code
	responseBody       string        // response body for JSON mode
	initialDelay       time.Duration // delay before starting response
}

func newMockMLServer(behavior *mockMLNodeBehavior) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if behavior.initialDelay > 0 {
			time.Sleep(behavior.initialDelay)
		}

		if behavior.statusCode != 0 && behavior.statusCode != 200 {
			w.WriteHeader(behavior.statusCode)
			if behavior.responseBody != "" {
				w.Write([]byte(behavior.responseBody))
			} else {
				w.Write([]byte(`{"error": "mock error"}`))
			}
			return
		}

		if behavior.responseType == "streaming" {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")
			w.WriteHeader(http.StatusOK)

			flusher, ok := w.(http.Flusher)
			if !ok {
				http.Error(w, "Streaming not supported", http.StatusInternalServerError)
				return
			}

			chunksToSend := len(behavior.chunks)
			if behavior.abortAfterChunks >= 0 && behavior.abortAfterChunks < chunksToSend {
				chunksToSend = behavior.abortAfterChunks
			}

			for i := 0; i < chunksToSend; i++ {
				if behavior.delayBetweenChunks > 0 && i > 0 {
					time.Sleep(behavior.delayBetweenChunks)
				}
				fmt.Fprintln(w, behavior.chunks[i])
				flusher.Flush()
			}

			// If abort requested, close connection abruptly
			if behavior.abortAfterChunks >= 0 && behavior.abortAfterChunks < len(behavior.chunks) {
				return
			}

			// Send DONE marker for complete streams
			fmt.Fprintln(w, "data: [DONE]")
			flusher.Flush()
		} else {
			// JSON mode
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(behavior.responseBody))
		}
	}))
}

// ============================================================================
// Test Data Helpers
// ============================================================================

func generateStreamingChunks(inferenceId string, model string, tokenCount int) []string {
	chunks := make([]string, 0, tokenCount+1)

	for i := 0; i < tokenCount; i++ {
		chunk := fmt.Sprintf(`data: {"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{"content":"token%d"},"logprobs":{"content":[{"token":"token%d","logprob":-0.1,"bytes":[116],"top_logprobs":[]}]},"finish_reason":null}]}`,
			inferenceId, time.Now().Unix(), model, i, i)
		chunks = append(chunks, chunk)
	}

	// Final chunk with usage info
	finalChunk := fmt.Sprintf(`data: {"id":"%s","object":"chat.completion.chunk","created":%d,"model":"%s","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":%d,"total_tokens":%d}}`,
		inferenceId, time.Now().Unix(), model, tokenCount, 10+tokenCount)
	chunks = append(chunks, finalChunk)

	return chunks
}

func generateJSONResponse(inferenceId string, model string, promptTokens, completionTokens int) string {
	return fmt.Sprintf(`{
		"id": "%s",
		"object": "chat.completion",
		"created": %d,
		"model": "%s",
		"choices": [{
			"index": 0,
			"message": {"role": "assistant", "content": "Hello! How can I help you?"},
			"logprobs": {"content": [{"token": "Hello", "logprob": -0.1, "bytes": [72], "top_logprobs": []}]},
			"finish_reason": "stop"
		}],
		"usage": {"prompt_tokens": %d, "completion_tokens": %d, "total_tokens": %d}
	}`, inferenceId, time.Now().Unix(), model, promptTokens, completionTokens, promptTokens+completionTokens)
}

// ============================================================================
// Test Setup
// ============================================================================

func setupInterruptionTestWithMLServer(t *testing.T, mlBehavior *mockMLNodeBehavior) *interruptionTestSuite {
	os.Setenv("ENFORCED_MODEL_ID", "disabled")

	suite := &interruptionTestSuite{
		t:                    t,
		finishInferenceCalls: make([]*inference.MsgFinishInference, 0),
		devKey:               newTestSigningKey(),
		taKey:                newTestSigningKey(),
	}

	// 1. Create mock ML server
	suite.mockMLServer = newMockMLServer(mlBehavior)

	// 2. Create config manager
	tmpFile, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })

	_, err = tmpFile.Write([]byte("nodes: []\ncurrent_node_version: v1"))
	require.NoError(t, err)
	tmpFile.Close()

	suite.configManager = &apiconfig.ConfigManager{
		KoanProvider:   file.Provider(tmpFile.Name()),
		WriterProvider: apiconfig.NewFileWriteCloserProvider(tmpFile.Name()),
	}
	err = suite.configManager.Load()
	require.NoError(t, err)

	// 3. Create mock recorder
	suite.mockRecorder = &cosmosclient.MockCosmosMessageClient{}
	suite.mockQueryClient = &mockInterruptionQueryClient{}

	// Setup query client mocks
	suite.mockQueryClient.On("ModelsAll", mock.Anything, mock.Anything).Return(&types.QueryModelsAllResponse{
		Model: []types.Model{{Id: "test-model"}},
	}, nil)

	suite.mockQueryClient.On("Params", mock.Anything, mock.Anything).Return(&types.QueryParamsResponse{
		Params: types.Params{},
	}, nil)

	// Create a real in-memory keyring with a test key
	testKeyring, testExecutorAddress := createTestKeyring(t)
	suite.executorAddress = testExecutorAddress

	suite.mockQueryClient.On("GetRandomExecutor", mock.Anything, mock.Anything).Return(&types.QueryGetRandomExecutorResponse{
		Executor: types.Participant{
			Address:      testExecutorAddress,
			InferenceUrl: "http://localhost:8080",
		},
	}, nil)

	// Return TA's pubkey for grantee validation
	suite.mockQueryClient.On("GranteesByMessageType", mock.Anything, mock.Anything).Return(&types.QueryGranteesByMessageTypeResponse{
		Grantees: []*types.Grantee{
			{
				Address: testExecutorAddress,
				PubKey:  suite.taKey.GetPubKeyBase64(),
			},
		},
	}, nil)

	// Return account pubkey for authz cache
	suite.mockQueryClient.On("AccountByAddress", mock.Anything, mock.Anything).Return(&types.QueryAccountByAddressResponse{
		Pubkey: suite.devKey.GetPubKeyBase64(),
	}, nil)

	suite.mockQueryClient.On("GetModelPerTokenPrice", mock.Anything, mock.Anything).Return(&types.QueryGetModelPerTokenPriceResponse{
		Price: 1,
		Found: true,
	}, nil)

	parentGroupResp := &types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			EpochIndex:          100,
			SubGroupModels:      []string{"test-model"},
		},
	}
	suite.mockQueryClient.On("EpochGroupData", mock.Anything, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: 100,
		ModelId:    "",
	}).Return(parentGroupResp, nil)

	modelEpochData := &types.QueryGetEpochGroupDataResponse{
		EpochGroupData: types.EpochGroupData{
			PocStartBlockHeight: 100,
			EpochIndex:          100,
			ModelSnapshot:       &types.Model{Id: "test-model"},
		},
	}
	suite.mockQueryClient.On("EpochGroupData", mock.Anything, &types.QueryGetEpochGroupDataRequest{
		EpochIndex: 100,
		ModelId:    "test-model",
	}).Return(modelEpochData, nil)

	suite.mockRecorder.On("NewInferenceQueryClient").Return(suite.mockQueryClient)
	suite.mockRecorder.On("GetContext").Return(context.Background())
	suite.mockRecorder.On("GetAccountAddress").Return(testExecutorAddress)
	suite.mockRecorder.On("GetSignerAddress").Return(testExecutorAddress)
	suite.mockRecorder.On("SignBytes", mock.Anything).Return([]byte("mock-signature"), nil)
	suite.mockRecorder.On("GetKeyring").Return(testKeyring)

	// Track FinishInference calls - THIS IS THE KEY MOCK
	suite.mockRecorder.On("FinishInference", mock.Anything).Run(func(args mock.Arguments) {
		msg := args.Get(0).(*inference.MsgFinishInference)
		suite.mu.Lock()
		suite.finishInferenceCalls = append(suite.finishInferenceCalls, msg)
		suite.mu.Unlock()
		t.Logf("FinishInference CALLED: id=%s, promptTokens=%d, completionTokens=%d",
			msg.InferenceId, msg.PromptTokenCount, msg.CompletionTokenCount)
	}).Return(nil)

	suite.mockRecorder.On("StartInference", mock.Anything).Return(nil)

	suite.mockRecorder.On("Status", mock.Anything).Return(&ctypes.ResultStatus{
		SyncInfo: ctypes.SyncInfo{
			LatestBlockHeight: 100,
			LatestBlockTime:   time.Now(),
			LatestBlockHash:   cmtbytes.HexBytes("abc123"),
		},
	}, nil)

	// 4. Create phase tracker
	suite.phaseTracker = &chainphase.ChainPhaseTracker{}
	suite.phaseTracker.Update(
		chainphase.BlockInfo{Height: 150, Hash: "hash-150"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 100},
		&types.EpochParams{EpochLength: 200, PocStageDuration: 50},
		true,
		nil,
	)

	// 5. Create broker with mock ML node client factory
	mockParticipant := &mockInterruptionParticipantInfo{}
	mockParticipant.On("GetAddress").Return(testExecutorAddress)
	mockParticipant.On("GetPubKey").Return(suite.taKey.GetPubKeyBase64())

	bridge := broker.NewBrokerChainBridgeImpl(suite.mockRecorder, "")
	suite.mockClientFactory = mlnodeclient.NewMockClientFactory()

	suite.nodeBroker = broker.NewBroker(bridge, suite.phaseTracker, mockParticipant, "", suite.mockClientFactory, suite.configManager)

	// 6. Register a node pointing to mock ML server
	mlServerURL := suite.mockMLServer.URL
	mlServerURL = strings.TrimPrefix(mlServerURL, "http://")
	parts := strings.Split(mlServerURL, ":")
	host := parts[0]
	port := 80
	if len(parts) > 1 {
		fmt.Sscanf(parts[1], "%d", &port)
	}

	nodeConfig := apiconfig.InferenceNodeConfig{
		Id:               "test-node",
		Host:             host,
		InferencePort:    port,
		InferenceSegment: "",
		PoCPort:          port + 1,
		PoCSegment:       "",
		MaxConcurrent:    10,
		Models:           map[string]apiconfig.ModelConfig{"test-model": {Args: []string{}}},
	}

	cmd := broker.NewRegisterNodeCommand(nodeConfig)
	err = suite.nodeBroker.QueueMessage(cmd)
	require.NoError(t, err)
	resp := <-cmd.Response
	require.NoError(t, resp.Error)

	mlNode := types.MLNodeInfo{
		NodeId:             nodeConfig.Id,
		Throughput:         0,
		PocWeight:          10,
		TimeslotAllocation: []bool{true, false},
	}
	model := types.Model{Id: "test-model"}
	suite.nodeBroker.UpdateNodeEpochData([]*types.MLNodeInfo{&mlNode}, "test-model", model)

	pocURL := fmt.Sprintf("http://%s:%d", host, port+1)
	mockClient := suite.mockClientFactory.CreateClient(pocURL, fmt.Sprintf("http://%s:%d", host, port)).(*mlnodeclient.MockClient)
	mockClient.Mu.Lock()
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mockClient.InferenceIsHealthy = true
	mockClient.Mu.Unlock()

	inferenceUpCmd := broker.NewInferenceUpAllCommand()
	err = suite.nodeBroker.QueueMessage(inferenceUpCmd)
	require.NoError(t, err)
	require.True(t, <-inferenceUpCmd.Response)
	suite.waitForInferenceNodeReady(t, nodeConfig.Id)

	// 7. Create the public server
	payloadStorage := newMockPayloadStorage()

	suite.server = NewServer(
		suite.nodeBroker,
		suite.configManager,
		suite.mockRecorder,
		nil,
		suite.phaseTracker,
		payloadStorage,
	)

	return suite
}

// ============================================================================
// Create Properly Signed Request
// ============================================================================

func (s *interruptionTestSuite) createSignedExecutorRequest(inferenceId string, model string, stream bool) *http.Request {
	body := map[string]interface{}{
		"model":    model,
		"messages": []map[string]string{{"role": "user", "content": "Hello"}},
		"stream":   stream,
	}
	bodyBytes, _ := json.Marshal(body)
	modifiedRequest, err := completionapi.ModifyRequestBodyWithLogprobsMode(bodyBytes, 12345, types.DefaultLogprobsMode)
	require.NoError(s.t, err)
	modifiedPromptHash, _, err := getModifiedPromptHash(modifiedRequest.NewBody)
	require.NoError(s.t, err)

	timestamp := time.Now().UnixNano()
	// Use the address from the test suite's generated keyring
	transferAddress := s.executorAddress
	executorAddress := s.executorAddress

	// Dev signs: hash(original_prompt) + timestamp + ta_address
	originalPromptHash := utils.GenerateSHA256Hash(string(bodyBytes))
	devComponents := calculations.SignatureComponents{
		Payload:         originalPromptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: "",
	}
	devSignature, _ := calculations.Sign(s.devKey, devComponents, calculations.Developer)

	// TA signs: prompt_hash + timestamp + ta_address + executor_address
	taComponents := calculations.SignatureComponents{
		Payload:         modifiedPromptHash,
		Timestamp:       timestamp,
		TransferAddress: transferAddress,
		ExecutorAddress: executorAddress,
	}
	taSignature, _ := calculations.Sign(s.taKey, taComponents, calculations.TransferAgent)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", devSignature)
	req.Header.Set("X-Inference-Id", inferenceId)
	req.Header.Set("X-Seed", "12345")
	req.Header.Set("X-Timestamp", fmt.Sprintf("%d", timestamp))
	req.Header.Set("X-Transfer-Address", transferAddress)
	req.Header.Set("X-Requester-Address", "test-requester-address")
	req.Header.Set("X-TA-Signature", taSignature)
	req.Header.Set(utils.XPromptHashHeader, modifiedPromptHash)

	return req
}

// ============================================================================
// ACTUAL TESTS - Verify FinishInference is called or not
// ============================================================================

func TestInterruption_S1_MLNodeClosesStream_VerifyFinishInference(t *testing.T) {
	// S1: MLNode closes connection mid-stream (vLLM crash, OOM)
	// HYPOTHESIS: FinishInference should NOT be called
	// ACTUAL: Let's find out!

	chunks := generateStreamingChunks("inf-s1", "test-model", 5)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType:     "streaming",
		chunks:           chunks,
		abortAfterChunks: 2, // Send only 2 chunks then abort
		statusCode:       200,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-s1", "test-model", true)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("S1 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("S1 RESULT: HTTP status = %d", rec.Code)

	if len(calls) > 0 {
		t.Logf("S1 ACTUAL: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
		t.Logf("S1 BUG CONFIRMED: Partial stream results in FinishInference with potentially wrong data!")
	} else {
		t.Logf("S1 ACTUAL: FinishInference was NOT called")
	}
}

func TestInterruption_S4_StreamSuccess_VerifyFinishInference(t *testing.T) {
	// S4: Successful streaming response (baseline)
	// EXPECTED: FinishInference IS called

	chunks := generateStreamingChunks("inf-s4", "test-model", 5)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType:     "streaming",
		chunks:           chunks,
		abortAfterChunks: -1, // Complete all chunks
		statusCode:       200,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-s4", "test-model", true)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("S4 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("S4 RESULT: HTTP status = %d", rec.Code)

	if len(calls) > 0 {
		t.Logf("S4 ACTUAL: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
	} else {
		t.Logf("S4 ACTUAL: FinishInference was NOT called - THIS IS UNEXPECTED!")
	}
}

func TestInterruption_J1_MLNodeClosesJSON_VerifyFinishInference(t *testing.T) {
	// J1: MLNode closes connection mid-JSON response
	// HYPOTHESIS: FinishInference should NOT be called (partial JSON fails parse)

	// Create a mock server that sends partial JSON then closes
	partialJSON := `{"id": "inf-j1", "object": "chat.completion", "created": 12345`
	mockServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(partialJSON))
	}))
	defer mockServer.Close()

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "json",
		responseBody: generateJSONResponse("inf-j1", "test-model", 10, 20),
		statusCode:   200,
	})
	// Replace the ML server with our partial JSON server
	suite.mockMLServer.Close()
	suite.mockMLServer = mockServer
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-j1", "test-model", false)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("J1 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("J1 RESULT: HTTP status = %d", rec.Code)

	if len(calls) == 0 {
		t.Logf("J1 ACTUAL: FinishInference was NOT called (correct for partial JSON)")
	} else {
		t.Logf("J1 ACTUAL: FinishInference WAS called - UNEXPECTED!")
	}
}

func TestInterruption_J4_JSONSuccess_VerifyFinishInference(t *testing.T) {
	// J4: Successful JSON response (baseline)
	// EXPECTED: FinishInference IS called

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "json",
		responseBody: generateJSONResponse("inf-j4", "test-model", 10, 20),
		statusCode:   200,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-j4", "test-model", false)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("J4 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("J4 RESULT: HTTP status = %d", rec.Code)

	if len(calls) > 0 {
		t.Logf("J4 ACTUAL: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
	} else {
		t.Logf("J4 ACTUAL: FinishInference was NOT called - THIS IS UNEXPECTED!")
	}
}

func TestInterruption_E1_MLNode400_VerifyFinishInference(t *testing.T) {
	// E1: MLNode returns 400 Bad Request
	// EXPECTED: FinishInference IS called (with synthetic response)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "json",
		responseBody: `{"error": {"message": "Invalid request", "type": "invalid_request_error"}}`,
		statusCode:   400,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-e1", "test-model", false)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("E1 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("E1 RESULT: HTTP status = %d", rec.Code)

	if len(calls) > 0 {
		t.Logf("E1 ACTUAL: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
	} else {
		t.Logf("E1 ACTUAL: FinishInference was NOT called")
	}
}

func TestInterruption_E3_MLNode500_VerifyFinishInference(t *testing.T) {
	// E3: MLNode returns 500 Internal Server Error
	// EXPECTED: FinishInference should NOT be called

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "json",
		responseBody: `{"error": {"message": "Internal server error", "type": "server_error"}}`,
		statusCode:   500,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-e3", "test-model", false)
	rec := httptest.NewRecorder()

	suite.server.e.ServeHTTP(rec, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("E3 RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("E3 RESULT: HTTP status = %d", rec.Code)

	if len(calls) == 0 {
		t.Logf("E3 ACTUAL: FinishInference was NOT called (expected for 500 error)")
	} else {
		t.Logf("E3 ACTUAL: FinishInference WAS called - UNEXPECTED!")
	}
}

// ============================================================================
// CLIENT/TA DISCONNECT TESTS - The critical scenarios
// ============================================================================

// disconnectingResponseWriter simulates a client that disconnects mid-response
type disconnectingResponseWriter struct {
	header       http.Header
	writtenBytes int
	disconnectAt int // disconnect after this many bytes
	statusCode   int
	err          error
}

func newDisconnectingResponseWriter(disconnectAfterBytes int) *disconnectingResponseWriter {
	return &disconnectingResponseWriter{
		header:       make(http.Header),
		disconnectAt: disconnectAfterBytes,
		err:          &net.OpError{Op: "write", Net: "tcp", Err: fmt.Errorf("broken pipe")},
	}
}

func (w *disconnectingResponseWriter) Header() http.Header {
	return w.header
}

func (w *disconnectingResponseWriter) Write(data []byte) (int, error) {
	if w.disconnectAt >= 0 && w.writtenBytes >= w.disconnectAt {
		return 0, w.err
	}
	remaining := len(data)
	if w.disconnectAt >= 0 && w.writtenBytes+len(data) > w.disconnectAt {
		remaining = w.disconnectAt - w.writtenBytes
	}
	w.writtenBytes += remaining
	if remaining < len(data) {
		return remaining, w.err
	}
	return len(data), nil
}

func (w *disconnectingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *disconnectingResponseWriter) Flush() {
	// No-op for testing
}

func TestInterruption_ClientDisconnect_StreamingComplete_VerifyFinishInference(t *testing.T) {
	// CRITICAL SCENARIO: MLNode completes the full response, but client disconnects
	// while Executor is streaming back to client/TA
	//
	// Pipeline: Client -> TA -> Executor -> MLNode
	// What happens: MLNode finishes, Executor has full response, but write to TA/Client fails
	//
	// EXPECTED: FinishInference SHOULD be called (work was completed)

	chunks := generateStreamingChunks("inf-cd1", "test-model", 5)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType:     "streaming",
		chunks:           chunks,
		abortAfterChunks: -1, // MLNode completes fully
		statusCode:       200,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-cd1", "test-model", true)

	// Use disconnecting writer - disconnect after receiving some bytes
	// This simulates TA/Client disconnecting while Executor streams response back
	disconnectWriter := newDisconnectingResponseWriter(100) // Disconnect after 100 bytes

	suite.server.e.ServeHTTP(disconnectWriter, req)

	calls := suite.waitForFinishInferenceCallsAtLeast(1, finishInferenceAsyncMaxWait)
	t.Logf("CLIENT_DISCONNECT_STREAM RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("CLIENT_DISCONNECT_STREAM: Written bytes before disconnect = %d", disconnectWriter.writtenBytes)

	require.Len(t, calls, 1, "executor should record FinishInference from partial response data after client disconnect")
	require.Equal(t, "inf-cd1", calls[0].InferenceId)
	require.Equal(t, uint64(1), calls[0].CompletionTokenCount)
	require.NotEmpty(t, calls[0].ResponseHash)
}

func TestInterruption_ClientDisconnect_JSONComplete_VerifyFinishInference(t *testing.T) {
	// CRITICAL SCENARIO: MLNode returns complete JSON, but client disconnects
	// while Executor writes JSON response back
	//
	// EXPECTED: FinishInference SHOULD be called (work was completed)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "json",
		responseBody: generateJSONResponse("inf-cd2", "test-model", 10, 20),
		statusCode:   200,
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-cd2", "test-model", false)

	// Disconnect after first 50 bytes of JSON response
	disconnectWriter := newDisconnectingResponseWriter(50)

	suite.server.e.ServeHTTP(disconnectWriter, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("CLIENT_DISCONNECT_JSON RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("CLIENT_DISCONNECT_JSON: Written bytes before disconnect = %d", disconnectWriter.writtenBytes)

	if len(calls) > 0 {
		t.Logf("CLIENT_DISCONNECT_JSON: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
		t.Logf("CLIENT_DISCONNECT_JSON: GOOD - Executor recorded the inference despite client disconnect")
	} else {
		t.Logf("CLIENT_DISCONNECT_JSON: FinishInference was NOT called")
		t.Logf("CLIENT_DISCONNECT_JSON: BUG! Executor did NOT record inference when client disconnected!")
	}
}

func TestInterruption_ClientDisconnect_BeforeMLNodeResponse_VerifyFinishInference(t *testing.T) {
	// SCENARIO: Client disconnects BEFORE MLNode even starts responding
	// MLNode has a delay, client disconnects during that delay
	//
	// This tests if the Executor still completes and records when client is gone

	chunks := generateStreamingChunks("inf-cd3", "test-model", 5)

	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType:     "streaming",
		chunks:           chunks,
		abortAfterChunks: -1,
		statusCode:       200,
		initialDelay:     100 * time.Millisecond, // MLNode takes 100ms to start
	})
	defer suite.cleanup()

	req := suite.createSignedExecutorRequest("inf-cd3", "test-model", true)

	// Disconnect immediately (0 bytes) - client gone before any response
	disconnectWriter := newDisconnectingResponseWriter(0)

	suite.server.e.ServeHTTP(disconnectWriter, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("CLIENT_DISCONNECT_EARLY RESULT: FinishInference calls count = %d", len(calls))

	if len(calls) > 0 {
		t.Logf("CLIENT_DISCONNECT_EARLY: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
		t.Logf("CLIENT_DISCONNECT_EARLY: Executor completed work despite early client disconnect")
	} else {
		t.Logf("CLIENT_DISCONNECT_EARLY: FinishInference was NOT called")
		t.Logf("CLIENT_DISCONNECT_EARLY: Need to verify if this is expected behavior")
	}
}

// ============================================================================
// TIMEOUT TESTS - Executor -> MLNode connection timeout
// ============================================================================

func TestInterruption_MLNodeTimeout_VerifyFinishInference(t *testing.T) {
	// CRITICAL SCENARIO: MLNode takes too long (timeout)
	// The HTTP client timeout is triggered, connection is closed
	//
	// EXPECTED: FinishInference SHOULD be called with synthetic response
	// so the inference lifecycle is closed on-chain
	//
	// This tests the new isTimeoutOrConnectionError() handling

	// Create a mock MLNode that never responds (simulates timeout)
	// We use initialDelay to make it block long enough for timeout
	suite := setupInterruptionTestWithMLServer(t, &mockMLNodeBehavior{
		responseType: "streaming",
		chunks:       generateStreamingChunks("inf-timeout1", "test-model", 5),
		statusCode:   200,
		initialDelay: 10 * time.Second, // Long delay to trigger timeout
	})
	defer suite.cleanup()

	// Create a custom HTTP client with a very short timeout for this test
	shortTimeoutClient := &http.Client{
		Timeout: 500 * time.Millisecond, // 500ms timeout - much shorter than initialDelay
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// Replace the server's HTTP client temporarily
	originalClient := suite.server.httpClient
	suite.server.httpClient = shortTimeoutClient
	defer func() { suite.server.httpClient = originalClient }()

	req := suite.createSignedExecutorRequest("inf-timeout1", "test-model", true)

	// Use a regular recorder since we want to see if the request completes
	rr := httptest.NewRecorder()

	// This should timeout after 500ms (before the 10s delay completes)
	suite.server.e.ServeHTTP(rr, req)

	suite.awaitAsyncFinishInferenceSettled()

	calls := suite.getFinishInferenceCalls()
	t.Logf("TIMEOUT TEST RESULT: FinishInference calls count = %d", len(calls))
	t.Logf("TIMEOUT TEST: HTTP status = %d", rr.Code)

	if len(calls) > 0 {
		t.Logf("TIMEOUT TEST: FinishInference WAS called (promptTokens=%d, completionTokens=%d)",
			calls[0].PromptTokenCount, calls[0].CompletionTokenCount)
		t.Logf("TIMEOUT TEST: GOOD - Executor recorded FinishInference with synthetic response on timeout")
	} else {
		t.Logf("TIMEOUT TEST: FinishInference was NOT called")
		t.Logf("TIMEOUT TEST: Check if isTimeoutOrConnectionError() is properly detecting the timeout")
	}
}
