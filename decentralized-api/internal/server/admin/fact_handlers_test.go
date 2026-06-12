package admin

import (
	"decentralized-api/apiconfig"
	"decentralized-api/chainphase"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/assert"
)

// addConfigOnlyNode adds a node to the config manager without registering it
// with the broker — the fact endpoints read configuration and tester state,
// not broker state.
func addConfigOnlyNode(t *testing.T, cm *apiconfig.ConfigManager, cfg apiconfig.InferenceNodeConfig) {
	t.Helper()
	assert.NoError(t, cm.SetNodes(append(cm.GetNodes(), cfg)))
}

func TestGetNodeLaunchPlan(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	addConfigOnlyNode(t, cm, apiconfig.InferenceNodeConfig{
		Id:   "plan-node",
		Host: "localhost",
		Models: map[string]apiconfig.ModelConfig{
			"test-model":   {Args: []string{"--local-arg"}},
			"legacy-model": {}, // not in governance → reported as skipped
		},
	})

	t.Run("returns merged plan and skipped models", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/plan-node/launch-plan", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)

		assert.Equal(t, http.StatusOK, rec.Code)
		var report LaunchPlanReport
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &report))
		assert.Equal(t, "plan-node", report.NodeId)
		if assert.Len(t, report.Plans, 1) {
			assert.Equal(t, "test-model", report.Plans[0].ModelID)
			assert.Contains(t, report.Plans[0].Args, "--local-arg")
		}
		assert.Equal(t, []string{"legacy-model"}, report.SkippedModels)
		assert.Empty(t, report.UnsupportedModels)
	})

	t.Run("unknown node is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/no-such-node/launch-plan", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestGetNodeTestResult(t *testing.T) {
	s, cm, _ := setupTestServer(t)
	addConfigOnlyNode(t, cm, apiconfig.InferenceNodeConfig{
		Id:     "tested-node",
		Host:   "localhost",
		Models: map[string]apiconfig.ModelConfig{"test-model": {}},
	})

	getResult := func() (int, struct {
		NodeId  string      `json:"node_id"`
		Running bool        `json:"running"`
		Last    *TestResult `json:"last"`
	}) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/tested-node/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		var resp struct {
			NodeId  string      `json:"node_id"`
			Running bool        `json:"running"`
			Last    *TestResult `json:"last"`
		}
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return rec.Code, resp
	}

	t.Run("no result recorded yet", func(t *testing.T) {
		code, resp := getResult()
		assert.Equal(t, http.StatusOK, code)
		assert.Equal(t, "tested-node", resp.NodeId)
		assert.False(t, resp.Running)
		assert.Nil(t, resp.Last)
	})

	t.Run("returns the raw last result", func(t *testing.T) {
		s.tester.recordResult("tested-node", 0, &TestResult{
			NodeId:    "tested-node",
			Status:    TestSuccess,
			LoadMs:    map[string]int64{"test-model": 42},
			StartedAt: time.Now(),
		})
		code, resp := getResult()
		assert.Equal(t, http.StatusOK, code)
		if assert.NotNil(t, resp.Last) {
			assert.Equal(t, TestSuccess, resp.Last.Status)
			assert.Equal(t, int64(42), resp.Last.LoadMs["test-model"])
		}
	})

	t.Run("unknown node is 404", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/nodes/no-such-node/test", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})
}

func TestGetPoCTiming(t *testing.T) {
	s, _, _ := setupTestServer(t)

	getTiming := func() (int, struct {
		Available bool        `json:"available"`
		Timing    *TimingInfo `json:"timing"`
	}) {
		req := httptest.NewRequest(http.MethodGet, "/admin/v1/poc/timing", nil)
		rec := httptest.NewRecorder()
		s.e.ServeHTTP(rec, req)
		var resp struct {
			Available bool        `json:"available"`
			Timing    *TimingInfo `json:"timing"`
		}
		assert.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
		return rec.Code, resp
	}

	t.Run("available once synced", func(t *testing.T) {
		// setupTestServer leaves the tracker synced at height 1, PoC at 100.
		code, resp := getTiming()
		assert.Equal(t, http.StatusOK, code)
		assert.True(t, resp.Available)
		if assert.NotNil(t, resp.Timing) {
			assert.Greater(t, resp.Timing.BlocksUntilNextPoC, int64(0))
		}
	})

	t.Run("unavailable while not synced", func(t *testing.T) {
		s.phaseTracker.Update(
			chainphase.BlockInfo{Height: 2, Hash: "hash-2"},
			&types.Epoch{Index: 100, PocStartBlockHeight: 100},
			&types.EpochParams{},
			false,
			nil,
		)
		code, resp := getTiming()
		assert.Equal(t, http.StatusOK, code)
		assert.False(t, resp.Available)
		assert.Nil(t, resp.Timing)
	})
}
