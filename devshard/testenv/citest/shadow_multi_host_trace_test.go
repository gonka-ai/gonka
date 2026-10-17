//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// multiHostChatRounds bounds how many chats this test drives while looking for
// a request that opened attempts on two hosts. One chat is not enough: the
// picker does not always make the shadow-quarantined host primary, and the
// first chats after the fault drive can still fail while the healthy host works
// off its probation strikes.
const multiHostChatRounds = 8

// TestShadowHostMultiAttemptSameTrace proves that when shadow quarantine forces
// the gateway to open attempts on more than one host, those attempts stay under
// the one client request_id and one parent trace_id — including the mock-dapi
// acquires each attempt triggers.
//
// Shadow (not probe) quarantine is the mode under test: probe stops sending
// real traffic, which is exactly the shape this test must not measure.
func TestShadowHostMultiAttemptSameTrace(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	// HA-solo, not the default 2-host stack: versiond-0/1 are one on-chain
	// participant, so a 2-host stack can never produce attempts on two distinct
	// hosts — the picker reports "tried every host in escrow" after one.
	stack, cfg, eps, obs := harness.BootObservabilityStackHASolo(t, "citest-shadow-multi-host-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack,
				"devshardctl", "versiond-0", "versiond-1", "versiond-2", "mock-dapi", "mock-openai-0", "alloy", "loki", "tempo")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	// Fast redundancy so a secondary attempt starts without waiting out the
	// production receipt timeout.
	harness.PatchAdversarialFastRedundancy(t, client, eps.GatewayHTTP)
	harness.PatchAdversarialFastTimeouts(t, client, eps.MockDapiHTTP)

	model := config.PrimaryModelID(cfg)
	devshardID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)

	// Exactly one shadow host: with every participant quarantined no host may
	// win, and the client request fails before any fan-out is observable.
	shadowKey := harness.ForceOneShadowQuarantine(t, client,
		eps.GatewayHTTP, eps.MockOpenAIHTTP, devshardID, model, 2*time.Minute)
	views := harness.GetParticipantThrottles(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, devshardID)
	harness.RequireShadowQuarantineMode(t, views, shadowKey)
	t.Logf("citest: shadow-quarantined participant=%s", shadowKey)

	var (
		chosenRequestID string
		chosenTraceID   string
		attempts        int
		participants    []string
	)
	for round := 1; round <= multiHostChatRounds; round++ {
		needle := fmt.Sprintf("citest-shadow-multi-host-%d-%d", time.Now().UnixNano(), round)
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: needle},
			},
			MaxTokens: 32,
		}
		harness.Step(t, "round %d: chat with a shadow-quarantined host in the pool", round)
		status, hdr, content := harness.PostGatewayChatSoftEx(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, req)
		requestID := hdr.Get("X-Request-Id")
		if status != http.StatusOK || content == "" {
			// A round the gateway could not serve says nothing about attempt
			// fan-out; the next round gets a fresh request id.
			t.Logf("citest: round %d unusable (status=%d request_id=%s), retrying", round, status, requestID)
			continue
		}
		harness.RequireMockOpenAIContent(t, content)
		require.NotEmpty(t, requestID, "gateway must return X-Request-Id")

		ids, found := harness.TryTraceIDsForRequest(obs, "devshardctl", requestID, 60*time.Second)
		if !found || len(ids) != 1 {
			t.Logf("citest: round %d has trace ids %v for request_id=%s, retrying", round, ids, requestID)
			continue
		}
		traceID := ids[0]

		gotAttempts, gotParticipants, ok := harness.TryWaitMultiHostAttempts(obs, traceID, 2, 45*time.Second)
		t.Logf("citest: round %d request_id=%s trace_id=%s attempts=%d hosts=%v",
			round, requestID, traceID, gotAttempts, gotParticipants)
		if ok {
			chosenRequestID, chosenTraceID = requestID, traceID
			attempts, participants = gotAttempts, gotParticipants
			break
		}
	}
	require.NotEmpty(t, chosenTraceID,
		"no chat opened attempts on >=2 hosts within %d rounds; shadow quarantine should force escalation",
		multiHostChatRounds)

	// Several attempts, on distinct hosts, all on one trace. Re-running the
	// single-trace check here states the negative case explicitly: the extra
	// attempts must not have minted a second trace for this request.
	require.Equal(t, chosenTraceID,
		harness.RequireSingleTraceForRequest(t, obs, "devshardctl", chosenRequestID, time.Minute))
	require.GreaterOrEqual(t, attempts, 2, "expected >=2 gateway.attempt spans on trace %s", chosenTraceID)
	require.GreaterOrEqual(t, len(participants), 2,
		"attempts must target distinct hosts, got %v", participants)

	// All three services are still on the one trace.
	harness.RequireLogsForTrace(t, obs, chosenTraceID, []string{
		"devshardctl",
		"versiond.*",
		"mock-dapi",
	}, 2*time.Minute)

	// Every node-manager acquire on this trace belongs to this request.
	acquires := harness.RequireStagesForTrace(t, obs, chosenTraceID, "mock-dapi",
		[]string{harness.StageMLNodeAcquire}, 2*time.Minute)
	require.NotEmpty(t, acquires)
	for _, entry := range acquires {
		require.Equal(t, chosenTraceID, entry.Str("trace_id"))
		require.Equal(t, chosenRequestID, entry.Str("request_id"),
			"an acquire on this trace carried a different request_id: %v", entry.Fields)
	}

	// Attempts add span_ids, they do not mint a second request identity.
	harness.RequireRequestIDOnTrace(t, obs, chosenTraceID, chosenRequestID, []string{
		"devshardctl",
		"versiond.*",
		"mock-dapi",
	}, 2*time.Minute)
}
