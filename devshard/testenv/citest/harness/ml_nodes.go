package harness

import (
	"net/http"
	"testing"

	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// MLNodeIDs returns the mock-openai pool ids from cfg.
func MLNodeIDs(cfg *config.File) []string {
	if cfg == nil {
		return []string{config.MLNodeID(0)}
	}
	return cfg.MLNodeIDs()
}

// MockOpenAIHTTPForNode resolves the host-published /testenv URL for a pool member.
func MockOpenAIHTTPForNode(t *testing.T, stack *Stack, cfg *config.File, nodeID string) string {
	t.Helper()
	require.NotNil(t, stack)
	require.NotNil(t, cfg)
	require.NotEmpty(t, nodeID)
	return "http://" + stack.composePublishedAddr(t, nodeID, cfg.MockOpenAI.HTTPPort)
}

// PatchMockOpenAIFaultForNode posts /testenv/fault to one pool member.
func PatchMockOpenAIFaultForNode(t *testing.T, client *http.Client, stack *Stack, cfg *config.File, nodeID string, patch mockopenai.FaultPatch) {
	t.Helper()
	PatchMockOpenAIFault(t, client, MockOpenAIHTTPForNode(t, stack, cfg, nodeID), patch)
}

// ResetAllMockOpenAIFaults clears fault knobs on every pool member.
func ResetAllMockOpenAIFaults(t *testing.T, client *http.Client, stack *Stack, cfg *config.File) {
	t.Helper()
	for _, id := range MLNodeIDs(cfg) {
		ResetMockOpenAIFault(t, client, MockOpenAIHTTPForNode(t, stack, cfg, id))
	}
}

// StopMLNode stops a mock-openai compose service (connection-refused fault).
func StopMLNode(t *testing.T, stack *Stack, nodeID string) {
	t.Helper()
	stack.StopService(t, nodeID)
}

// StartMLNode starts a previously stopped mock-openai compose service.
func StartMLNode(t *testing.T, stack *Stack, nodeID string) {
	t.Helper()
	stack.StartService(t, nodeID)
}
