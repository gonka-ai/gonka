package admin

import (
	"bytes"
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"encoding/json"
	"net/http"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

type TestResultStatus string

const (
	TestSuccess TestResultStatus = "SUCCESS"
	TestFailed  TestResultStatus = "FAILED"
)

type TestMetrics struct {
	LoadMs   map[string]int64
	HealthMs int64
	RespMs   int64
}

type TestResult struct {
	NodeId       string
	Status       TestResultStatus
	FailingModel string
	Error        string
	Metrics      TestMetrics
}

func getFirstModelId(models map[string]apiconfig.ModelConfig) string {
	for modelId := range models {
		return modelId
	}
	return "" // Return empty if no models
}

type MLnodeTestingOrchestrator struct {
	configManager    *apiconfig.ConfigManager
	blockTimeSeconds float64
	nodeBroker       *broker.Broker
}

func NewMLnodeTestingOrchestrator(cm *apiconfig.ConfigManager, nodeBroker *broker.Broker) *MLnodeTestingOrchestrator {
	return &MLnodeTestingOrchestrator{configManager: cm, blockTimeSeconds: 6.0, nodeBroker: nodeBroker}
}

func (o *MLnodeTestingOrchestrator) ShouldAutoTest(secondsUntilNextPoC int64) bool {
	return secondsUntilNextPoC > 3600
}

func (o *MLnodeTestingOrchestrator) RunNodeTest(ctx context.Context, node apiconfig.InferenceNodeConfig) *TestResult {
	version := o.configManager.GetCurrentNodeVersion()
	pocUrl := getPoCUrlWithVersion(node, version)
	inferenceUrl := formatURL(node.Host, node.InferencePort, node.InferenceSegment)
	client := mlnodeclient.NewNodeClient(pocUrl, inferenceUrl)

	// Helper function to set node state to TEST_FAILED on failure
	setTestFailed := func(nodeId string, reason string) {
		if o.nodeBroker != nil {
			cmd := broker.NewSetNodeMLNodeOnboardingStateCommand(nodeId, string(apiconfig.MLNodeState_TEST_FAILED))
			_ = o.nodeBroker.QueueMessage(cmd)
			_ = o.nodeBroker.QueueMessage(broker.NewSetNodeFailureReasonCommand(nodeId, reason))
		}

		// Notify MLnode about the failure
		notifyCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.SetNodeState(notifyCtx, mlnodeclient.MlNodeState_TEST_FAILED, reason)
	}

	metrics := TestMetrics{LoadMs: map[string]int64{}}

	for modelId, cfg := range node.Models {
		start := time.Now()
		err := client.InferenceUp(ctx, modelId, cfg.Args)
		metrics.LoadMs[modelId] = time.Since(start).Milliseconds()
		if err != nil {
			logging.Error("MLnode test failed during model loading", types.Nodes, "node_id", node.Id, "model", modelId, "error", err)
			setTestFailed(node.Id, err.Error())
			return &TestResult{NodeId: node.Id, Status: TestFailed, FailingModel: modelId, Error: err.Error(), Metrics: metrics}
		}
	}

	startHealth := time.Now()
	ok, err := client.InferenceHealth(ctx)
	metrics.HealthMs = time.Since(startHealth).Milliseconds()
	if err != nil || !ok {
		if err != nil {
			logging.Error("MLnode health check failed", types.Nodes, "node_id", node.Id, "error", err)
			setTestFailed(node.Id, err.Error())
			return &TestResult{NodeId: node.Id, Status: TestFailed, Error: err.Error(), Metrics: metrics}
		}
		logging.Error("MLnode health check not OK", types.Nodes, "node_id", node.Id)
		setTestFailed(node.Id, "health_not_ok")
		return &TestResult{NodeId: node.Id, Status: TestFailed, Error: "health_not_ok", Metrics: metrics}
	}

	// Perform test inference request to validate response and measure performance
	startResp := time.Now()
	testRequest := map[string]interface{}{
		"model":      getFirstModelId(node.Models),
		"messages":   []map[string]string{{"role": "user", "content": "Hello, how are you?"}},
		"max_tokens": 10,
	}
	requestBody, err := json.Marshal(testRequest)
	if err != nil {
		logging.Error("MLnode test failed to create test request", types.Nodes, "node_id", node.Id, "error", err)
		setTestFailed(node.Id, err.Error())
		return &TestResult{NodeId: node.Id, Status: TestFailed, Error: err.Error(), Metrics: metrics}
	}

	completionsUrl := inferenceUrl + "/v1/chat/completions"
	resp, err := http.Post(completionsUrl, "application/json", bytes.NewReader(requestBody))
	if err != nil {
		logging.Error("MLnode test failed during inference request", types.Nodes, "node_id", node.Id, "error", err)
		setTestFailed(node.Id, err.Error())
		return &TestResult{NodeId: node.Id, Status: TestFailed, Error: err.Error(), Metrics: metrics}
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		logging.Error("MLnode test received non-success status code", types.Nodes, "node_id", node.Id, "status_code", resp.StatusCode)
		setTestFailed(node.Id, "non_success_status_code")
		return &TestResult{NodeId: node.Id, Status: TestFailed, Error: "non_success_status_code", Metrics: metrics}
	}

	metrics.RespMs = time.Since(startResp).Milliseconds()

	// Helper function to set node state to WAITING_FOR_POC on success
	setTestSuccess := func(nodeId string) {
		if o.nodeBroker != nil {
			cmd := broker.NewSetNodeMLNodeOnboardingStateCommand(nodeId, string(apiconfig.MLNodeState_WAITING_FOR_POC))
			_ = o.nodeBroker.QueueMessage(cmd)
		}
	}

	// On success, set node MLNodeOnboardingState to WAITING_FOR_POC
	setTestSuccess(node.Id)

	logging.Info("MLnode test succeeded", types.Nodes, "node_id", node.Id)
	return &TestResult{NodeId: node.Id, Status: TestSuccess, Metrics: metrics}
}

func (o *MLnodeTestingOrchestrator) RunAutoTests(ctx context.Context, secondsUntilNextPoC int64) []TestResult {
	if !o.ShouldAutoTest(secondsUntilNextPoC) {
		return nil
	}
	nodes := o.configManager.GetNodes()
	results := make([]TestResult, 0, len(nodes))
	for _, n := range nodes {
		r := o.RunNodeTest(ctx, n)
		if r != nil {
			results = append(results, *r)
		}
	}
	return results
}

func (o *MLnodeTestingOrchestrator) RunManualTest(ctx context.Context, nodeId string) *TestResult {
	nodes := o.configManager.GetNodes()
	for _, n := range nodes {
		if n.Id == nodeId {
			return o.RunNodeTest(ctx, n)
		}
	}
	return &TestResult{NodeId: nodeId, Status: TestFailed, Error: "node_not_found"}
}
