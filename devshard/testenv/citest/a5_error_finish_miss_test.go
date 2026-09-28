//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
	"devshard/testenv/mockopenai"

	"github.com/stretchr/testify/require"
)

// TestA5_ErrorFinishMiss verifies a streamed OpenAI error envelope (HTTP 200 SSE)
// is accounted as MsgErrorMiss: the client still sees today's
// hostApplicationError, the executor takes a Missed, Cost is unchanged, the
// client is refunded, and no validation job runs. Settlement copies HostStats.
func TestA5_ErrorFinishMiss(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootErrorMissAdversarialStack(t, "citest-a5-*")
	client := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	mockOpenAI := eps.MockOpenAIHTTP
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, mockOpenAI)
		if t.Failed() {
			if out, err := stack.ComposeLogsTail(400, "devshardctl"); err == nil {
				t.Logf("citest: gateway logs:\n%s", out)
			}
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai", "versiond-0", "versiond-1", "versiond-2", "versiond-3")
		}
	})

	harness.PatchGatewayRedundancySpeedPolicy(t, client, eps.GatewayHTTP, "legacy")

	feePerNonce := harness.GetGatewayFeePerNonce(t, client, eps.GatewayHTTP, adminKey)
	require.NotZero(t, feePerNonce, "session FeePerNonce must be set")
	before := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
	beforeMissed := totalHostMissed(before.HostStats)

	on := true
	harness.PatchMockOpenAIFault(t, client, mockOpenAI, mockopenai.FaultPatch{StreamErrorEnvelope: &on})

	req := harness.ChatCompletionRequest{
		Model: config.PrimaryModelID(cfg),
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest a5 error-finish-miss unique prompt"},
		},
		MaxTokens: 16,
	}
	harness.Step(t, "non-stream chat with mock-openai stream_error_envelope should serve hostApplicationError")
	status, body := harness.PostGatewayChatFailure(t, client, eps.GatewayHTTP, adminKey, req)
	require.Equal(t, http.StatusInternalServerError, status, body)
	require.Contains(t, body, "InternalServerError")
	require.Contains(t, body, "EngineCore encountered an issue")

	harness.Step(t, "wait for ERROR miss to land: StatusTimedOut, Missed++, host Cost unwound")
	var (
		timedOut harness.GatewayDebugInference
		after    harness.GatewayLedgerSnapshot
	)
	ok := harness.AssertEventually(t, 2*time.Minute, 500*time.Millisecond, func() bool {
		after = harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
		if totalHostMissed(after.HostStats) < beforeMissed+1 {
			return false
		}
		for _, inf := range harness.GetGatewayDebugInferences(t, client, eps.GatewayHTTP, adminKey) {
			if inf.Status == "timed_out" {
				timedOut = inf
				return true
			}
		}
		return false
	})
	require.True(t, ok, "ERROR miss did not land (missed %d→? timed_out missing)", beforeMissed)

	// Sequencer FeePerNonce is charged on every applied nonce (receipt, Finish+
	// ErrorMiss, height-sync heartbeats). Error-miss refunds ActualCost, so the
	// balance drop in this window must equal fees for the new diffs — not a
	// leftover reservation.
	require.Greater(t, after.LatestNonce, before.LatestNonce)
	charged := feePerNonce * (after.LatestNonce - before.LatestNonce)
	require.GreaterOrEqual(t, before.Balance, charged,
		"balance underflow: before=%d fees=%d (nonce %d→%d fee=%d)",
		before.Balance, charged, before.LatestNonce, after.LatestNonce, feePerNonce)
	require.Equal(t, before.Balance-charged, after.Balance,
		"client reservation must be refunded (before=%d after=%d nonce %d→%d fee=%d); drop must equal FeePerNonce × new diffs",
		before.Balance, after.Balance, before.LatestNonce, after.LatestNonce, feePerNonce)
	slotKey := fmt.Sprintf("%d", timedOut.ExecutorSlot)
	beforeHS := before.HostStats[slotKey]
	afterHS := after.HostStats[slotKey]
	require.Equal(t, beforeHS.Missed+1, afterHS.Missed, "executor slot %s must take the miss (settlement copies HostStats.Missed)", slotKey)
	require.Equal(t, beforeHS.Cost, afterHS.Cost, "executor HostStats.Cost must be unchanged after ERROR unwind")
	require.Equal(t, uint32(0), timedOut.VotesValid, "timed-out inferences are not sampled for validation")
	require.Equal(t, uint32(0), timedOut.VotesInvalid)
	require.Equal(t, beforeHS.CompletedValidations, afterHS.CompletedValidations, "no validation job should complete against a miss")
}

func totalHostMissed(stats map[string]harness.GatewayHostStats) uint32 {
	var n uint32
	for _, hs := range stats {
		n += hs.Missed
	}
	return n
}
