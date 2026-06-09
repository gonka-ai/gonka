package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/knadh/koanf/providers/file"
	"github.com/productscience/inference/x/inference/types"
)

type stubGovModels struct {
	resp *types.QueryModelsAllResponse
	err  error
}

func (s stubGovModels) GetGovernanceModels() (*types.QueryModelsAllResponse, error) {
	return s.resp, s.err
}

func stubGovModelsForConfig(cm *apiconfig.ConfigManager) stubGovModels {
	modelsByID := map[string]types.Model{}
	for _, node := range cm.GetNodes() {
		for modelID := range node.Models {
			modelsByID[modelID] = types.Model{Id: modelID}
		}
	}
	models := make([]types.Model, 0, len(modelsByID))
	for _, model := range modelsByID {
		models = append(models, model)
	}
	return stubGovModels{resp: &types.QueryModelsAllResponse{Model: models}}
}

func newTesterConfig(t *testing.T, nodes []apiconfig.InferenceNodeConfig) *apiconfig.ConfigManager {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "tester-config-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(tmpFile.Name()) })
	if _, err := tmpFile.Write([]byte("nodes: []")); err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	cm := &apiconfig.ConfigManager{
		KoanProvider:   file.Provider(tmpFile.Name()),
		WriterProvider: apiconfig.NewFileWriteCloserProvider(tmpFile.Name()),
	}
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(nodes) > 0 {
		if err := cm.SetNodes(nodes); err != nil {
			t.Fatalf("SetNodes: %v", err)
		}
	}
	return cm
}

func TestMLNodeTester_NodeNotFound(t *testing.T) {
	cm := newTesterConfig(t, nil)
	factory := mlnodeclient.NewMockClientFactory()
	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))

	_, err := tester.Run(context.Background(), "missing")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("got %v, want ErrNodeNotFound", err)
	}
}

func TestMLNodeTester_SuccessRecordsResult(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"Qwen2.5-7B-Instruct": {Args: []string{"--quantization", "fp8"}},
		},
	}})
	factory := mlnodeclient.NewMockClientFactory()

	// Pre-create the mock client at the same pocUrl the tester will
	// use, and configure it to report healthy.
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("got %q, want %q (error=%q)", result.Status, TestSuccess, result.Error)
	}
	if _, ok := result.LoadMs["Qwen2.5-7B-Instruct"]; !ok {
		t.Fatalf("LoadMs missing for model: %+v", result.LoadMs)
	}
	if got := tester.LastResult("node1"); got == nil || got.Status != TestSuccess {
		t.Fatalf("LastResult not recorded: %+v", got)
	}
}

func TestMLNodeTester_ModelLoadFailure(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"broken-model": {},
		},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceUpError = errors.New("OOM")

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestFailed {
		t.Fatalf("got %q, want %q", result.Status, TestFailed)
	}
	if result.FailingModel != "broken-model" {
		t.Errorf("FailingModel=%q, want broken-model", result.FailingModel)
	}
	if result.Error == "" {
		t.Errorf("Error empty, expected OOM message")
	}
}

func TestMLNodeTester_MergesGovernanceArgs(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"model-a": {Args: []string{"--local", "2"}},
		},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	gov := stubGovModels{resp: &types.QueryModelsAllResponse{
		Model: []types.Model{{Id: "model-a", ModelArgs: []string{"--gov", "1"}}},
	}}

	tester := NewMLNodeTester(cm, factory, gov)
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("status=%q error=%q", result.Status, result.Error)
	}
	// The model is launched with the broker's merged args (governance first,
	// then local), not just local args.
	want := []string{"--gov", "1", "--local", "2"}
	if got := mockClient.LastInferenceArgs; !reflect.DeepEqual(got, want) {
		t.Errorf("InferenceUp args = %v, want %v (governance + local merged)", got, want)
	}
}

func TestMLNodeTester_MultipleModels(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"model-a": {},
			"model-b": {},
		},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("status=%q error=%q", result.Status, result.Error)
	}
	// Each configured model must be loaded AND inference-probed, with a
	// Stop between them (the MLnode 409s a second /inference/up otherwise).
	if len(result.LoadMs) != 2 {
		t.Errorf("LoadMs=%v, want both models loaded", result.LoadMs)
	}
	if mockClient.InferenceUpCalled != 2 {
		t.Errorf("InferenceUp called %d times, want 2", mockClient.InferenceUpCalled)
	}
	if mockClient.InferenceCalled != 2 {
		t.Errorf("inference probed %d times, want 2 (one per model)", mockClient.InferenceCalled)
	}
	if mockClient.StopCalled < 1 {
		t.Errorf("Stop called %d times, want >=1 (between models)", mockClient.StopCalled)
	}
}

func TestMLNodeTester_FiltersModelsUnsupportedByPoCParams(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"model-a": {},
			"model-b": {},
		},
	}})
	if err := cm.SetPoCParams(apiconfig.PoCParamsCache{
		Models: []apiconfig.PoCModelConfigCache{{ModelId: "model-a", SeqLen: 1024}},
	}); err != nil {
		t.Fatalf("SetPoCParams: %v", err)
	}
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	gov := stubGovModels{resp: &types.QueryModelsAllResponse{
		Model: []types.Model{{Id: "model-a"}, {Id: "model-b"}},
	}}

	result, err := NewMLNodeTester(cm, factory, gov).Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("status=%q error=%q", result.Status, result.Error)
	}
	if mockClient.InferenceUpCalled != 1 {
		t.Fatalf("InferenceUp called %d times, want 1", mockClient.InferenceUpCalled)
	}
	if mockClient.LastInferenceModel != "model-a" {
		t.Fatalf("last model=%q, want model-a", mockClient.LastInferenceModel)
	}
	if _, ok := result.LoadMs["model-b"]; ok {
		t.Fatalf("unsupported model-b should not be tested: %+v", result.LoadMs)
	}
}

func TestMLNodeTester_SkipsConfiguredModelsAbsentFromGovernance(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"legacy-model": {},
			"model-a":      {},
		},
	}})
	if err := cm.SetPoCParams(apiconfig.PoCParamsCache{
		Models: []apiconfig.PoCModelConfigCache{
			{ModelId: "legacy-model", SeqLen: 1024},
			{ModelId: "model-a", SeqLen: 1024},
		},
	}); err != nil {
		t.Fatalf("SetPoCParams: %v", err)
	}
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	gov := stubGovModels{resp: &types.QueryModelsAllResponse{
		Model: []types.Model{{Id: "model-a"}},
	}}

	result, err := NewMLNodeTester(cm, factory, gov).Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("status=%q error=%q", result.Status, result.Error)
	}
	if mockClient.InferenceUpCalled != 1 {
		t.Fatalf("InferenceUp called %d times, want 1", mockClient.InferenceUpCalled)
	}
	if mockClient.LastInferenceModel != "model-a" {
		t.Fatalf("last model=%q, want model-a", mockClient.LastInferenceModel)
	}
	if _, ok := result.LoadMs["legacy-model"]; ok {
		t.Fatalf("governance-absent legacy-model should be skipped: %+v", result.LoadMs)
	}
}

func TestMLNodeTester_StopsBeforeFirstModel(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"model-a": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.CurrentState = mlnodeclient.MlNodeState_INFERENCE
	mockClient.InferenceIsHealthy = true

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestSuccess {
		t.Fatalf("status=%q error=%q", result.Status, result.Error)
	}
	if mockClient.StopCalled < 2 {
		t.Errorf("Stop called %d times, want >=2 (before first model and cleanup)", mockClient.StopCalled)
	}
}

func TestMLNodeTester_StopFailureFailsTest(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"model-a": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.StopError = errors.New("stop failed")

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestFailed {
		t.Fatalf("status=%q, want %q", result.Status, TestFailed)
	}
	if result.FailingModel != "model-a" {
		t.Fatalf("failing model=%q, want model-a", result.FailingModel)
	}
	if mockClient.InferenceUpCalled != 0 {
		t.Fatalf("InferenceUp called %d times after Stop failure, want 0", mockClient.InferenceUpCalled)
	}
}

func TestMLNodeTester_GovernanceQueryFailureFailsTest(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"model-a": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	tester := NewMLNodeTester(cm, factory, stubGovModels{err: errors.New("chain unavailable")})

	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestFailed {
		t.Fatalf("status=%q, want %q", result.Status, TestFailed)
	}
	if !strings.Contains(result.Error, "chain unavailable") {
		t.Fatalf("error=%q, want governance query error", result.Error)
	}
}

func TestMLNodeTester_InvalidatedRunDoesNotRestoreStaleResult(t *testing.T) {
	cm := newTesterConfig(t, nil)
	tester := NewMLNodeTester(cm, mlnodeclient.NewMockClientFactory(), stubGovModelsForConfig(cm))
	stale := &TestResult{NodeId: "node1", Status: TestSuccess}

	tester.Invalidate("node1")
	tester.recordResult("node1", 0, stale)

	if got := tester.LastResult("node1"); got != nil {
		t.Fatalf("stale result was restored after invalidation: %+v", got)
	}
}

func TestMLNodeTester_FindNodeReturnsDeepCopy(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:     "node1",
		Models: map[string]apiconfig.ModelConfig{"model-a": {Args: []string{"--old"}}},
	}})
	tester := NewMLNodeTester(cm, mlnodeclient.NewMockClientFactory(), stubGovModelsForConfig(cm))

	snapshot, ok := tester.findNode("node1")
	if !ok {
		t.Fatal("node not found")
	}

	nodes := cm.GetNodes()
	nodes[0].Models["model-a"] = apiconfig.ModelConfig{Args: []string{"--new"}}

	if got := snapshot.Models["model-a"].Args; !reflect.DeepEqual(got, []string{"--old"}) {
		t.Fatalf("snapshot args changed after config mutation: %v", got)
	}
}

func TestMLNodeURL(t *testing.T) {
	if got := apiconfig.MLNodeURL("h", 8080, "/seg", ""); got != "http://h:8080/seg" {
		t.Errorf("unversioned = %q", got)
	}
	if got := apiconfig.MLNodeURL("h", 8080, "/seg", "v1.2.3"); got != "http://h:8080/v1.2.3/seg" {
		t.Errorf("versioned = %q", got)
	}
	if got := apiconfig.MLNodeURL("h", 5000, "", "v1"); got != "http://h:5000/v1" {
		t.Errorf("versioned no-segment = %q", got)
	}
}

func TestMLNodeTester_InferenceRequestFailure(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models: map[string]apiconfig.ModelConfig{
			"model-a": {},
		},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	// Model loads fine and is healthy, but the inference request fails —
	// the response-validation step must catch it.
	mockClient.InferenceIsHealthy = true
	mockClient.InferenceError = errors.New("bad gateway")

	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))
	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestFailed {
		t.Fatalf("got %q, want %q", result.Status, TestFailed)
	}
	if result.FailingModel != "model-a" {
		t.Errorf("FailingModel=%q, want model-a", result.FailingModel)
	}
	if mockClient.InferenceCalled == 0 {
		t.Errorf("expected the response-validation step to call Inference")
	}
}

func TestMLNodeTester_RejectsConcurrent(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	tester := NewMLNodeTester(cm, factory, stubGovModelsForConfig(cm))

	// Simulate an ongoing test, then verify a concurrent Run rejects.
	tester.mu.Lock()
	tester.inFlight["node1"] = true
	tester.mu.Unlock()

	_, err := tester.Run(context.Background(), "node1")
	if !errors.Is(err, ErrTestInProgress) {
		t.Fatalf("got %v, want ErrTestInProgress", err)
	}
}
