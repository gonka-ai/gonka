package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
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

type MLnodeTestingOrchestrator struct {
	configManager    *apiconfig.ConfigManager
	blockTimeSeconds float64
}

func NewMLnodeTestingOrchestrator(cm *apiconfig.ConfigManager) *MLnodeTestingOrchestrator {
	return &MLnodeTestingOrchestrator{configManager: cm, blockTimeSeconds: 6.0}
}

func (o *MLnodeTestingOrchestrator) ShouldAutoTest(secondsUntilNextPoC int64) bool {
	return secondsUntilNextPoC > 3600
}

func (o *MLnodeTestingOrchestrator) RunNodeTest(ctx context.Context, node apiconfig.InferenceNodeConfig) *TestResult {
	version := o.configManager.GetCurrentNodeVersion()
	pocUrl := getPoCUrlWithVersion(node, version)
	inferenceUrl := formatURL(node.Host, node.InferencePort, node.InferenceSegment)
	client := mlnodeclient.NewNodeClient(pocUrl, inferenceUrl)

	metrics := TestMetrics{LoadMs: map[string]int64{}}

	for modelId, cfg := range node.Models {
		start := time.Now()
		err := client.InferenceUp(ctx, modelId, cfg.Args)
		metrics.LoadMs[modelId] = time.Since(start).Milliseconds()
		if err != nil {
			logging.Error("MLnode test failed during model loading", types.Nodes, "node_id", node.Id, "model", modelId, "error", err)
			return &TestResult{NodeId: node.Id, Status: TestFailed, FailingModel: modelId, Error: err.Error(), Metrics: metrics}
		}
	}

	startHealth := time.Now()
	ok, err := client.InferenceHealth(ctx)
	metrics.HealthMs = time.Since(startHealth).Milliseconds()
	if err != nil || !ok {
		if err != nil {
			logging.Error("MLnode health check failed", types.Nodes, "node_id", node.Id, "error", err)
			return &TestResult{NodeId: node.Id, Status: TestFailed, Error: err.Error(), Metrics: metrics}
		}
		logging.Error("MLnode health check not OK", types.Nodes, "node_id", node.Id)
		return &TestResult{NodeId: node.Id, Status: TestFailed, Error: "health_not_ok", Metrics: metrics}
	}

	metrics.RespMs = 0

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
