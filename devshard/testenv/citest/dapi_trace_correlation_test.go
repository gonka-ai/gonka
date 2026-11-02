//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestTraceLogCorrelationGatewayHostDapi proves one user chat produces one
// trace_id and one request_id across all three hops: devshardctl (gateway),
// versiond/devshardd (host) and mock-dapi (node manager).
//
// TestTraceLogCorrelation already covers gateway + host. The node-selection hop
// is what this test adds, and it is the hop that used to drop the caller's
// context on the floor (AcquireMLNode took `_ context.Context`).
func TestTraceLogCorrelationGatewayHostDapi(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootObservabilityStack(t, "citest-dapi-correlation-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack,
				"devshardctl", "versiond-0", "mock-dapi", "alloy", "loki", "tempo")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	needle := fmt.Sprintf("citest-dapi-correlation-%d", time.Now().UnixNano())
	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: needle},
		},
		MaxTokens: 32,
	}
	harness.Step(t, "POST %s/v1/chat/completions (three-service correlation)", eps.GatewayHTTP)
	resp, hdr := harness.PostGatewayChatCompletionEx(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	requestID := hdr.Get("X-Request-Id")
	require.NotEmpty(t, requestID, "gateway must return X-Request-Id")
	t.Logf("citest: request_id=%s profile=%s", requestID, obs.Profile)

	// Start from the client's request id rather than any recent trace, so a
	// warm-up chat cannot satisfy the assertions below. Exactly one trace_id
	// for this request_id is also the negative case: an orphan Acquire that
	// started its own root trace would show up as a second id here.
	traceID := harness.RequireSingleTraceForRequest(t, obs, "devshardctl", requestID, 2*time.Minute)
	t.Logf("citest: shared trace_id=%s", traceID)

	// Logs: every hop wrote at least one line on that trace.
	harness.RequireLogsForTrace(t, obs, traceID, []string{
		"devshardctl",
		"versiond.*",
		"mock-dapi",
	}, 2*time.Minute)

	// …and those lines describe the same user request, not just the same trace.
	harness.RequireRequestIDOnTrace(t, obs, traceID, requestID, []string{
		"devshardctl",
		"versiond.*",
		"mock-dapi",
	}, 2*time.Minute)

	// The dapi hop logged node selection, with the node it handed back.
	acquires := harness.RequireStagesForTrace(t, obs, traceID, "mock-dapi",
		[]string{harness.StageMLNodeAcquire}, 2*time.Minute)
	require.NotEmpty(t, acquires)
	require.NotEmpty(t, acquires[0].Str("node_id"), "mlnode_acquire must name the node: %v", acquires[0].Fields)
	require.Equal(t, requestID, acquires[0].Str("request_id"))

	// Spans: same three services on one trace, and the acquire hop is visible
	// either as the host client span or the mock-dapi server span.
	harness.WaitTraceServices(t, obs, traceID,
		[]string{"devshardctl", "devshardd", "mock-dapi"}, 2*time.Minute)
	harness.WaitTraceSpanNames(t, obs, traceID,
		[]string{"gateway.request", "devshardd.request"}, 2*time.Minute)
	acquireSpan := harness.WaitTraceSpanNameAny(t, obs, traceID, []string{
		"devshardd.mlnode.acquire",
		"nodemanager.NodeManager/AcquireMLNode",
	}, 2*time.Minute)
	t.Logf("citest: acquire hop span=%s", acquireSpan)

	spans, ok := harness.TraceSpans(obs, traceID)
	require.True(t, ok, "load trace %s", traceID)
	require.NotEmpty(t, spans)
}
