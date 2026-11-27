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

const (
	hostFailedAnswer    = "an upstream host failed to answer the request"
	gatewayLogTailLines = 400
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
			if logTail, err := stack.ComposeLogsTail(gatewayLogTailLines, "gateway"); err == nil {
				t.Logf("citest: gateway logs:\n%s", logTail)
			}
			harness.DumpComposeLogs(t, stack, "gateway", "mock-openai", "versiond-0", "versiond-1", "versiond-2", "versiond-3")
		}
	})

	escrowID := harness.GetGatewayEscrowID(t, client, eps.GatewayHTTP)

	feePerNonce := harness.GetGatewayFeePerNonce(t, client, eps.GatewayHTTP, adminKey, escrowID)
	require.NotZero(t, feePerNonce, "session FeePerNonce must be set")
	before := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey, escrowID)
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
	harness.Step(t, "non-stream chat with mock-openai stream_error_envelope is answered as a failed host")
	status, body := harness.PostGatewayChatFailure(t, client, eps.GatewayHTTP, adminKey, req)
	require.Equal(t, http.StatusBadGateway, status, body)
	require.Contains(t, body, hostFailedAnswer)
	require.NotContains(t, body, "EngineCore encountered an issue", "the host's own error text must not reach the client")

	harness.Step(t, "wait for ERROR miss to land: StatusTimedOut, Missed++, host Cost unwound")
	var (
		timedOut harness.GatewayDebugInference
		after    harness.GatewayLedgerSnapshot
	)
	ok := harness.AssertEventually(t, 2*time.Minute, 500*time.Millisecond, func() bool {
		after = harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey, escrowID)
		if totalHostMissed(after.HostStats) < beforeMissed+1 {
			return false
		}
		landed := false
		for _, inference := range harness.GetGatewayDebugInferences(t, client, eps.GatewayHTTP, adminKey, escrowID) {
			switch inference.Status {
			case harness.InferenceStatusPending, harness.InferenceStatusStarted:
				return false
			case harness.InferenceStatusTimedOut:
				timedOut, landed = inference, true
			}
		}
		return landed
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
