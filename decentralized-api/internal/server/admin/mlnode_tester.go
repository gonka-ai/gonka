package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/mlnodeclient"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

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

	mu        sync.Mutex
	inFlight  map[string]bool
	lastTests map[string]*TestResult
}

func NewMLNodeTester(cm *apiconfig.ConfigManager, factory mlnodeclient.ClientFactory) *MLNodeTester {
	return &MLNodeTester{
		configManager: cm,
		factory:       factory,
		inFlight:      map[string]bool{},
		lastTests:     map[string]*TestResult{},
	}
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
	cfg, ok := t.findNode(nodeId)
	if !ok {
		return nil, ErrNodeNotFound
	}

	t.mu.Lock()
	if t.inFlight[nodeId] {
		t.mu.Unlock()
		return nil, ErrTestInProgress
	}
	t.inFlight[nodeId] = true
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.inFlight, nodeId)
		t.mu.Unlock()
	}()

	result := t.runOnce(ctx, cfg)

	t.mu.Lock()
	t.lastTests[nodeId] = result
	t.mu.Unlock()
	return result, nil
}

// versionedURL builds an MLnode URL, inserting the node version into the
// path when non-empty — matching broker.Node.PoCUrlWithVersion /
// InferenceUrlWithVersion so the tester reaches the same routes the broker
// uses in versioned (rolling-upgrade) deployments.
func versionedURL(host string, port int, segment, version string) string {
	if version == "" {
		return fmt.Sprintf("http://%s:%d%s", host, port, segment)
	}
	return fmt.Sprintf("http://%s:%d/%s%s", host, port, version, segment)
}

func (t *MLNodeTester) findNode(nodeId string) (apiconfig.InferenceNodeConfig, bool) {
	for _, n := range t.configManager.GetNodes() {
		if n.Id == nodeId {
			return n, true
		}
	}
	return apiconfig.InferenceNodeConfig{}, false
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

	// Build URLs the same way the broker does for versioned (rolling-
	// upgrade) deployments: insert the current node version into the path
	// when set, so the test hits the same MLnode routes the broker uses
	// (an unversioned URL would hit the wrong endpoint and falsely fail).
	version := t.configManager.GetCurrentNodeVersion()
	pocUrl := versionedURL(cfg.Host, cfg.PoCPort, cfg.PoCSegment, version)
	inferenceUrl := versionedURL(cfg.Host, cfg.InferencePort, cfg.InferenceSegment, version)
	client := t.factory.CreateClient(pocUrl, inferenceUrl)

	// Best-effort cleanup at the end so a successful test leaves the
	// MLnode idle, not stuck in inference mode.
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()

	// Iterate models in a stable (sorted) order so FailingModel and the
	// per-model load sequence are deterministic across runs — Go map
	// iteration order is randomized.
	modelIds := make([]string, 0, len(cfg.Models))
	for modelId := range cfg.Models {
		modelIds = append(modelIds, modelId)
	}
	sort.Strings(modelIds)

	lastModel := ""
	for _, modelId := range modelIds {
		modelCfg := cfg.Models[modelId]
		modelStart := time.Now()
		if err := client.InferenceUp(ctx, modelId, modelCfg.Args); err != nil {
			result.Status = TestFailed
			result.FailingModel = modelId
			result.Error = err.Error()
			result.LoadMs[modelId] = time.Since(modelStart).Milliseconds()
			return result
		}
		result.LoadMs[modelId] = time.Since(modelStart).Milliseconds()
		lastModel = modelId
	}

	healthStart := time.Now()
	ok, err := client.InferenceHealth(ctx)
	result.HealthMs = time.Since(healthStart).Milliseconds()
	if err != nil {
		result.Status = TestFailed
		result.Error = "health check error: " + err.Error()
		return result
	}
	if !ok {
		result.Status = TestFailed
		result.Error = "health check returned not ok"
		return result
	}

	// Response validation: send one real inference request against the
	// loaded model and validate the response, recording response time.
	// (Uses the existing host:port URL construction; see #717 follow-up
	// in proposals/onboarding-clarity-v1/README.md.)
	if lastModel != "" {
		respStart := time.Now()
		if err := client.Inference(ctx, lastModel); err != nil {
			result.Status = TestFailed
			result.FailingModel = lastModel
			result.Error = "inference request error: " + err.Error()
			result.RespMs = time.Since(respStart).Milliseconds()
			return result
		}
		result.RespMs = time.Since(respStart).Milliseconds()
	}

	return result
}
