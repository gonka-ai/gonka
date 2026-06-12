package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// governanceModelSource provides the chain's governance models (with their
// args). Narrow interface so the tester can merge governance args the same
// way the broker does, and so it is easy to stub in tests.
type governanceModelSource interface {
	GetGovernanceModels() (*types.QueryModelsAllResponse, error)
}

// TestResultStatus is the outcome of a one-shot MLnode validation.
type TestResultStatus string

const (
	TestSuccess TestResultStatus = "SUCCESS"
	TestFailed  TestResultStatus = "FAILED"
)

// TestResult is the report returned by MLNodeTester.Run for one node.
// Reported back to the HTTP caller verbatim; not stored on broker
// state. Each per-model load time is recorded in LoadMs to help
// operators spot slow model loads.
type TestResult struct {
	NodeId       string           `json:"node_id"`
	Status       TestResultStatus `json:"status"`
	FailingModel string           `json:"failing_model,omitempty"`
	Error        string           `json:"error,omitempty"`
	LoadMs       map[string]int64 `json:"load_ms,omitempty"`
	HealthMs     int64            `json:"health_ms,omitempty"`
	RespMs       int64            `json:"resp_ms,omitempty"`
	StartedAt    time.Time        `json:"started_at"`
	DurationMs   int64            `json:"duration_ms"`
	// Retryable marks a FAILED result that looks transient (RPC/network/health
	// blip) rather than a deterministic config error (model missing from
	// governance, no supported model). Auto-test uses this to retry transient
	// failures with backoff instead of latching TEST_FAILED until config
	// changes. SUCCESS results leave it false.
	Retryable bool `json:"retryable,omitempty"`
}

// ErrTestInProgress is returned by MLNodeTester.Run when a test is
// already running for the same node id. Callers should translate this
// to HTTP 409 Conflict.
var ErrTestInProgress = errors.New("mlnode test already in progress for this node")

// ErrNodeNotFound is returned by MLNodeTester.Run when the requested
// node id is not present in the configured node list. Callers should
// translate this to HTTP 404 Not Found.
var ErrNodeNotFound = errors.New("node not configured")

// MLNodeTester runs one-shot validation against a configured MLnode
// without involving the broker. It is safe for concurrent use; a
// per-node mutex prevents two tests running against the same node.
type MLNodeTester struct {
	configManager *apiconfig.ConfigManager
	factory       mlnodeclient.ClientFactory
	govModels     governanceModelSource

	mu        sync.Mutex
	inFlight  map[string]bool
	lastTests map[string]*TestResult
	revisions map[string]uint64
}

func NewMLNodeTester(cm *apiconfig.ConfigManager, factory mlnodeclient.ClientFactory, govModels governanceModelSource) *MLNodeTester {
	return &MLNodeTester{
		configManager: cm,
		factory:       factory,
		govModels:     govModels,
		inFlight:      map[string]bool{},
		lastTests:     map[string]*TestResult{},
		revisions:     map[string]uint64{},
	}
}

func (t *MLNodeTester) governanceModels() ([]types.Model, error) {
	if t.govModels == nil {
		return nil, fmt.Errorf("governance model source not configured")
	}
	resp, err := t.govModels.GetGovernanceModels()
	if err != nil {
		return nil, fmt.Errorf("get governance models: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("get governance models: empty response")
	}
	return resp.Model, nil
}

// LastResult returns the most recent TestResult for nodeId, or nil
// if no test has been recorded for that node yet.
func (t *MLNodeTester) LastResult(nodeId string) *TestResult {
	t.mu.Lock()
	defer t.mu.Unlock()
	if r, ok := t.lastTests[nodeId]; ok {
		return r
	}
	return nil
}

// Invalidate drops any recorded result for nodeId, so a stale pass from a
// previous configuration is not reported as validated after the node is
// changed.
func (t *MLNodeTester) Invalidate(nodeId string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.revisions[nodeId]++
	delete(t.lastTests, nodeId)
}

// IsRunning reports whether a test is currently in flight for nodeId.
func (t *MLNodeTester) IsRunning(nodeId string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.inFlight[nodeId]
}

// Run validates the MLnode behind nodeId: for each configured model,
// it loads the model and measures load time; then it runs a single
// inference-health check. Models that load successfully are torn down
// via Stop on completion. Returns ErrNodeNotFound or ErrTestInProgress
// before launching the test.
func (t *MLNodeTester) Run(ctx context.Context, nodeId string) (*TestResult, error) {
	t.mu.Lock()
	if t.inFlight[nodeId] {
		t.mu.Unlock()
		return nil, ErrTestInProgress
	}
	cfg, ok := t.findNode(nodeId)
	if !ok {
		t.mu.Unlock()
		return nil, ErrNodeNotFound
	}
	t.inFlight[nodeId] = true
	revision := t.revisions[nodeId]
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.inFlight, nodeId)
		t.mu.Unlock()
	}()

	result := t.runOnce(ctx, cfg)

	t.recordResult(nodeId, revision, result)
	return result, nil
}

func (t *MLNodeTester) recordResult(nodeId string, revision uint64, result *TestResult) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.revisions[nodeId] == revision {
		t.lastTests[nodeId] = result
	}
}

// HasNode reports whether nodeId is present in the configured node list.
// Used by handlers to return 404 before applying test-safety gates so an
// unknown node never masquerades as "blocked".
func (t *MLNodeTester) HasNode(nodeId string) bool {
	_, ok := t.findNode(nodeId)
	return ok
}

func (t *MLNodeTester) findNode(nodeId string) (apiconfig.InferenceNodeConfig, bool) {
	for _, n := range t.configManager.GetNodes() {
		if n.Id == nodeId {
			return n.DeepCopy(), true
		}
	}
	return apiconfig.InferenceNodeConfig{}, false
}

// buildTestableLaunchPlans builds launch plans for the node models the broker
// could actually launch: those present in governance. Models configured on the
// node but absent from governance are returned in skipped (not failed), since
// the broker would ignore them too. Plans are returned in stable model-id order.
func buildTestableLaunchPlans(governanceModels []types.Model, nodeModels map[string]broker.ModelArgs) (plans []broker.ModelLaunchPlan, skipped []string, err error) {
	govByID := make(map[string]types.Model, len(governanceModels))
	for _, m := range governanceModels {
		govByID[m.Id] = m
	}
	ids := make([]string, 0, len(nodeModels))
	for id := range nodeModels {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		gm, ok := govByID[id]
		if !ok {
			skipped = append(skipped, id)
			continue
		}
		plan, planErr := broker.BuildModelLaunchPlan(gm, nodeModels)
		if planErr != nil {
			return nil, nil, planErr
		}
		plans = append(plans, plan)
	}
	return plans, skipped, nil
}

// LaunchPlanEntry is one model the node would launch, with the final merged
// argument list (governance args + node-local args) — the same plan the
// broker's InferenceUpNodeCommand and the pre-PoC test use.
type LaunchPlanEntry struct {
	ModelID string   `json:"model_id"`
	Args    []string `json:"args"`
}

// LaunchPlanReport answers "what launch plan would this node use right now?"
// without touching the MLnode: the models that would be launched with their
// final args, plus the configured models that would NOT launch and why —
// absent from governance (skipped) or filtered out by the current PoC params
// (unsupported).
type LaunchPlanReport struct {
	NodeId            string            `json:"node_id"`
	Plans             []LaunchPlanEntry `json:"plans"`
	SkippedModels     []string          `json:"skipped_models,omitempty"`
	UnsupportedModels []string          `json:"unsupported_models,omitempty"`
}

// LaunchPlans computes the launch plans for nodeId using exactly the model
// selection the pre-PoC test (and the broker) apply: filter the node's
// configured models by the current PoC params, keep those present in
// governance, and merge governance args with node-local args. Read-only —
// no MLnode network calls, nothing recorded. Returns ErrNodeNotFound for an
// unknown node; a governance query failure propagates as an error.
func (t *MLNodeTester) LaunchPlans(nodeId string) (*LaunchPlanReport, error) {
	cfg, ok := t.findNode(nodeId)
	if !ok {
		return nil, ErrNodeNotFound
	}
	governanceModels, err := t.governanceModels()
	if err != nil {
		return nil, err
	}

	nodeModels := make(map[string]broker.ModelArgs, len(cfg.Models))
	for modelID, modelConfig := range cfg.Models {
		nodeModels[modelID] = broker.ModelArgs{Args: modelConfig.Args}
	}
	supported := broker.SupportedNodeModels(nodeModels, t.configManager.GetPoCParams())
	var unsupported []string
	for modelID := range nodeModels {
		if _, ok := supported[modelID]; !ok {
			unsupported = append(unsupported, modelID)
		}
	}
	slices.Sort(unsupported)

	plans, skipped, err := buildTestableLaunchPlans(governanceModels, supported)
	if err != nil {
		return nil, err
	}
	report := &LaunchPlanReport{
		NodeId:            cfg.Id,
		Plans:             make([]LaunchPlanEntry, 0, len(plans)),
		SkippedModels:     skipped,
		UnsupportedModels: unsupported,
	}
	for _, p := range plans {
		report.Plans = append(report.Plans, LaunchPlanEntry{ModelID: p.ModelID, Args: p.Args})
	}
	return report, nil
}

func (t *MLNodeTester) runOnce(ctx context.Context, cfg apiconfig.InferenceNodeConfig) *TestResult {
	started := time.Now()
	result := &TestResult{
		NodeId:    cfg.Id,
		Status:    TestSuccess,
		LoadMs:    map[string]int64{},
		StartedAt: started,
	}
	defer func() {
		result.DurationMs = time.Since(started).Milliseconds()
	}()

	governanceModels, err := t.governanceModels()
	if err != nil {
		result.Status = TestFailed
		result.Error = err.Error()
		// Governance is fetched over RPC; a failure here is transient.
		result.Retryable = true
		return result
	}

	nodeModels := make(map[string]broker.ModelArgs, len(cfg.Models))
	for modelID, modelConfig := range cfg.Models {
		nodeModels[modelID] = broker.ModelArgs{Args: modelConfig.Args}
	}

	// Align the readiness test with broker model semantics so it does not
	// false-fail on models the broker would never launch this epoch:
	//   1. Drop models unsupported by the current PoC params — the broker
	//      filters these out before it resolves which model to launch.
	//   2. Among the remaining models, test those present in governance and
	//      SKIP (rather than hard-fail on) configured models absent from
	//      governance — a node carrying an old/backup model would otherwise be
	//      marked TEST_FAILED even though the broker would simply ignore it.
	// Every model that survives both filters is still loaded and probed, so a
	// multi-model node keeps full coverage of the models it can actually serve.
	supported := broker.SupportedNodeModels(nodeModels, t.configManager.GetPoCParams())
	if len(supported) == 0 {
		result.Status = TestFailed
		result.Error = "no configured model is supported by the current PoC params"
		return result
	}
	launchPlans, skipped, err := buildTestableLaunchPlans(governanceModels, supported)
	if err != nil {
		result.Status = TestFailed
		result.Error = err.Error()
		return result
	}
	if len(launchPlans) == 0 {
		result.Status = TestFailed
		result.Error = "no configured model is both supported by the current PoC params and present in governance"
		return result
	}
	if len(skipped) > 0 {
		logging.Info("MLnode test skipping models absent from governance", types.Nodes,
			"node_id", cfg.Id, "skipped_models", skipped)
	}

	// Build URLs the same way the broker does for versioned (rolling-
	// upgrade) deployments: insert the current node version into the path
	// when set, so the test hits the same MLnode routes the broker uses
	// (an unversioned URL would hit the wrong endpoint and falsely fail).
	version := t.configManager.GetCurrentNodeVersion()
	pocUrl := apiconfig.MLNodeURL(cfg.Host, cfg.PoCPort, cfg.PoCSegment, version)
	inferenceUrl := apiconfig.MLNodeURL(cfg.Host, cfg.InferencePort, cfg.InferenceSegment, version)
	client := t.factory.CreateClient(pocUrl, inferenceUrl)

	// Best-effort cleanup at the end so a successful test leaves the
	// MLnode idle, not stuck in inference mode.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()

	for _, plan := range launchPlans {
		// Match the broker's redeploy behavior: stop any existing inference
		// process before every InferenceUp, including the first model.
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := broker.StopBeforeModelLaunch(stopCtx, client)
		cancel()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "failed to stop node before inference up: " + err.Error()
			// MLnode unreachable for stop is a transient/infra failure.
			result.Retryable = true
			return result
		}

		modelStart := time.Now()
		if err := client.InferenceUp(ctx, plan.ModelID, plan.Args); err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = err.Error()
			result.LoadMs[plan.ModelID] = time.Since(modelStart).Milliseconds()
			return result
		}
		result.LoadMs[plan.ModelID] = time.Since(modelStart).Milliseconds()

		healthStart := time.Now()
		ok, err := client.InferenceHealth(ctx)
		result.HealthMs = time.Since(healthStart).Milliseconds()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "health check error: " + err.Error()
			// A health probe transport error is transient.
			result.Retryable = true
			return result
		}
		if !ok {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "health check returned not ok"
			return result
		}

		// Response validation: send a real inference request for THIS model
		// and validate the response, recording response time.
		respStart := time.Now()
		if err := client.Inference(ctx, plan.ModelID); err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "inference request error: " + err.Error()
			result.RespMs = time.Since(respStart).Milliseconds()
			// A transport error on the probe request is transient.
			result.Retryable = true
			return result
		}
		result.RespMs = time.Since(respStart).Milliseconds()
	}

	return result
}
