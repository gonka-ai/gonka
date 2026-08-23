//go:build testenvci

package citest

import (
	"strconv"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
)

// TestEscrowLongPollWarmWithoutInferenceNode encodes v4-deploy-test-plan §6.1–§6.3.
// A DAPI escrow-created host event is long-polled by v4 devshardd, which prefetches
// escrow metadata into escrow_cache; a first gateway chat for that escrow then succeeds
// even with the live chain escrow-query path faulted — proving the session bound from
// the warm cache rather than a request-time chain fetch.
func TestEscrowLongPollWarmWithoutInferenceNode(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootStack(t, "citest-escrow-longpoll-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "mock-dapi", "mock-chain")
		}
	})
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health via router", stack)

	mockDapi := harness.MockDAPIFromEndpoints(eps)
	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)
	escrowNum, err := strconv.ParseUint(escrowID, 10, 64)
	require.NoError(t, err)

	// §6.1 step 1: baseline — no warm cache row before the long-poll event.
	harness.Step(t, "baseline: devshard_escrow_cache has no row for escrow %s", escrowID)
	baseCount, err := stack.PostgresEscrowCacheCount(cfg, escrowID)
	require.NoError(t, err)
	require.Zero(t, baseCount, "escrow_cache should be empty before host-event warm (no chat yet)")

	// §6.1 steps 2-3: emit an escrow-created host event → devshardd long-poll warm →
	// escrow_cache row appears BEFORE any chat to that escrow (timing check).
	harness.Step(t, "emit escrow-created host event for escrow %s", escrowID)
	harness.TriggerEscrowCreatedHostEvent(t, client, mockDapi.HTTP, harness.EscrowCreatedHostEvent{
		EscrowID: escrowNum,
	})

	harness.Step(t, "poll devshard_escrow_cache until the warm row appears (before chat)")
	warmed := harness.AssertEventually(t, 60*time.Second, 2*time.Second, func() bool {
		n, qerr := stack.PostgresEscrowCacheCount(cfg, escrowID)
		return qerr == nil && n >= 1
	})
	require.True(t, warmed, "escrow long-poll did not populate escrow_cache for escrow %s", escrowID)

	// §6.2: fault the live chain escrow query so a request-time escrow fetch cannot succeed.
	harness.Step(t, "fault mock-chain DevshardEscrow query (request-time escrow fetch down)")
	harness.SetEscrowQueryFault(t, client, mockDapi.HTTP, true)
	t.Cleanup(func() { harness.SetEscrowQueryFault(t, client, mockDapi.HTTP, false) })

	// §6.3: first inference (new session) for the escrow still succeeds, served from the
	// warm cache — devshardd binds without the live escrow fetch.
	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey
	harness.Step(t, "gateway chat for escrow %s with escrow query faulted (must bind from warm cache)", escrowID)
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "longpoll-warm without live escrow fetch"}},
		MaxTokens: 32,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)
	require.Equal(t, "assistant", resp.Choices[0].Message.Role)
}
