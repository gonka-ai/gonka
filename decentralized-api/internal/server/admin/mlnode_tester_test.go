package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/knadh/koanf/providers/file"
	"github.com/productscience/inference/x/inference/types"
)

type stubGovModels struct {
	resp *types.QueryModelsAllResponse
	err  error
	// block, when non-nil, makes the governance query wait until it is closed
	// or the context is cancelled — used to assert the query is cancellable.
	block chan struct{}
	// calls counts invocations; only read after the test has synchronized.
	calls *atomic.Int32
}

func (s stubGovModels) GetGovernanceModels(ctx context.Context) (*types.QueryModelsAllResponse, error) {
	if s.calls != nil {
		s.calls.Add(1)
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	tester.inFlight["node1"] = &runningTest{cancel: func() {}}
	tester.mu.Unlock()

	_, err := tester.Run(context.Background(), "node1")
	if !errors.Is(err, ErrTestInProgress) {
		t.Fatalf("got %v, want ErrTestInProgress", err)
	}
}

// TestMLNodeTester_RejectsSecondNodeWhileBusy covers the global limit: a test
// takes its node out of service, so two nodes must never be under test at once
// (a batch registration would otherwise stop several MLnodes simultaneously).
func TestMLNodeTester_RejectsSecondNodeWhileBusy(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{
		{Id: "node1", Host: "test-host", PoCPort: 8080, InferencePort: 5000,
			Models: map[string]apiconfig.ModelConfig{"m": {}}},
		{Id: "node2", Host: "test-host-2", PoCPort: 8080, InferencePort: 5000,
			Models: map[string]apiconfig.ModelConfig{"m": {}}},
	})
	tester := NewMLNodeTester(cm, mlnodeclient.NewMockClientFactory(), stubGovModelsForConfig(cm))

	tester.mu.Lock()
	tester.inFlight["node1"] = &runningTest{cancel: func() {}}
	tester.mu.Unlock()

	if _, err := tester.Run(context.Background(), "node2"); !errors.Is(err, ErrTestBusy) {
		t.Fatalf("got %v, want ErrTestBusy", err)
	}
	if !tester.IsAnyRunning() {
		t.Fatal("IsAnyRunning should report the in-flight test")
	}
}

// TestMLNodeTester_GovernanceQueryIsCancellable guards against the in-flight
// slot leaking: the governance RPC must observe the caller's context, so a hung
// chain query ends with the test instead of pinning the node as "testing"
// forever.
func TestMLNodeTester_GovernanceQueryIsCancellable(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {}},
	}})
	gov := stubGovModels{block: make(chan struct{})} // never closed
	tester := NewMLNodeTester(cm, mlnodeclient.NewMockClientFactory(), gov)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan *TestResult, 1)
	go func() {
		result, err := tester.Run(ctx, "node1")
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		done <- result
	}()

	select {
	case result := <-done:
		if result.Status != TestFailed {
			t.Fatalf("status=%q, want %q", result.Status, TestFailed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("governance query did not observe context cancellation")
	}
	if tester.IsAnyRunning() {
		t.Fatal("in-flight slot leaked after a cancelled test")
	}
	// A cancelled run says nothing about the node, so nothing is recorded.
	if got := tester.LastResult("node1"); got != nil {
		t.Fatalf("cancelled run recorded a result: %+v", got)
	}
}

// TestMLNodeTester_InvalidateCancelsRunningTest covers the PUT/DELETE race: a
// test running against the old deployment must be aborted when the node is
// reconfigured, so its teardown cannot stop the new one.
func TestMLNodeTester_InvalidateCancelsRunningTest(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {}},
	}})
	release := make(chan struct{})
	gov := stubGovModels{
		resp:  &types.QueryModelsAllResponse{Model: []types.Model{{Id: "m"}}},
		block: release,
	}
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	tester := NewMLNodeTester(cm, factory, gov)

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := tester.Run(context.Background(), "node1"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	}()

	// Wait until the test is registered as in flight, then invalidate it.
	deadline := time.Now().Add(2 * time.Second)
	for !tester.IsRunning("node1") {
		if time.Now().After(deadline) {
			t.Fatal("test never became in-flight")
		}
		time.Sleep(5 * time.Millisecond)
	}
	tester.Invalidate("node1")

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Invalidate did not cancel the running test")
	}
	if got := tester.LastResult("node1"); got != nil {
		t.Fatalf("cancelled run recorded a result: %+v", got)
	}
	// The cancelled test must not have issued a teardown Stop against what may
	// already be a new deployment.
	mockClient.Mu.Lock()
	stops := mockClient.StopCalled
	mockClient.Mu.Unlock()
	if stops != 0 {
		t.Fatalf("cancelled test issued %d Stop calls, want 0", stops)
	}
}

// TestMLNodeTester_TeardownOnlySkippedForInvalidation separates the two reasons a
// test's context can be cancelled. Keying the teardown skip off ctx.Err() lumped
// them together: an operator closing the tab mid-test produces the same
// context.Canceled as Invalidate, and the MLnode was then left with the test's
// model loaded and no Stop issued — worse than the base, which always tore down.
//
// Stop is called twice on a normal path (once before the launch, once in
// teardown) and once when teardown is skipped.
func TestMLNodeTester_TeardownOnlySkippedForInvalidation(t *testing.T) {
	newTester := func(t *testing.T) (*MLNodeTester, *mlnodeclient.MockClient, chan struct{}) {
		t.Helper()
		cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
			Id:            "node1",
			Host:          "test-host",
			PoCPort:       8080,
			InferencePort: 5000,
			Models:        map[string]apiconfig.ModelConfig{"m": {}},
		}})
		factory := mlnodeclient.NewMockClientFactory()
		mc := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
		mc.InferenceIsHealthy = true
		release := make(chan struct{})
		mc.InferenceUpBlock = release
		tester := NewMLNodeTester(cm, factory, stubGovModels{
			resp: &types.QueryModelsAllResponse{Model: []types.Model{{Id: "m"}}},
		})
		return tester, mc, release
	}

	stopCount := func(mc *mlnodeclient.MockClient) int {
		mc.Mu.Lock()
		defer mc.Mu.Unlock()
		return mc.StopCalled
	}

	waitInFlight := func(t *testing.T, tester *MLNodeTester) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for !tester.IsRunning("node1") {
			if time.Now().After(deadline) {
				t.Fatal("test never became in-flight")
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	// IsRunning flips in Run before runOnce reaches the pre-launch Stop, so
	// cancelling on that signal alone can abort at the loop's ctx.Err() check
	// and skip it — leaving one Stop, not two. Wait for the launch sequence to
	// have actually started before interfering with it.
	waitStops := func(t *testing.T, mc *mlnodeclient.MockClient, want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for stopCount(mc) < want {
			if time.Now().After(deadline) {
				t.Fatalf("Stop called %d times, want at least %d", stopCount(mc), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	t.Run("client disconnect still tears down", func(t *testing.T) {
		tester, mc, _ := newTester(t)
		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := tester.Run(ctx, "node1"); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
		waitInFlight(t, tester)
		waitStops(t, mc, 1) // the pre-launch Stop has landed; the launch is under way

		cancel() // the operator closed the tab
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("run did not finish after the caller went away")
		}

		if got := stopCount(mc); got != 2 {
			t.Fatalf("Stop called %d times, want 2 (pre-launch + teardown): a caller "+
				"going away must not leave the node with the test's model loaded", got)
		}
	})

	t.Run("invalidation skips teardown", func(t *testing.T) {
		tester, mc, _ := newTester(t)

		done := make(chan struct{})
		go func() {
			defer close(done)
			if _, err := tester.Run(context.Background(), "node1"); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
		waitInFlight(t, tester)

		tester.Invalidate("node1") // node reconfigured under the test
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("Invalidate did not cancel the running test")
		}

		if got := stopCount(mc); got != 1 {
			t.Fatalf("Stop called %d times, want 1 (pre-launch only): teardown must be "+
				"skipped so it cannot stop a new deployment", got)
		}
	})

	t.Run("deadline still tears down", func(t *testing.T) {
		tester, mc, _ := newTester(t)
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()

		if _, err := tester.Run(ctx, "node1"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got := stopCount(mc); got != 2 {
			t.Fatalf("Stop called %d times, want 2 (pre-launch + teardown)", got)
		}
	})
}

// TestMLNodeTester_ResultInvalidatedByConfigChange covers results being bound to
// the inputs they were produced from: changing the node's model args must make
// the recorded pass stop counting, without anyone having to call Invalidate.
func TestMLNodeTester_ResultInvalidatedByConfigChange(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {Args: []string{"--old"}}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	tester := NewMLNodeTester(cm, factory, stubGovModels{
		resp: &types.QueryModelsAllResponse{Model: []types.Model{{Id: "m"}}},
	})

	if _, err := tester.Run(context.Background(), "node1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tester.LastResult("node1"); got == nil || got.Status != TestSuccess {
		t.Fatalf("LastResult not recorded: %+v", got)
	}

	// Same node id, different args: the recorded pass no longer describes the
	// configuration that would be launched now.
	if err := cm.SetNodes([]apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {Args: []string{"--new"}}},
	}}); err != nil {
		t.Fatalf("SetNodes: %v", err)
	}
	if got := tester.LastResult("node1"); got != nil {
		t.Fatalf("stale result survived a config change: %+v", got)
	}
}

// TestMLNodeTester_ResultInvalidatedByPoCParamChange is the same guard for
// process state the test derives its launch plan from, not just node config.
func TestMLNodeTester_ResultInvalidatedByPoCParamChange(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceIsHealthy = true
	tester := NewMLNodeTester(cm, factory, stubGovModels{
		resp: &types.QueryModelsAllResponse{Model: []types.Model{{Id: "m"}}},
	})

	if _, err := tester.Run(context.Background(), "node1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := tester.LastResult("node1"); got == nil {
		t.Fatal("LastResult not recorded")
	}

	if err := cm.SetPoCParams(apiconfig.PoCParamsCache{
		Models: []apiconfig.PoCModelConfigCache{{ModelId: "m", SeqLen: 2048}},
	}); err != nil {
		t.Fatalf("SetPoCParams: %v", err)
	}
	if got := tester.LastResult("node1"); got != nil {
		t.Fatalf("stale result survived a PoC param change: %+v", got)
	}
}

// TestMLNodeTester_RetryableClassification pins the retry classification the
// auto-test backoff depends on: an MLnode that answered "busy" (409) or failed
// server-side (5xx) is transient; a request the node rejected as invalid (422)
// is not, and must not be retried every backoff window forever.
func TestMLNodeTester_RetryableClassification(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "already running (409)", err: &mlnodeclient.StatusError{Op: "inference/up", StatusCode: 409}, want: true},
		{name: "server error (500)", err: &mlnodeclient.StatusError{Op: "inference/up", StatusCode: 500}, want: true},
		{name: "validation error (422)", err: &mlnodeclient.StatusError{Op: "inference/up", StatusCode: 422}, want: false},
		{name: "not found (404)", err: &mlnodeclient.StatusError{Op: "stop", StatusCode: 404}, want: false},
		{name: "transport error", err: errors.New("connection refused"), want: true},
		{name: "deadline exceeded", err: context.DeadlineExceeded, want: true},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableMLNodeError(tc.err); got != tc.want {
				t.Fatalf("isRetryableMLNodeError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestMLNodeTester_BusyNodeIsRetryable is the end-to-end form of the above: a
// node that answers 409 to /inference/up (vLLM already running) must produce a
// retryable failure, not a latched TEST_FAILED.
func TestMLNodeTester_BusyNodeIsRetryable(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"m": {}},
	}})
	factory := mlnodeclient.NewMockClientFactory()
	mockClient := factory.CreateClient("http://test-host:8080", "http://test-host:5000").(*mlnodeclient.MockClient)
	mockClient.InferenceUpError = &mlnodeclient.StatusError{
		Op: "inference/up", StatusCode: 409, Body: "VLLM is already running.",
	}
	tester := NewMLNodeTester(cm, factory, stubGovModels{
		resp: &types.QueryModelsAllResponse{Model: []types.Model{{Id: "m"}}},
	})

	result, err := tester.Run(context.Background(), "node1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != TestFailed {
		t.Fatalf("status=%q, want %q", result.Status, TestFailed)
	}
	if !result.Retryable {
		t.Fatalf("a 409 (node busy) must be retryable: %+v", result)
	}
}

// TestMLNodeTester_PerModelTimings guards that a multi-model node reports one
// health/response time per model instead of only the last model's (the scalar
// fields were overwritten each iteration).
func TestMLNodeTester_PerModelTimings(t *testing.T) {
	cm := newTesterConfig(t, []apiconfig.InferenceNodeConfig{{
		Id:            "node1",
		Host:          "test-host",
		PoCPort:       8080,
		InferencePort: 5000,
		Models:        map[string]apiconfig.ModelConfig{"model-a": {}, "model-b": {}},
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
	for _, modelID := range []string{"model-a", "model-b"} {
		if _, ok := result.HealthMs[modelID]; !ok {
			t.Errorf("HealthMs missing %s: %+v", modelID, result.HealthMs)
		}
		if _, ok := result.RespMs[modelID]; !ok {
			t.Errorf("RespMs missing %s: %+v", modelID, result.RespMs)
		}
	}
	if result.FinishedAt.IsZero() || result.FinishedAt.Before(result.StartedAt) {
		t.Errorf("FinishedAt=%v not after StartedAt=%v", result.FinishedAt, result.StartedAt)
	}
}
