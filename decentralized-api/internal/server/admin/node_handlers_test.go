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
	nodeConfig := apiconfig.InferenceNodeConfig{
		Id:               id,
		Host:             "localhost",
		InferencePort:    8080,
		InferenceSegment: "/api/v1",
		PoCPort:          8081,
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

// TestShouldAutoTest covers the auto-test gate: only fire when the next
// PoC is strictly more than the configured lead time away.
func TestShouldAutoTest(t *testing.T) {
	assert.False(t, shouldAutoTest(apiconfig.AutoTestMinSecondsBeforePoC), "exactly the threshold is not > threshold")
	assert.True(t, shouldAutoTest(apiconfig.AutoTestMinSecondsBeforePoC+1))
	assert.False(t, shouldAutoTest(0))
	assert.False(t, shouldAutoTest(SecondsUntilPoCUnknown), "unknown schedule must not auto-test")
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

// TestComputeOnboarding_ActiveIsValidated checks that an active
// participant's node is treated as validated — it must not be shown as
// "not yet validated", since it is already serving in the epoch.
func TestComputeOnboarding_ActiveIsValidated(t *testing.T) {
	s, _, _ := setupTestServer(t)
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
