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
// is accounted as TIMEOUT_REASON_ERROR: the client still sees today's
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
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai", "versiond-0", "versiond-1", "versiond-2")
		}
	})

	beforeBalance, beforeStats := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
	beforeMissed := totalHostMissed(beforeStats)

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

	harness.Step(t, "wait for ERROR miss to land: StatusTimedOut, Missed++, Cost and balance unchanged")
	var (
		timedOut     harness.GatewayDebugInference
		afterBalance uint64
		afterStats   map[string]harness.GatewayHostStats
	)
	ok := harness.AssertEventually(t, 2*time.Minute, 500*time.Millisecond, func() bool {
		afterBalance, afterStats = harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
		if totalHostMissed(afterStats) < beforeMissed+1 {
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

	require.Equal(t, beforeBalance, afterBalance, "client must be refunded the full ReservedCost")
	slotKey := fmt.Sprintf("%d", timedOut.ExecutorSlot)
	beforeHS := beforeStats[slotKey]
	afterHS := afterStats[slotKey]
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
