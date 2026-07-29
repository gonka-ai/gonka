package admin

import (
	"context"
	"decentralized-api/apiconfig"
	"decentralized-api/broker"
	"decentralized-api/chainphase"
	"decentralized-api/mlnodeclient"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// registerTestNode adds a node to the config manager and registers it
// with the broker so the admin handlers can see it. Mirrors the
// known-good node shape used by TestPostVersionStatus.
func registerTestNode(t *testing.T, s *Server, cm *apiconfig.ConfigManager, id string) apiconfig.InferenceNodeConfig {
	t.Helper()
	// The broker rejects duplicate host+port, so give each node its own ports.
	// The first node keeps 8080/8081 so testNodePoCURL stays valid.
	offset := len(cm.GetNodes()) * 10
	nodeConfig := apiconfig.InferenceNodeConfig{
		Id:               id,
		Host:             "localhost",
		InferencePort:    8080 + offset,
		InferenceSegment: "/api/v1",
		PoCPort:          8081 + offset,
		PoCSegment:       "/api/v1",
		MaxConcurrent:    3,
		Models: map[string]apiconfig.ModelConfig{
			"test-model": {Args: []string{}},
		},
	}
	nodes := append(cm.GetNodes(), nodeConfig)
	assert.NoError(t, cm.SetNodes(nodes))

	respChan := s.nodeBroker.LoadNodeToBroker(&nodeConfig)
	select {
	case response := <-respChan:
		if response.Error != nil || response.Node == nil {
			t.Fatalf("failed to register node %s", id)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out registering node %s", id)
	}
	return nodeConfig
}

// pocURL the MLNodeTester will compute for registerTestNode's node.
const testNodePoCURL = "http://localhost:8081/api/v1"

// TestPostNodeTest exercises the manual MLnode test trigger end-to-end
// through the HTTP layer (the automated stand-in for "hit POST
// /admin/v1/nodes/:id/test on a live node"), using a mocked MLnode.
func TestPostNodeTest(t *testing.T) {
	s, cm, factory := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")

	// Push the next PoC far out so the manual-test gate is open for the
	// success/failure cases (setupTestServer's default puts PoC inside the
	// must-be-online window, which would correctly 409 — covered separately).
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)

	t.Run("success returns 200 with SUCCESS", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var result TestResult
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		assert.Equal(t, TestSuccess, result.Status)
		assert.Equal(t, "node-1", result.NodeId)
	})

	t.Run("model load failure returns 200 with FAILED + failing model", func(t *testing.T) {
		// Stub the same mock client the tester will use (factory keys by
		// pocURL) so InferenceUp fails like a misconfigured MLnode.
		mc := factory.CreateClient(testNodePoCURL, "").(*mlnodeclient.MockClient)
		mc.InferenceUpError = errors.New("CUDA out of memory")
		defer func() { mc.InferenceUpError = nil }()

		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var result TestResult
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &result))
		assert.Equal(t, TestFailed, result.Status)
		assert.Equal(t, "test-model", result.FailingModel)
	})

	t.Run("unknown node returns 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/does-not-exist/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("imminent PoC returns 409", func(t *testing.T) {
		// Node should be online for an imminent PoC: refuse to test it.
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 9999, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)
		defer s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 1, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	// The boundary the old gate let through: outside the 600s must-be-online
	// window, but close enough that a full-length test would still be running
	// when the node has to be online.
	t.Run("just outside the online window still returns 409", func(t *testing.T) {
		// 601s before PoC at 6s/block ≈ 100 blocks.
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 10000 - 100, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)
		defer func() {
			s.phaseTracker.Update(
				chainphase.BlockInfo{Height: 1, Hash: "h"},
				&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
				&types.EpochParams{},
				true,
				nil,
			)
		}()

		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	// Fail-closed: an unknown schedule must not allow a multi-minute test.
	t.Run("unsynced chain returns 409", func(t *testing.T) {
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 1, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			false,
			nil,
		)
		defer s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 1, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusConflict, rec.Code)
	})

	// An unknown node must 404 even when PoC is imminent (existence is checked
	// before the timing gate).
	t.Run("unknown node returns 404 even near PoC", func(t *testing.T) {
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 9999, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)
		defer s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 1, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)

		req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/does-not-exist/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

// TestPostNodeTest_RefusesRoutableNode is the HTTP-layer form of the central
// safety fix: a node the router can pick — up in INFERENCE with an epoch model,
// even with no lock held and no epoch assignment — must not be testable. Before
// the fix such a node passed the idle check and the test stopped it outside the
// broker's state machine, failing in-flight inference requests.
func TestPostNodeTest_RefusesRoutableNode(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")
	farFromPoC(t, s)

	// Drive the node into the state the router accepts. RegisterNode already
	// populated EpochModels from the configured model.
	setNodeStatuses(t, s, "node-1", types.HardwareNodeStatus_INFERENCE, types.HardwareNodeStatus_INFERENCE)

	req := httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Contains(t, rec.Body.String(), "can receive requests at any time")

	// Same node before it is up: the onboarding case must stay testable.
	setNodeStatuses(t, s, "node-1", types.HardwareNodeStatus_INFERENCE, types.HardwareNodeStatus_STOPPED)
	req = httptest.NewRequest(http.MethodPost, "/admin/v1/nodes/node-1/test", nil)
	rec = httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// nodeView is a lightweight projection of the GET /nodes response used
// to assert the JSON shape the way an external consumer (the admin UI)
// reads it — by field name, not by binding to the strongly-typed
// broker structs (whose proto enums don't round-trip through
// encoding/json).
type nodeView struct {
	Node struct {
		Id string `json:"id"`
	} `json:"node"`
	State      json.RawMessage `json:"state"`
	Onboarding *struct {
		ParticipantState string `json:"participant_state"`
		MLNodeState      string `json:"mlnode_state"`
		UserMessage      string `json:"user_message"`
	} `json:"onboarding"`
}

func getNodeViews(t *testing.T, s *Server) []nodeView {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes", nil)
	rec := httptest.NewRecorder()
	s.e.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	var nodes []nodeView
	if err := json.Unmarshal(rec.Body.Bytes(), &nodes); err != nil {
		t.Fatalf("unmarshal err: %v\nbody: %s", err, rec.Body.String())
	}
	return nodes
}

func TestNodeStateTestBlockReason(t *testing.T) {
	// An enabled node in the inference phase of a later epoch: ShouldBeOperational
	// is true, so admin state does not affect the outcome of these cases.
	enabled := epochPhase{epoch: 9, phase: types.InferencePhase, known: true}
	tests := []struct {
		name  string
		state broker.NodeState
		ep    epochPhase
		// epUnknown drives the fail-closed path: leave ep zero and assert the
		// behavior when the chain phase is not available.
		epUnknown bool
		want      string
	}{
		{name: "idle node is testable", state: broker.NodeState{}, want: ""},
		{
			name:  "assigned node",
			state: broker.NodeState{EpochMLNodes: map[string]types.MLNodeInfo{"model": {}}},
			want:  "node is assigned in the current epoch",
		},
		{
			name:  "preserved inference node",
			state: broker.NodeState{PreservedModels: map[string]bool{"model": true}},
			want:  "node is preserved for inference service",
		},
		{
			name:  "locked node",
			state: broker.NodeState{LockCount: 1},
			want:  "node is serving one or more inference requests",
		},
		{
			name:  "reconciling node",
			state: broker.NodeState{ReconcileInfo: &broker.ReconcileInfo{Status: types.HardwareNodeStatus_INFERENCE}},
			want:  "node is currently reconciling",
		},
		{
			name:  "poc node",
			state: broker.NodeState{IntendedStatus: types.HardwareNodeStatus_POC},
			want:  "node is participating in PoC",
		},
		{
			// The bug this guards: an unassigned node still gets EpochModels
			// populated from its configured model (broker
			// populateNodeWithConfiguredModel), so once it is up in INFERENCE the
			// router can hand it a request even with LockCount == 0 and no epoch
			// assignment. Testing it would stop it outside the state machine.
			name: "routable inference node with no lock is busy",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
				// RegisterNode sets {Enabled: true, Epoch: currentEpoch}, so this
				// is what a real registered node carries — not the zero value,
				// which would mean "disabled since epoch 0".
				AdminState: broker.AdminState{Enabled: true, Epoch: 5},
			},
			want: "node is serving inference and can receive requests at any time",
		},
		{
			// The onboarding case must stay testable: intended INFERENCE but not
			// yet up, so the router cannot pick it.
			name: "node not yet up is testable",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_STOPPED,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
			},
			want: "",
		},
		{
			// Up, but nothing to serve: the router rejects it on the epoch-model
			// check, so it is genuinely idle.
			name: "inference node without epoch models is testable",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
			},
			want: "",
		},
		{
			name: "failed node is testable",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_FAILED,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
			},
			want: "",
		},
		{
			// An admin-disabled node is not routable, so it must stay testable —
			// this is the operator's remediation when a serving node is refused.
			// Without the ShouldBeOperational term it was refused anyway, leaving
			// no way to test it until the reconciler drove it out of INFERENCE.
			name: "admin-disabled node is testable",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
				AdminState:     broker.AdminState{Enabled: false, Epoch: 5},
			},
			ep:   enabled, // epoch 9 > disabled-at epoch 5 → not operational
			want: "",
		},
		{
			// Explicitly enabled and past its epoch: routable, so refused.
			name: "admin-enabled node is busy",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
				AdminState:     broker.AdminState{Enabled: true, Epoch: 5},
			},
			ep:   enabled,
			want: "node is serving inference and can receive requests at any time",
		},
		{
			// Fail-closed: with an unknown phase we cannot evaluate admin state, so
			// an otherwise-routable node is refused rather than assumed disabled.
			name: "unknown phase refuses an otherwise routable node",
			state: broker.NodeState{
				IntendedStatus: types.HardwareNodeStatus_INFERENCE,
				CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
				EpochModels:    map[string]types.Model{"m": {Id: "m"}},
				AdminState:     broker.AdminState{Enabled: false, Epoch: 5},
			},
			epUnknown: true,
			want:      "node is serving inference and can receive requests at any time",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := tt.ep
			if !tt.epUnknown && !ep.known {
				ep = enabled // default: a known phase where admin state is a no-op
			}
			assert.Equal(t, tt.want, nodeStateTestBlockReason(tt.state, ep))
		})
	}
}

// TestIsRoutableForInference_MirrorsNodeAvailable pins the invariant the safety
// check depends on: isRoutableForInference must never be weaker than the
// router's own predicate. A false positive (refusing a node the router would
// skip) is safe; a false negative would let a test stop a node mid-request.
func TestIsRoutableForInference_MirrorsNodeAvailable(t *testing.T) {
	ep := epochPhase{epoch: 9, phase: types.InferencePhase, known: true}
	routable := broker.NodeState{
		IntendedStatus: types.HardwareNodeStatus_INFERENCE,
		CurrentStatus:  types.HardwareNodeStatus_INFERENCE,
		EpochModels:    map[string]types.Model{"m": {Id: "m"}},
		AdminState:     broker.AdminState{Enabled: true, Epoch: 5},
	}
	assert.True(t, isRoutableForInference(routable, ep))

	// Each condition the router requires, removed one at a time.
	t.Run("intended not inference", func(t *testing.T) {
		s := routable
		s.IntendedStatus = types.HardwareNodeStatus_STOPPED
		assert.False(t, isRoutableForInference(s, ep))
	})
	t.Run("current not inference", func(t *testing.T) {
		s := routable
		s.CurrentStatus = types.HardwareNodeStatus_STOPPED
		assert.False(t, isRoutableForInference(s, ep))
	})
	t.Run("reconciling", func(t *testing.T) {
		s := routable
		s.ReconcileInfo = &broker.ReconcileInfo{Status: types.HardwareNodeStatus_INFERENCE}
		assert.False(t, isRoutableForInference(s, ep))
	})
	t.Run("no epoch models", func(t *testing.T) {
		s := routable
		s.EpochModels = nil
		assert.False(t, isRoutableForInference(s, ep))
	})
	t.Run("not operational", func(t *testing.T) {
		s := routable
		s.AdminState = broker.AdminState{Enabled: false, Epoch: 5}
		assert.False(t, isRoutableForInference(s, ep),
			"must agree with ShouldBeOperational, which the router also consults")
		assert.False(t, s.ShouldBeOperational(ep.epoch, ep.phase))
	})
}

// TestMaybeAutoTest verifies the auto-trigger fires a background test
// only when PoC is far away, recording the result on the tester (which
// then surfaces via GET /nodes) without any broker write.
func TestMaybeAutoTest(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")

	t.Run("does not fire when PoC is near", func(t *testing.T) {
		// setupTestServer's phase tracker puts PoC ~594s away (< 1h).
		s.maybeAutoTest("node-1")
		time.Sleep(100 * time.Millisecond)
		assert.Nil(t, s.tester.LastResult("node-1"))
	})

	t.Run("fires when PoC is far away", func(t *testing.T) {
		// Push the next PoC far out so the gate opens.
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 1, Hash: "h"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
			&types.EpochParams{},
			true,
			nil,
		)
		s.maybeAutoTest("node-1")

		var got *TestResult
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if got = s.tester.LastResult("node-1"); got != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if assert.NotNil(t, got, "auto-test should have recorded a result") {
			assert.Equal(t, TestSuccess, got.Status)
		}
	})
}

func TestMaybeAutoTest_RetriesRetryableFailureAfterBackoff(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)

	t.Run("recent retryable failure stays in backoff", func(t *testing.T) {
		recentFailure := &TestResult{
			NodeId:    "node-1",
			Status:    TestFailed,
			Retryable: true,
			StartedAt: time.Now(),
		}
		s.tester.recordResult("node-1", 0, recentFailure)

		s.maybeAutoTest("node-1")
		time.Sleep(100 * time.Millisecond)

		got := s.tester.LastResult("node-1")
		assert.Same(t, recentFailure, got)
		assert.Equal(t, TestFailed, got.Status)
	})

	t.Run("old retryable failure is retried", func(t *testing.T) {
		oldFailure := &TestResult{
			NodeId:    "node-1",
			Status:    TestFailed,
			Retryable: true,
			StartedAt: time.Now().Add(-time.Duration(apiconfig.AutoTestRetryBackoffSeconds+1) * time.Second),
		}
		s.tester.recordResult("node-1", 0, oldFailure)

		s.maybeAutoTest("node-1")

		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if got := s.tester.LastResult("node-1"); got != nil && got != oldFailure && got.Status == TestSuccess {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("old retryable failure was not retried; last result: %+v", s.tester.LastResult("node-1"))
	})
}

// setNodeStatuses forces a registered node's intended/current status so a test
// can place it in the state the router accepts (or rejects). It first waits for
// the broker's startup status scan to land, otherwise that scan's queued
// STOPPED update would overwrite what we just set.
func setNodeStatuses(t *testing.T, s *Server, nodeId string, intended, current types.HardwareNodeStatus) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		nodes, err := s.nodeBroker.GetNodes()
		if err == nil {
			for _, n := range nodes {
				if n.Node.Id == nodeId && n.State.CurrentStatus != types.HardwareNodeStatus_UNKNOWN {
					goto settled
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
settled:
	if !s.nodeBroker.SetNodeStatusesForTest(nodeId, intended, current) {
		t.Fatalf("node %s not registered with the broker", nodeId)
	}
}

// stubActivity is a participantActivity whose answers the test controls.
type stubActivity struct {
	active bool
	known  bool
}

func (s stubActivity) IsActive() bool { return s.active }
func (s stubActivity) IsKnown() bool  { return s.known }

// farFromPoC pushes the phase tracker to a state with hours of slack, so the
// schedule gate is open.
func farFromPoC(t *testing.T, s *Server) {
	t.Helper()
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 1000000},
		&types.EpochParams{},
		true,
		nil,
	)
}

// TestAutoTestSkippedForActiveParticipant guards the gate that keeps auto-test
// an onboarding-only aid. A participant already in the active set has nodes
// doing (or about to do) real work; self-initiated stops outside the broker's
// state machine must never happen there, no matter how much slack there is
// before PoC. Operators can still test explicitly.
func TestAutoTestSkippedForActiveParticipant(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")
	farFromPoC(t, s)
	s.activityTracker = stubActivity{active: true, known: true}

	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())
	s.maybeAutoTest("node-1")
	time.Sleep(200 * time.Millisecond)

	assert.Nil(t, s.tester.LastResult("node-1"),
		"an active participant's node must never be auto-tested")

	// Same node, same schedule, inactive participant: now it is tested.
	s.activityTracker = stubActivity{active: false, known: true}
	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if s.tester.LastResult("node-1") != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("inactive participant's node was not auto-tested")
}

// TestOnEpochStateTestsOneNodeAtATime covers the global concurrency limit on the
// per-block sweep: a test takes its node out of service, so with several idle
// nodes one block must not stop them all. The governance query is made to block
// so the started test stays in flight while we observe.
func TestOnEpochStateTestsOneNodeAtATime(t *testing.T) {
	s, cm, factory := setupTestServer(t)
	ids := []string{"node-1", "node-2", "node-3"}
	for _, id := range ids {
		registerTestNode(t, s, cm, id)
	}
	farFromPoC(t, s)

	release := make(chan struct{})
	s.tester = NewMLNodeTester(cm, factory, stubGovModels{
		resp:  &types.QueryModelsAllResponse{Model: []types.Model{{Id: "test-model"}}},
		block: release,
	})

	countRunning := func() int {
		n := 0
		for _, id := range ids {
			if s.tester.IsRunning(id) {
				n++
			}
		}
		return n
	}

	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())
	deadline := time.Now().Add(2 * time.Second)
	for countRunning() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, 1, countRunning(), "one block must start exactly one node test")

	// More blocks while that test is still in flight must not start another.
	for i := 0; i < 3; i++ {
		s.OnEpochState(s.phaseTracker.GetCurrentEpochState())
	}
	assert.Equal(t, 1, countRunning(), "no second test may start while one is in flight")

	close(release)
	deadline = time.Now().Add(3 * time.Second)
	for countRunning() > 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	tested := 0
	for _, id := range ids {
		if s.tester.LastResult(id) != nil {
			tested++
		}
	}
	assert.Equal(t, 1, tested, "only the one node that was tested has a result")

	// Later blocks pick up the remaining nodes, one per block.
	for i := 0; i < 10 && tested < len(ids); i++ {
		s.OnEpochState(s.phaseTracker.GetCurrentEpochState())
		deadline = time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			n := 0
			for _, id := range ids {
				if s.tester.LastResult(id) != nil {
					n++
				}
			}
			if n > tested && countRunning() == 0 {
				tested = n
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	assert.Equal(t, len(ids), tested, "later blocks should cover the remaining nodes")
}

// TestMaybeAutoTest_BackoffMeasuredFromCompletion guards that a long-running
// failed test does not become instantly eligible for a retry: the backoff runs
// from when the attempt ended, not when it started.
func TestMaybeAutoTest_BackoffMeasuredFromCompletion(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")
	farFromPoC(t, s)

	backoff := time.Duration(apiconfig.AutoTestRetryBackoffSeconds) * time.Second
	// Started long ago (older than the backoff) but only just finished — a test
	// can itself run for minutes.
	longRun := &TestResult{
		NodeId:     "node-1",
		Status:     TestFailed,
		Retryable:  true,
		StartedAt:  time.Now().Add(-2 * backoff),
		FinishedAt: time.Now(),
	}
	s.tester.recordResult("node-1", 0, longRun)

	s.maybeAutoTest("node-1")
	time.Sleep(150 * time.Millisecond)

	assert.Same(t, longRun, s.tester.LastResult("node-1"),
		"backoff must be measured from FinishedAt, not StartedAt")
}

func TestOnEpochStateTestsRegisteredNode(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")

	// A synced epoch update with enough time before PoC: a node loaded from
	// config and registered at startup must get auto-tested when the
	// dispatcher reports fresh epoch state.
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)

	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if result := s.tester.LastResult("node-1"); result != nil {
			assert.Equal(t, TestSuccess, result.Status)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("registered node was not auto-tested on epoch state")
}

// TestOnEpochStateSkipsUnsyncedAndUnregistered guards the two races the old
// polling scheduler had: it must not test anything before the chain is synced,
// and it must not test a node that is in config but not registered with the
// broker (a node whose startup registration failed).
func TestOnEpochStateSkipsUnsyncedAndUnregistered(t *testing.T) {
	s, cm, _ := setupTestServer(t)

	// A node present in config but never loaded into the broker.
	nodes := append(cm.GetNodes(), apiconfig.InferenceNodeConfig{
		Id:     "config-only",
		Host:   "localhost",
		Models: map[string]apiconfig.ModelConfig{"test-model": {Args: []string{}}},
	})
	assert.NoError(t, cm.SetNodes(nodes))

	// Not synced yet: nothing should be tested.
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		false,
		nil,
	)
	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())

	// Synced now, but the config-only node is not registered with the broker,
	// so it must still be skipped.
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 2, Hash: "h2"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)
	s.OnEpochState(s.phaseTracker.GetCurrentEpochState())

	time.Sleep(200 * time.Millisecond)
	assert.Nil(t, s.tester.LastResult("config-only"),
		"a config-only node not registered with the broker must not be auto-tested")
}

// TestLogOnboardingStatus covers the onboarding status logger's branching:
// no-op (returns prevActive) when no node is configured, and reports
// inactive (false) while a configured node's participant isn't active.
func TestLogOnboardingStatus(t *testing.T) {
	s, cm, _ := setupTestServer(t)

	// No nodes configured yet -> no-op, prevActive passes through.
	assert.True(t, s.logOnboardingStatus(true))
	assert.False(t, s.logOnboardingStatus(false))

	// With a configured node and an inactive participant (activityTracker
	// nil in the test server), it reports inactive and logs "waiting".
	registerTestNode(t, s, cm, "node-1")
	assert.False(t, s.logOnboardingStatus(false))
	assert.False(t, s.logOnboardingStatus(true))
}

// TestComputeOnboarding_BrokerFailedIsNotTestFailed guards the fix that
// TEST_FAILED comes only from the MLnode test, not the broker's
// operational FAILED status. An inactive node the broker marked FAILED
// ("no epoch models") with no recorded test result must NOT be shown as
// TEST_FAILED — that is normal onboarding.
func TestComputeOnboarding_BrokerFailedIsNotTestFailed(t *testing.T) {
	s, _, _ := setupTestServer(t)
	n := broker.NodeResponse{
		Node: broker.Node{Id: "inactive-node"},
		State: broker.NodeState{
			FailureReason: "No epoch models available for this node",
			CurrentStatus: types.HardwareNodeStatus_FAILED,
		},
	}
	ob := s.computeOnboarding(n)
	if assert.NotNil(t, ob) {
		assert.NotEqual(t, MLNodeState_TEST_FAILED, ob.MLNodeState,
			"inactive node with broker FAILED must not surface as TEST_FAILED")
	}
}

// TestComputeOnboarding_ActiveAssignedNodeUsesParticipantMessage checks that
// an assigned active node never gets the inactive waiting copy ("safe to be
// offline"), even when the next PoC is far away.
func TestComputeOnboarding_ActiveAssignedNodeUsesParticipantMessage(t *testing.T) {
	s, _, _ := setupTestServer(t)
	s.phaseTracker.Update(
		chainphase.BlockInfo{Height: 1, Hash: "h"},
		&types.Epoch{Index: 100, PocStartBlockHeight: 10000},
		&types.EpochParams{},
		true,
		nil,
	)
	n := broker.NodeResponse{
		Node: broker.Node{Id: "active-node"},
		State: broker.NodeState{
			// A populated epoch-MLnodes map marks the participant active.
			EpochMLNodes: map[string]types.MLNodeInfo{"m": {}},
		},
	}
	ob := s.computeOnboarding(n)
	if assert.NotNil(t, ob) {
		assert.Equal(t, string(ParticipantState_ACTIVE_PARTICIPATING), ob.ParticipantState)
		assert.Equal(t, "Participant is in active set and participating", ob.UserMessage)
		assert.NotContains(t, ob.UserMessage, "safe to be offline")
		assert.NotContains(t, ob.UserMessage, "not yet validated")
	}
}

// TestGetNodesOnboarding verifies the GET /nodes response shape (the
// existing node fields plus the new optional onboarding envelope) and
// that a failed manual test surfaces as TEST_FAILED — the gap #1 wiring
// from the tester back into the onboarding status.
func TestGetNodesOnboarding(t *testing.T) {
	s, cm, factory := setupTestServer(t)
	registerTestNode(t, s, cm, "node-1")

	t.Run("response preserves node shape and adds onboarding", func(t *testing.T) {
		nodes := getNodeViews(t, s)
		assert.Len(t, nodes, 1)
		assert.Equal(t, "node-1", nodes[0].Node.Id) // existing field still there
		assert.NotEmpty(t, nodes[0].State)          // existing state block still there
		assert.NotNil(t, nodes[0].Onboarding)       // new optional envelope present
	})

	t.Run("failed manual test surfaces as TEST_FAILED", func(t *testing.T) {
		// Make the MLnode fail, then run a manual test so the tester
		// records a failed result for node-1.
		mc := factory.CreateClient(testNodePoCURL, "").(*mlnodeclient.MockClient)
		mc.InferenceUpError = errors.New("model not found")
		_, err := s.tester.Run(context.Background(), "node-1")
		assert.NoError(t, err)

		nodes := getNodeViews(t, s)
		assert.Len(t, nodes, 1)
		if assert.NotNil(t, nodes[0].Onboarding) {
			assert.Equal(t, string(MLNodeState_TEST_FAILED), nodes[0].Onboarding.MLNodeState)
			assert.Contains(t, nodes[0].Onboarding.UserMessage, "test-model")
		}
	})
}
