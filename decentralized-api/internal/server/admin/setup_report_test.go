package admin

import (
	"decentralized-api/apiconfig"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Note: Unit tests for individual check functions (cold key, warm key, permissions,
// consensus key, participant stats, MLNode, block sync) would require extensive mocking
// of the cosmos client, keyring, RPC client, and HTTP client.
// The actual functionality is tested via integration tests and real usage.
// We test only the components that don't require mocking below.

// Test Summary Generation

func TestGenerateSummary_AllPass(t *testing.T) {
    s, _, _, _ := setupTestServer(t)

	report := &SetupReport{
		Checks: []Check{
			{ID: "check1", Status: PASS, Message: "Check 1 passed"},
			{ID: "check2", Status: PASS, Message: "Check 2 passed"},
			{ID: "check3", Status: PASS, Message: "Check 3 passed"},
		},
	}

	s.generateSummary(report)

	assert.Equal(t, PASS, report.OverallStatus)
	assert.Equal(t, 3, report.Summary.TotalChecks)
	assert.Equal(t, 3, report.Summary.PassedChecks)
	assert.Equal(t, 0, report.Summary.FailedChecks)
	assert.Equal(t, 0, report.Summary.UnavailableChecks)
	assert.Empty(t, report.Summary.Issues)
	assert.Empty(t, report.Summary.Recommendations)
}

func TestGenerateSummary_WithFailures(t *testing.T) {
    s, _, _, _ := setupTestServer(t)

	report := &SetupReport{
		Checks: []Check{
			{ID: "check1", Status: PASS, Message: "Check 1 passed"},
			{ID: "permissions_granted", Status: FAIL, Message: "Missing permissions"},
			{ID: "check3", Status: PASS, Message: "Check 3 passed"},
		},
	}

	s.generateSummary(report)

	assert.Equal(t, FAIL, report.OverallStatus)
	assert.Equal(t, 3, report.Summary.TotalChecks)
	assert.Equal(t, 2, report.Summary.PassedChecks)
	assert.Equal(t, 1, report.Summary.FailedChecks)
	assert.Equal(t, 0, report.Summary.UnavailableChecks)
	assert.Len(t, report.Summary.Issues, 1)
	assert.Contains(t, report.Summary.Issues[0], "Missing permissions")
	assert.Len(t, report.Summary.Recommendations, 1)
	assert.Contains(t, report.Summary.Recommendations[0], "authz grant")
}

func TestGenerateSummary_WithUnavailable(t *testing.T) {
    s, _, _, _ := setupTestServer(t)

	report := &SetupReport{
		Checks: []Check{
			{ID: "check1", Status: PASS, Message: "Check 1 passed"},
			{ID: "check2", Status: UNAVAILABLE, Message: "Could not check"},
			{ID: "check3", Status: PASS, Message: "Check 3 passed"},
		},
	}

	s.generateSummary(report)

	assert.Equal(t, UNAVAILABLE, report.OverallStatus)
	assert.Equal(t, 3, report.Summary.TotalChecks)
	assert.Equal(t, 2, report.Summary.PassedChecks)
	assert.Equal(t, 0, report.Summary.FailedChecks)
	assert.Equal(t, 1, report.Summary.UnavailableChecks)
	assert.Len(t, report.Summary.Issues, 1)
	assert.Contains(t, report.Summary.Issues[0], "Check unavailable")
}

func TestGenerateSummary_MLNodeWithNoGPUs(t *testing.T) {
    s, _, _, _ := setupTestServer(t)

	report := &SetupReport{
		Checks: []Check{
			{
				ID:      "mlnode_node1",
				Status:  PASS,
				Message: "MLNode is healthy",
				Details: map[string]interface{}{
					"id":     "node1",
					"host":   "localhost",
					"models": []string{"model1"},
					"gpus":   []GPUDeviceInfo{}, // No GPUs
				},
			},
		},
	}

	s.generateSummary(report)

	assert.Equal(t, PASS, report.OverallStatus)
	assert.Len(t, report.Summary.Issues, 1)
	assert.Contains(t, report.Summary.Issues[0], "No GPUs detected")
	assert.Len(t, report.Summary.Recommendations, 1)
	assert.Contains(t, report.Summary.Recommendations[0], "GPU drivers")
}

func TestGenerateSummary_MLNodeWithUnavailableGPU(t *testing.T) {
    s, _, _, _ := setupTestServer(t)

	report := &SetupReport{
		Checks: []Check{
			{
				ID:      "mlnode_node1",
				Status:  PASS,
				Message: "MLNode is healthy",
				Details: map[string]interface{}{
					"id":     "node1",
					"host":   "localhost",
					"models": []string{"model1"},
					"gpus": []GPUDeviceInfo{
						{
							Index:     0,
							Name:      "NVIDIA A100",
							Available: false, // GPU not available
						},
					},
				},
			},
		},
	}

	s.generateSummary(report)

	assert.Equal(t, PASS, report.OverallStatus)
	assert.Len(t, report.Summary.Issues, 1)
	assert.Contains(t, report.Summary.Issues[0], "GPU 0")
	assert.Contains(t, report.Summary.Issues[0], "not available")
}

// Caching and integration tests omitted due to complex mocking requirements.
// The caching logic is tested in production usage.

// Test Helper Functions

func TestURLHelperFunctions(t *testing.T) {
	node := apiconfig.InferenceNodeConfig{
		Host:       "localhost",
		PoCPort:    8080,
		PoCSegment: "/api/v1",
	}

	t.Run("formatURL", func(t *testing.T) {
		url := formatURL("localhost", 8080, "/api/v1")
		assert.Equal(t, "http://localhost:8080/api/v1", url)
	})

	t.Run("formatURLWithVersion", func(t *testing.T) {
		url := formatURLWithVersion("localhost", 8080, "v2", "/api/v1")
		assert.Equal(t, "http://localhost:8080/v2/api/v1", url)
	})

	t.Run("getPoCUrl", func(t *testing.T) {
		url := getPoCUrl(node)
		assert.Equal(t, "http://localhost:8080/api/v1", url)
	})

	t.Run("getPoCUrlVersioned", func(t *testing.T) {
		url := getPoCUrlVersioned(node, "v2")
		assert.Equal(t, "http://localhost:8080/v2/api/v1", url)
	})

	t.Run("getPoCUrlWithVersion empty version", func(t *testing.T) {
		url := getPoCUrlWithVersion(node, "")
		assert.Equal(t, "http://localhost:8080/api/v1", url)
	})

	t.Run("getPoCUrlWithVersion with version", func(t *testing.T) {
		url := getPoCUrlWithVersion(node, "v2")
		assert.Equal(t, "http://localhost:8080/v2/api/v1", url)
	})
}

func TestBuildRecommendationMap(t *testing.T) {
	recMap := buildRecommendationMap()

	// Verify key recommendations exist
	assert.Contains(t, recMap, "cold_key_configured")
	assert.Contains(t, recMap, "permissions_granted")
	assert.Contains(t, recMap, "consensus_key_match")
	assert.Contains(t, recMap, "block_sync")

	// Verify recommendations are actionable
	assert.Contains(t, recMap["permissions_granted"], "authz grant")
	assert.Contains(t, recMap["cold_key_configured"], "ACCOUNT_PUBKEY")
}

// Test Check Status Constants

func TestCheckStatusConstants(t *testing.T) {
	assert.Equal(t, CheckStatus("PASS"), PASS)
	assert.Equal(t, CheckStatus("FAIL"), FAIL)
	assert.Equal(t, CheckStatus("UNAVAILABLE"), UNAVAILABLE)
}

// Edge case tests omitted due to complex mocking requirements.
