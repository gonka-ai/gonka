package admin

import (
	"context"
	"crypto/sha256"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/logging"
	"decentralized-api/mlnodeclient"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/productscience/inference/x/inference/types"
)

// TestResultStatus is the outcome of a one-shot MLnode validation.
type TestResultStatus string

const (
	TestSuccess TestResultStatus = "SUCCESS"
	TestFailed  TestResultStatus = "FAILED"
)

// TestResult is the report returned by MLNodeTester.Run for one node.
// Reported back to the HTTP caller verbatim; not stored on broker
// state. Per-model timings are recorded in LoadMs / HealthMs / RespMs so
// operators can see which model is slow on a multi-model node.
type TestResult struct {
	NodeId       string           `json:"node_id"`
	Status       TestResultStatus `json:"status"`
	FailingModel string           `json:"failing_model,omitempty"`
	Error        string           `json:"error,omitempty"`
	LoadMs       map[string]int64 `json:"load_ms,omitempty"`
	HealthMs     map[string]int64 `json:"health_ms,omitempty"`
	RespMs       map[string]int64 `json:"resp_ms,omitempty"`
	StartedAt    time.Time        `json:"started_at"`
	FinishedAt   time.Time        `json:"finished_at"`
	DurationMs   int64            `json:"duration_ms"`
	// Retryable marks a FAILED result that looks transient (RPC/network/health
	// blip, or an MLnode that answered "busy") rather than a deterministic
	// config error (model missing from governance, no supported model, a 4xx
	// describing the request itself). Auto-test uses this to retry transient
	// failures with backoff instead of latching TEST_FAILED until config
	// changes. SUCCESS results leave it false.
	Retryable bool `json:"retryable,omitempty"`
	// Fingerprint identifies the inputs this result was produced from (node
	// config + governance/PoC params + node version). A recorded result whose
	// fingerprint no longer matches the current inputs is stale and is not
	// reported as validated — see MLNodeTester.LastResult.
	Fingerprint string `json:"fingerprint,omitempty"`
}

// ErrTestInProgress is returned by MLNodeTester.Run when a test is
// already running for the same node id. Callers should translate this
// to HTTP 409 Conflict.
var ErrTestInProgress = errors.New("mlnode test already in progress for this node")

// ErrNodeNotFound is returned by MLNodeTester.Run when the requested
// node id is not present in the configured node list. Callers should
// translate this to HTTP 404 Not Found.
var ErrNodeNotFound = errors.New("node not configured")

// ErrTestBusy is returned by MLNodeTester.Run when another node's test is
// already running. Only one MLnode may be under test at a time: a test stops the
// node and loads models, so testing several at once could take a large share of a
// participant's capacity out of service simultaneously. Callers should translate
// this to HTTP 409 Conflict.
//
// Operator-facing consequence, accepted deliberately: while node A is under test,
// a manual test on node B is refused for up to NodeTestTimeoutSeconds. Serializing
// is the point — the alternative is letting an operator (or a batch registration)
// stop several nodes at once — and the 409 names the reason so it is diagnosable
// rather than looking like a failure of node B.
var ErrTestBusy = errors.New("another mlnode test is already in progress")

// MLNodeTester runs one-shot validation against a configured MLnode
// without involving the broker. It is safe for concurrent use; at most one
// test runs process-wide (see ErrTestBusy).
type MLNodeTester struct {
	configManager *apiconfig.ConfigManager
	factory       mlnodeclient.ClientFactory
	govModels     governanceModelSource

	mu sync.Mutex
	// inFlight maps node id -> handle for the running test, so Invalidate can
	// abort a test whose inputs just changed instead of letting it finish and
	// stop a node that has since been reconfigured.
	inFlight  map[string]*runningTest
	lastTests map[string]*TestResult
	revisions map[string]uint64
}

// runningTest is the handle to an in-flight test. invalidated is set only by
// Invalidate, so the test can distinguish "your node changed under you" from
// any other context cancellation — notably an HTTP client disconnecting, which
// produces the same context.Canceled but must still tear the MLnode down.
type runningTest struct {
	cancel      context.CancelFunc
	invalidated atomic.Bool
}

func NewMLNodeTester(cm *apiconfig.ConfigManager, factory mlnodeclient.ClientFactory, govModels governanceModelSource) *MLNodeTester {
	return &MLNodeTester{
		configManager: cm,
		factory:       factory,
		govModels:     govModels,
		inFlight:      map[string]*runningTest{},
		lastTests:     map[string]*TestResult{},
		revisions:     map[string]uint64{},
	}
}

func (t *MLNodeTester) governanceModels(ctx context.Context) ([]types.Model, error) {
	if t.govModels == nil {
		return nil, fmt.Errorf("governance model source not configured")
	}
	resp, err := t.govModels.GetGovernanceModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("get governance models: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("get governance models: empty response")
	}
	return resp.Model, nil
}

// LastResult returns the most recent TestResult for nodeId, or nil if no test
// has been recorded for that node yet or the recorded result was produced from
// inputs that have since changed. Callers therefore never see a pass that
// belongs to a superseded configuration, PoC param set, or node version.
func (t *MLNodeTester) LastResult(nodeId string) *TestResult {
	current := t.fingerprint(nodeId)
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.lastTests[nodeId]
	if !ok {
		return nil
	}
	// An empty recorded fingerprint means the result was recorded directly (in
	// tests) rather than through a real run; treat it as still applicable.
	if r.Fingerprint != "" && current != "" && r.Fingerprint != current {
		return nil
	}
	return r
}

// Invalidate drops any recorded result for nodeId and cancels a test currently
// running against it, so neither a stale pass nor a stale test's teardown can
// touch the node after it has been reconfigured or removed. A cancelled test
// records no result (see Run).
func (t *MLNodeTester) Invalidate(nodeId string) {
	t.mu.Lock()
	t.revisions[nodeId]++
	delete(t.lastTests, nodeId)
	running := t.inFlight[nodeId]
	t.mu.Unlock()

	if running != nil {
		// Mark before cancelling: the test reads this flag to decide whether to
		// skip its teardown Stop, and it must see the flag set by the time the
		// cancellation wakes it. Any other cancellation (a disconnected HTTP
		// client, a deadline) leaves the flag false and still tears down.
		running.invalidated.Store(true)
		running.cancel()
		logging.Info("Cancelled in-flight MLnode test because the node changed", types.Nodes, "node_id", nodeId)
	}
}

// Forget is Invalidate plus removal of the node's bookkeeping, for a node that
// no longer exists. Without it the revision counter for every node ever
// registered would be retained for the process's lifetime.
func (t *MLNodeTester) Forget(nodeId string) {
	t.Invalidate(nodeId)
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.lastTests, nodeId)
	// inFlight is owned by the running test's own defer; deleting it here would
	// let another test start against a node still being torn down. The revision
	// counter must also outlive an in-flight test: resetting it while a run
	// holds an older revision number would let that run's result be recorded
	// after all.
	if _, running := t.inFlight[nodeId]; !running {
		delete(t.revisions, nodeId)
	}
}

// IsRunning reports whether a test is currently in flight for nodeId.
func (t *MLNodeTester) IsRunning(nodeId string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, running := t.inFlight[nodeId]
	return running
}

// IsAnyRunning reports whether a test is in flight for any node.
func (t *MLNodeTester) IsAnyRunning() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.inFlight) > 0
}

// fingerprint hashes everything that determines what a test would do for
// nodeId: the node's config (host/ports/segments/models/args) plus the process
// state the test derives its launch plan from (PoC params, node version).
// Returns "" for an unknown node.
func (t *MLNodeTester) fingerprint(nodeId string) string {
	cfg, ok := t.findNode(nodeId)
	if !ok {
		return ""
	}
	return testInputFingerprint(cfg, t.configManager.GetPoCParams(), t.configManager.GetCurrentNodeVersion())
}

// testInputFingerprint is the pure part of fingerprint, so it can be tested and
// so callers can compute the value for a config snapshot they already hold.
// Model ids and PoC model ids are sorted, making the hash independent of Go's
// map iteration order.
func testInputFingerprint(cfg apiconfig.InferenceNodeConfig, pocParams apiconfig.PoCParamsCache, version string) string {
	h := sha256.New()
	writeField := func(parts ...string) {
		for _, p := range parts {
			h.Write([]byte(p))
			h.Write([]byte{0})
		}
	}
	writeField("v1", cfg.Id, cfg.Host, cfg.InferenceSegment, cfg.PoCSegment, version)
	writeField(strconv.Itoa(cfg.InferencePort), strconv.Itoa(cfg.PoCPort))

	modelIDs := make([]string, 0, len(cfg.Models))
	for id := range cfg.Models {
		modelIDs = append(modelIDs, id)
	}
	slices.Sort(modelIDs)
	for _, id := range modelIDs {
		writeField("model", id)
		for _, arg := range cfg.Models[id].Args {
			writeField("arg", arg)
		}
	}

	pocIDs := make([]string, 0, len(pocParams.Models))
	seqLens := map[string]int64{}
	for _, m := range pocParams.Models {
		pocIDs = append(pocIDs, m.ModelId)
		seqLens[m.ModelId] = m.SeqLen
	}
	slices.Sort(pocIDs)
	for _, id := range pocIDs {
		writeField("poc", id, strconv.FormatInt(seqLens[id], 10))
	}
	return hex.EncodeToString(h.Sum(nil))
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
	fingerprint := testInputFingerprint(cfg, t.configManager.GetPoCParams(), t.configManager.GetCurrentNodeVersion())

	// A test takes its node out of service for minutes. Serialize globally so a
	// batch registration (or an auto-test sweep across many nodes) can never
	// stop several MLnodes at once.
	runCtx, cancel := context.WithCancel(ctx)
	handle := &runningTest{cancel: cancel}
	t.mu.Lock()
	if _, running := t.inFlight[nodeId]; running {
		t.mu.Unlock()
		cancel()
		return nil, ErrTestInProgress
	}
	if len(t.inFlight) > 0 {
		t.mu.Unlock()
		cancel()
		return nil, ErrTestBusy
	}
	t.inFlight[nodeId] = handle
	revision := t.revisions[nodeId]
	t.mu.Unlock()

	defer func() {
		t.mu.Lock()
		delete(t.inFlight, nodeId)
		t.mu.Unlock()
		cancel()
	}()

	result := t.runOnce(runCtx, cfg, handle)
	result.Fingerprint = fingerprint

	// A cancelled run says nothing about the node: either the caller went away
	// or the node was reconfigured mid-test (Invalidate). Recording its failure
	// would latch a bogus TEST_FAILED and, worse, describe inputs that no
	// longer exist. Report it to the caller but keep it out of the record.
	if ctxErr := runCtx.Err(); ctxErr != nil && result.Status == TestFailed {
		return result, nil
	}

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
func (t *MLNodeTester) LaunchPlans(ctx context.Context, nodeId string) (*LaunchPlanReport, error) {
	cfg, ok := t.findNode(nodeId)
	if !ok {
		return nil, ErrNodeNotFound
	}
	governanceModels, err := t.governanceModels(ctx)
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

// runOnce performs the actual validation. handle may be nil for callers that do
// not participate in invalidation; a nil handle means "never invalidated", so
// teardown always runs.
func (t *MLNodeTester) runOnce(ctx context.Context, cfg apiconfig.InferenceNodeConfig, handle *runningTest) *TestResult {
	started := time.Now()
	result := &TestResult{
		NodeId:    cfg.Id,
		Status:    TestSuccess,
		LoadMs:    map[string]int64{},
		HealthMs:  map[string]int64{},
		RespMs:    map[string]int64{},
		StartedAt: started,
	}
	defer func() {
		result.FinishedAt = time.Now()
		result.DurationMs = result.FinishedAt.Sub(started).Milliseconds()
	}()

	governanceModels, err := t.governanceModels(ctx)
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

	// Best-effort cleanup at the end so a successful test leaves the MLnode idle,
	// not stuck in inference mode. Derived from ctx via WithoutCancel so teardown
	// still runs when the caller's context is done — a deadline, or an operator
	// closing the tab mid-test, must not leave the node with the test's model
	// loaded and nobody to stop it.
	//
	// The one case we skip is invalidation: the node was reconfigured or deleted
	// under us, so what is running now may be a different deployment and stopping
	// it would be wrong. That is keyed off an explicit flag rather than
	// ctx.Err(), because a disconnected HTTP client produces the very same
	// context.Canceled and must still be torn down.
	defer func() {
		if handle != nil && handle.invalidated.Load() {
			logging.Info("Skipping MLnode test teardown: node was reconfigured mid-test", types.Nodes, "node_id", cfg.Id)
			return
		}
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), nodeStopTimeout)
		defer cancel()
		if err := client.Stop(stopCtx); err != nil {
			logging.Warn("MLnode test teardown stop failed", types.Nodes, "node_id", cfg.Id, "error", err)
		}
	}()

	for _, plan := range launchPlans {
		if err := ctx.Err(); err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "test aborted: " + err.Error()
			result.Retryable = true
			return result
		}

		// Match the broker's redeploy behavior: stop any existing inference
		// process before every InferenceUp, including the first model. Bound by
		// ctx so a cancelled/expired test does not keep poking the node.
		stopCtx, cancel := context.WithTimeout(ctx, nodeStopTimeout)
		err := broker.StopBeforeModelLaunch(stopCtx, client)
		cancel()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "failed to stop node before inference up: " + err.Error()
			result.Retryable = isRetryableMLNodeError(err)
			return result
		}

		modelStart := time.Now()
		err = client.InferenceUp(ctx, plan.ModelID, plan.Args)
		result.LoadMs[plan.ModelID] = time.Since(modelStart).Milliseconds()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = err.Error()
			// A 409 ("vLLM already running/starting") or a 5xx is worth
			// retrying; a bad request or an OOM-on-load is not. Previously
			// every InferenceUp failure was treated as permanent, which
			// latched TEST_FAILED on a node that was merely busy.
			result.Retryable = isRetryableMLNodeError(err)
			return result
		}

		healthStart := time.Now()
		ok, err := client.InferenceHealth(ctx)
		result.HealthMs[plan.ModelID] = time.Since(healthStart).Milliseconds()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "health check error: " + err.Error()
			result.Retryable = isRetryableMLNodeError(err)
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
		err = client.Inference(ctx, plan.ModelID)
		result.RespMs[plan.ModelID] = time.Since(respStart).Milliseconds()
		if err != nil {
			result.Status = TestFailed
			result.FailingModel = plan.ModelID
			result.Error = "inference request error: " + err.Error()
			result.Retryable = isRetryableMLNodeError(err)
			return result
		}
	}

	return result
}

// nodeStopTimeout bounds a single /stop call made by the tester.
const nodeStopTimeout = 10 * time.Second

// isRetryableMLNodeError classifies an MLnode call failure. Transport errors
// and context expiry are transient by nature; an HTTP status is transient only
// when the node said "busy"/"server error" (409/429/408/5xx). A status that
// describes the request itself (400/404/422) would fail identically on retry,
// so it stays permanent and waits for a config change.
func isRetryableMLNodeError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if mlnodeclient.IsStatusError(err) {
		return mlnodeclient.IsTransientStatus(err)
	}
	// No HTTP status at all: the node was unreachable or the reply was
	// unparseable — an infra blip, not a config error.
	return true
}
