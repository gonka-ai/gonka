package mlnode

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"common/utils"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/cosmosclient"
	"decentralized-api/mlnodeclient"
	"decentralized-api/poc"
	"decentralized-api/poc/artifacts"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type stubBrokerChainBridge struct {
	models []string
}

const (
	testModelA = "model-a"
	testModelB = "org/model-b"
)

func (s stubBrokerChainBridge) GetHardwareNodes() (*types.QueryHardwareNodesResponse, error) {
	return &types.QueryHardwareNodesResponse{}, nil
}

func (s stubBrokerChainBridge) SubmitHardwareDiff(diff *types.MsgSubmitHardwareDiff) error {
	return nil
}

func (s stubBrokerChainBridge) GetBlockHash(height int64) (string, error) {
	return "", nil
}

func (s stubBrokerChainBridge) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	models := make([]types.Model, 0, len(s.models))
	for _, modelID := range s.models {
		models = append(models, types.Model{Id: modelID})
	}
	return &types.QueryModelsAllResponse{Model: models}, nil
}

func (s stubBrokerChainBridge) GetCurrentEpochGroupData() (*types.QueryCurrentEpochGroupDataResponse, error) {
	return &types.QueryCurrentEpochGroupDataResponse{}, nil
}

func (s stubBrokerChainBridge) GetEpochGroupDataByModelId(pocHeight uint64, modelId string) (*types.QueryGetEpochGroupDataResponse, error) {
	return &types.QueryGetEpochGroupDataResponse{}, nil
}

func (s stubBrokerChainBridge) GetPreservedNodesSnapshot() (*types.QueryPreservedNodesSnapshotResponse, error) {
	return &types.QueryPreservedNodesSnapshotResponse{Found: false}, nil
}

func (s stubBrokerChainBridge) GetParams() (*types.QueryParamsResponse, error) {
	return &types.QueryParamsResponse{}, nil
}

func newMLNodeTestBroker(t *testing.T, phase types.EpochPhase, modelIDs ...string) *broker.Broker {
	t.Helper()

	tracker := &chainphase.ChainPhaseTracker{}
	tracker.Update(
		chainphase.BlockInfo{Height: 110, Hash: "test-hash"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{
			EpochLength:           1000,
			EpochShift:            0,
			PocStageDuration:      100,
			PocExchangeDuration:   50,
			PocValidationDelay:    10,
			PocValidationDuration: 100,
		},
		true,
		nil,
	)
	testBroker := broker.NewBroker(
		stubBrokerChainBridge{models: modelIDs},
		tracker,
		nil,
		"http://callback",
		mlnodeclient.NewMockClientFactory(),
		&apiconfig.ConfigManager{},
	)

	switch phase {
	case types.PoCValidatePhase:
		tracker.Update(chainphase.BlockInfo{Height: 220, Hash: "test-hash"}, &types.Epoch{Index: 1, PocStartBlockHeight: 100}, &types.EpochParams{
			EpochLength:           1000,
			EpochShift:            0,
			PocStageDuration:      100,
			PocExchangeDuration:   50,
			PocValidationDelay:    10,
			PocValidationDuration: 100,
		}, true, nil)
	case types.PoCGeneratePhase:
		// already set above
	}

	models := make(map[string]apiconfig.ModelConfig, len(modelIDs))
	for _, modelID := range modelIDs {
		models[modelID] = apiconfig.ModelConfig{}
	}

	loadResp := testBroker.LoadNodeToBroker(&apiconfig.InferenceNodeConfig{
		Host:             "127.0.0.1",
		InferenceSegment: "/inference",
		InferencePort:    8081,
		PoCSegment:       "/poc",
		PoCPort:          8082,
		Models:           models,
		Id:               "node-1",
		MaxConcurrent:    1,
	})
	resp := <-loadResp
	if resp.Error != nil {
		t.Fatalf("LoadNodeToBroker failed: %v", resp.Error)
	}

	return testBroker
}

func TestV2GeneratedCallbackRequiresModelScopedRoute(t *testing.T) {
	artifactStore := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer artifactStore.Close()
	artifactStore.ActivateStage(100)

	server := NewServer(nil, newMLNodeTestBroker(t, types.PoCGeneratePhase, testModelA, testModelB), WithArtifactStore(artifactStore))

	body, err := json.Marshal(map[string]any{
		"block_hash":   "abc",
		"block_height": 100,
		"public_key":   "pub",
		"node_id":      1,
		"artifacts": []map[string]any{
			{"nonce": 1, "vector_b64": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})},
		},
	})
	assert.NoError(t, err)

	unscopedReq := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/generated", bytes.NewReader(body))
	unscopedReq.Header.Set("Content-Type", "application/json")
	unscopedRec := httptest.NewRecorder()
	server.e.ServeHTTP(unscopedRec, unscopedReq)
	assert.Equal(t, http.StatusNotFound, unscopedRec.Code)

	scopedReq := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/model-a/generated", bytes.NewReader(body))
	scopedReq.Header.Set("Content-Type", "application/json")
	scopedRec := httptest.NewRecorder()
	server.e.ServeHTTP(scopedRec, scopedReq)
	assert.Equal(t, http.StatusOK, scopedRec.Code)

	secondReq := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/org%252Fmodel-b/generated", bytes.NewReader(body))
	secondReq.Header.Set("Content-Type", "application/json")
	secondRec := httptest.NewRecorder()
	server.e.ServeHTTP(secondRec, secondReq)
	assert.Equal(t, http.StatusOK, secondRec.Code)

	modelStore, err := artifactStore.GetStore(100, testModelA)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), modelStore.Count())

	otherStore, err := artifactStore.GetStore(100, testModelB)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), otherStore.Count())
}

func postGeneratedBatch(t *testing.T, server *Server, modelID string, blockHeight int64) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"block_hash":   "abc",
		"block_height": blockHeight,
		"public_key":   "pub",
		"node_id":      1,
		"artifacts": []map[string]any{
			{"nonce": 1, "vector_b64": base64.StdEncoding.EncodeToString([]byte{1, 2, 3})},
		},
	})
	assert.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/"+modelID+"/generated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	return rec
}

func TestV2GeneratedCallbackRejectsWrongStageHeight(t *testing.T) {
	artifactStore := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer artifactStore.Close()
	artifactStore.ActivateStage(100)

	server := NewServer(nil, newMLNodeTestBroker(t, types.PoCGeneratePhase, testModelA), WithArtifactStore(artifactStore))
	rec := postGeneratedBatch(t, server, testModelA, 999)
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	_, err := artifactStore.GetStore(100, testModelA)
	assert.Error(t, err)
}

func TestV2GeneratedCallback_ChallengeStageRejectsOldHeight(t *testing.T) {
	poc.OpenChallenges.Reset()
	t.Cleanup(poc.OpenChallenges.Reset)
	poc.OpenChallenges.Replace("me", []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      "me",
			StartHeight: 777,
			Seed:        []byte{1},
		},
		Finish:     2000,
		Generating: true,
	}}, 0)

	testBroker := newMLNodeTestBroker(t, types.PoCGeneratePhase, testModelA)
	testBroker.GetPhaseTracker().Update(
		chainphase.BlockInfo{Height: 800, Hash: "test-hash"},
		&types.Epoch{Index: 1, PocStartBlockHeight: 100},
		&types.EpochParams{
			EpochLength:           1000,
			EpochShift:            0,
			PocStageDuration:      100,
			PocExchangeDuration:   50,
			PocValidationDelay:    10,
			PocValidationDuration: 100,
		},
		true,
		nil,
	)

	artifactStore := artifacts.NewManagedArtifactStore(t.TempDir(), 3)
	defer artifactStore.Close()
	server := NewServer(nil, testBroker, WithArtifactStore(artifactStore))

	old := postGeneratedBatch(t, server, testModelA, 100)
	assert.Equal(t, http.StatusBadRequest, old.Code)

	ok := postGeneratedBatch(t, server, testModelA, 777)
	assert.Equal(t, http.StatusOK, ok.Code)

	_, err := artifactStore.GetStore(100, testModelA)
	assert.Error(t, err)
	modelStore, err := artifactStore.GetStore(777, testModelA)
	assert.NoError(t, err)
	assert.Equal(t, uint32(1), modelStore.Count())
}

func TestV2ValidatedCallbackUsesPathModelID(t *testing.T) {
	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
			return msg != nil &&
				msg.PocStageStartBlockHeight == 100 &&
				len(msg.Validations) == 1 &&
				msg.Validations[0].ModelId == testModelA
		})).
		Return(nil).
		Once()

	server := NewServer(mockRecorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA))

	body, err := json.Marshal(map[string]any{
		"block_hash":      "abc",
		"block_height":    100,
		"public_key":      "02b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540",
		"node_id":         1,
		"n_total":         5,
		"n_mismatch":      0,
		"mismatch_nonces": []int{},
		"p_value":         1.0,
		"fraud_detected":  false,
	})
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/model-a/validated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	server.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	mockRecorder.AssertExpectations(t)
}

func TestV2ValidatedCallbackSubmitsChallengeMsg(t *testing.T) {
	const pubKey = "02b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540"
	target, err := utils.PubKeyHexToAddress(pubKey)
	require.NoError(t, err)

	poc.OpenChallenges.Reset()
	t.Cleanup(poc.OpenChallenges.Reset)
	poc.OpenChallenges.Replace("me", []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      target,
			StartHeight: 500,
		},
		Finish:     900,
		Generating: false,
	}}, 0)

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitPoCChallengeValidations", mock.MatchedBy(func(msg *types.MsgSubmitPoCChallengeValidations) bool {
			return msg != nil &&
				msg.PocStageStartBlockHeight == 500 &&
				len(msg.Validations) == 1 &&
				msg.Validations[0].ModelId == testModelA &&
				msg.Validations[0].ParticipantAddress == target
		})).
		Return(nil).
		Once()

	server := NewServer(mockRecorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA))

	body, err := json.Marshal(map[string]any{
		"block_hash":      "abc",
		"block_height":    500,
		"public_key":      pubKey,
		"node_id":         1,
		"n_total":         5,
		"n_mismatch":      0,
		"mismatch_nonces": []int{},
		"p_value":         1.0,
		"fraud_detected":  false,
	})
	assert.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/model-a/validated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	mockRecorder.AssertExpectations(t)
	mockRecorder.AssertNotCalled(t, "SubmitPocValidationsV2", mock.Anything)
}

func TestV2ValidatedCallback_HeightCollisionUsesRegularMsg(t *testing.T) {
	const pubKey = "02b463f7f42e5f4f1d2d0bb1c4b9f8d2c3b1a09c72fbc5d0b8d4c53b37f6f2a540"
	poc.OpenChallenges.Reset()
	t.Cleanup(poc.OpenChallenges.Reset)
	poc.OpenChallenges.Replace("me", []*types.OpenPoCChallenge{{
		Challenge: &types.PoCChallenge{
			Target:      "gonka1someoneelse",
			StartHeight: 500,
		},
		Finish:     900,
		Generating: false,
	}}, 0)

	mockRecorder := &cosmosclient.MockCosmosMessageClient{}
	mockRecorder.
		On("SubmitPocValidationsV2", mock.MatchedBy(func(msg *types.MsgSubmitPocValidationsV2) bool {
			return msg != nil && msg.PocStageStartBlockHeight == 500
		})).
		Return(nil).
		Once()

	server := NewServer(mockRecorder, newMLNodeTestBroker(t, types.PoCValidatePhase, testModelA))
	body, err := json.Marshal(map[string]any{
		"block_hash":      "abc",
		"block_height":    500,
		"public_key":      pubKey,
		"node_id":         1,
		"n_total":         5,
		"n_mismatch":      0,
		"mismatch_nonces": []int{},
		"p_value":         1.0,
		"fraud_detected":  false,
	})
	assert.NoError(t, err)
	req := httptest.NewRequest(http.MethodPost, "/v2/poc-batches/model-a/validated", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	mockRecorder.AssertExpectations(t)
	mockRecorder.AssertNotCalled(t, "SubmitPoCChallengeValidations", mock.Anything)
}

func TestGetVersions_OracleJSONContract(t *testing.T) {
	cm := &apiconfig.ConfigManager{}
	cm.SetDevshardVersions(apiconfig.DevshardVersionsCache{
		Versions: []apiconfig.DevshardVersion{
			{Name: "v1", Binary: "https://example/v1.zip", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		},
	})
	server := NewServer(nil, nil, WithConfigManager(cm))

	req := httptest.NewRequest(http.MethodGet, "/versions", nil)
	rec := httptest.NewRecorder()
	server.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var body struct {
		Versions []struct {
			Name   string `json:"name"`
			Binary string `json:"binary"`
			SHA256 string `json:"sha256"`
		} `json:"versions"`
	}
	assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Len(t, body.Versions, 1)
	assert.Equal(t, "v1", body.Versions[0].Name)
	assert.Equal(t, "https://example/v1.zip", body.Versions[0].Binary)
	assert.Equal(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", body.Versions[0].SHA256)
}
