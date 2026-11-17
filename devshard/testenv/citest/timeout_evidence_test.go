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

// TestTimeoutEvidence drives refusal and execution timeouts through the full
// stack and checks what verifiers make of the executor's answer when they
// challenge it. Every host carries the fault, so each attempt of a request
// fails the same way and the gateway votes a timeout for all of them.
//
//   - refusal, honest executor: the payload never arrives, so no receipt is
//     signed; challenged, the executor signs one. Verifiers check it, decline,
//     and hand it back as recovery, so the inference starts and nobody misses.
//   - refusal, forging executor: the challenge answer carries an unsigned
//     receipt. Verifiers vote the timeout and the executor takes a miss.
//   - execution, honest executor: the model 503s after the receipt, so there
//     is no finish. Verifiers vote the timeout.
//   - execution, forging executor: same, and the challenge answer carries a
//     stub finish for the inference. Verifiers still vote the timeout.
func TestTimeoutEvidence(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootTimeoutEvidenceStack(t, "citest-timeout-evidence-*")
	client := harness.GatewayChatClient()
	adminKey := harness.TestenvAdminAPIKey
	t.Cleanup(func() {
		harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
		harness.SetExecutorFault(t, stack, cfg, harness.ExecutorFaultDropPayload, false)
		harness.SetExecutorFault(t, stack, cfg, harness.ExecutorFaultForgeChallenge, false)
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "mock-openai", "versiond-0", "versiond-1", "versiond-2", "versiond-3")
		}
	})
	harness.PatchGatewayRedundancySpeedPolicy(t, client, eps.GatewayHTTP, "legacy")

	mlUnavailable := http.StatusServiceUnavailable
	for _, sc := range []struct {
		name     string
		faults   []string
		mlStatus *int
		timedOut bool
	}{
		{name: "refusal_honest_receipt_recovers", faults: []string{harness.ExecutorFaultDropPayload}},
		{name: "refusal_forged_receipt_times_out", faults: []string{harness.ExecutorFaultDropPayload, harness.ExecutorFaultForgeChallenge}, timedOut: true},
		{name: "execution_without_finish_times_out", mlStatus: &mlUnavailable, timedOut: true},
		{name: "execution_forged_finish_times_out", faults: []string{harness.ExecutorFaultForgeChallenge}, mlStatus: &mlUnavailable, timedOut: true},
	} {
		t.Run(sc.name, func(t *testing.T) {
			harness.Step(t, "%s: fleet serves a clean chat first", sc.name)
			harness.PostGatewayChatCompletionEventually(t, client, eps.GatewayHTTP, adminKey,
				timeoutEvidenceChat(cfg, "ready before "+sc.name), 3*time.Minute)

			before := harness.GetGatewayInferences(t, client, eps.GatewayHTTP, adminKey)
			beforeLedger := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)
			for _, f := range sc.faults {
				harness.SetExecutorFault(t, stack, cfg, f, true)
			}
			if sc.mlStatus != nil {
				harness.PatchMockOpenAIFault(t, client, eps.MockOpenAIHTTP, mockopenai.FaultPatch{HTTPStatus: sc.mlStatus})
			}

			harness.Step(t, "%s: chat fails on every host", sc.name)
			code := harness.PostGatewayChatExpectFailure(t, client, eps.GatewayHTTP, adminKey, timeoutEvidenceChat(cfg, sc.name))
			t.Logf("citest: %s: gateway status %d", sc.name, code)

			attempts := harness.NewGatewayInferences(before, harness.GetGatewayInferences(t, client, eps.GatewayHTTP, adminKey))
			require.NotEmpty(t, attempts, "the failed chat must have started inferences")

			harness.Step(t, "%s: wait for the timeout vote on %d attempt(s)", sc.name, len(attempts))
			settled := func(status string) bool { return status != "pending" }
			if sc.timedOut {
				settled = func(status string) bool { return status != "pending" && status != "started" }
			}
			wait := time.Duration(4*(harness.TimeoutEvidenceRefusalTimeout+harness.TimeoutEvidenceExecutionTimeout))*time.Second + 2*time.Minute
			final := harness.WaitGatewayInferencesSettle(t, client, eps.GatewayHTTP, adminKey, attempts, wait, settled)
			afterLedger := harness.GetGatewayLedgerSnapshot(t, client, eps.GatewayHTTP, adminKey)

			for _, f := range sc.faults {
				harness.SetExecutorFault(t, stack, cfg, f, false)
			}
			if sc.mlStatus != nil {
				harness.ResetMockOpenAIFault(t, client, eps.MockOpenAIHTTP)
			}

			wantMissed := map[string]uint32{}
			for id, rec := range final {
				t.Logf("citest: %s: inference %s slot %d → %s", sc.name, id, rec.ExecutorSlot, rec.Status)
				if sc.timedOut {
					require.Equal(t, "timed_out", rec.Status, "inference %s: verifiers must vote the timeout", id)
					wantMissed[fmt.Sprint(rec.ExecutorSlot)]++
				} else {
					require.NotEqual(t, "timed_out", rec.Status, "inference %s: a verified receipt must block the refusal", id)
					require.NotEqual(t, "pending", rec.Status, "inference %s: the verified receipt must land as recovery", id)
				}
			}
			for slot, after := range afterLedger.HostStats {
				require.Equal(t, beforeLedger.HostStats[slot].Missed+wantMissed[slot], after.Missed, "slot %s missed", slot)
			}
		})
	}
}

func timeoutEvidenceChat(cfg *config.File, tag string) harness.ChatCompletionRequest {
	return harness.ChatCompletionRequest{
		Model:     config.PrimaryModelID(cfg),
		Messages:  []harness.ChatMessage{{Role: "user", Content: fmt.Sprintf("citest timeout evidence %s %d", tag, time.Now().UnixNano())}},
		MaxTokens: 16,
	}
}
