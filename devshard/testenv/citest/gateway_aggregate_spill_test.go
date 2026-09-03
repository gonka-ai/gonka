//go:build testenvci

package citest

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestGatewayAggregateSpillRoundTrip forces the aggregate body buffer to spill
// under a tiny GATEWAY_AGGREGATE_MAX_MEMORY_BYTES, then checks a complete JSON
// fold, spill logging, and an empty spool directory afterward.
func TestGatewayAggregateSpillRoundTrip(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	// Mock pads content to max_tokens; each SSE chunk is large enough that a
	// few hundred tokens exceed a 1 KiB RAM tier and spill to disk.
	stack, cfg, eps := harness.BootStackWithAggregateByteLimits(t, "citest-aggregate-spill-*", 1024, 16<<20)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey

	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)

	req := harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest aggregate spill round-trip unique"},
		},
		MaxTokens: 256,
	}

	harness.Step(t, "forced aggregate (expect spill)")
	forced := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, req)
	require.Equal(t, 200, forced.Status, "body=%s", forced.Body)
	require.Contains(t, strings.ToLower(forced.ContentType), "application/json")
	var spilled harness.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(forced.Body, &spilled))
	require.NotEmpty(t, spilled.Choices)
	harness.RequireMockOpenAIContent(t, spilled.Choices[0].Message.Content)
	harness.RequireAggregateSpilledInGatewayLogs(t, stack)
	harness.RequireAggregateSpoolDirEmpty(t, stack)

	harness.Step(t, "unforced aggregate for content match")
	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, false)
	unforced := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, req)
	require.Equal(t, 200, unforced.Status, "body=%s", unforced.Body)
	var plain harness.ChatCompletionResponse
	require.NoError(t, json.Unmarshal(unforced.Body, &plain))
	require.Equal(t, spilled.Choices[0].Message.Content, plain.Choices[0].Message.Content)
}

// TestGatewayAggregateOversizeAborts checks that a response above
// GATEWAY_AGGREGATE_MAX_RESPONSE_BYTES yields a typed gateway error, never a
// 200 with a truncated or half-folded body.
func TestGatewayAggregateOversizeAborts(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStackWithAggregateByteLimits(t, "citest-aggregate-oversize-*", 2<<20, 512)
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-openai")
		}
	})
	model := config.PrimaryModelID(cfg)
	admin := harness.TestenvAdminAPIKey

	harness.PatchGatewayForceUpstreamStreaming(t, client, eps.GatewayHTTP, true)

	harness.Step(t, "non-stream request that must exceed the response byte cap")
	got := harness.PostGatewayChatHTTP(t, client, eps.GatewayHTTP, admin, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest aggregate oversize abort unique"},
		},
		MaxTokens: 256,
	})
	require.GreaterOrEqual(t, got.Status, 400, "expected typed error, got %d body=%s", got.Status, got.Body)
	require.NotEqual(t, 200, got.Status)
	require.NotContains(t, string(got.Body), "data: [DONE]")
	var errBody struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(got.Body, &errBody), "body=%s", got.Body)
	require.NotEmpty(t, errBody.Error.Message)
	require.True(t,
		strings.Contains(errBody.Error.Message, "aggregate response exceeds size limit") ||
			strings.Contains(errBody.Error.Message, "aggregate fold exceeds size limit"),
		"unexpected error message: %s", errBody.Error.Message)
}
