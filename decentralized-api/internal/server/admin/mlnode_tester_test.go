package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"
	"errors"
	"os"
	"testing"

	"github.com/knadh/koanf/providers/file"
)

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
	tester := NewMLNodeTester(cm, factory)

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

	tester := NewMLNodeTester(cm, factory)
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

	tester := NewMLNodeTester(cm, factory)
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

func TestVersionedURL(t *testing.T) {
	if got := versionedURL("h", 8080, "/seg", ""); got != "http://h:8080/seg" {
		t.Errorf("unversioned = %q", got)
	}
	if got := versionedURL("h", 8080, "/seg", "v1.2.3"); got != "http://h:8080/v1.2.3/seg" {
		t.Errorf("versioned = %q", got)
	}
	if got := versionedURL("h", 5000, "", "v1"); got != "http://h:5000/v1" {
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

	tester := NewMLNodeTester(cm, factory)
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
	tester := NewMLNodeTester(cm, factory)

	// Simulate an ongoing test, then verify a concurrent Run rejects.
	tester.mu.Lock()
	tester.inFlight["node1"] = true
	tester.mu.Unlock()

	_, err := tester.Run(context.Background(), "node1")
	if !errors.Is(err, ErrTestInProgress) {
		t.Fatalf("got %v, want ErrTestInProgress", err)
	}
}
