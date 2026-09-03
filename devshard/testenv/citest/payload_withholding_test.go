//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

func TestPayloadWithholding_AllCallers500_Invalidates(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootPayloadWithholdingStack(t, "citest-payload-withholding-all-*", harness.PayloadWithholdingBootOpts{
		PayloadHTTPStatus: "500",
	})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "versiond-3", "devshardctl", "mock-openai")
		}
	})

	bootPayloadWithholdingReady(t, stack, cfg, eps, client)

	harness.Step(t, "drive chat until a fetch-failure challenge settles Invalidated")
	infs := driveUntilInferenceStatus(t, client, eps.GatewayHTTP, config.PrimaryModelID(cfg), "invalidated", 2*time.Minute)
	challenged := harness.CountGatewayInferenceStatus(infs, "challenged")
	invalidated := harness.CountGatewayInferenceStatus(infs, "invalidated")
	require.Greater(t, challenged+invalidated, 0, "expected Challenged or Invalidated after payload 500")
	require.Greater(t, invalidated, 0, "Phase B must invalidate a withholding executor (challenged=%d invalidated=%d total=%d)",
		challenged, invalidated, len(infs))
}

func TestPayloadWithholding_SelectiveValidator_Challenges(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootPayloadWithholdingStack(t, "citest-payload-withholding-one-*", harness.PayloadWithholdingBootOpts{
		PayloadHTTPStatus: "500",
		FaultValidator:    "$solo",
	})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "versiond-3", "devshardctl")
		}
	})

	bootPayloadWithholdingReady(t, stack, cfg, eps, client)
	require.NotEmpty(t, cfg.Hosts[2].Address, "selective fault needs solo address")

	harness.Step(t, "drive chat until the faulted validator opens a challenge")
	infs := driveUntilInferenceStatus(t, client, eps.GatewayHTTP, config.PrimaryModelID(cfg), "challenged", 2*time.Minute, "invalidated")
	require.Greater(t, harness.CountGatewayInferenceStatus(infs, "challenged")+harness.CountGatewayInferenceStatus(infs, "invalidated"), 0,
		"one validator seeing payload 500 must still open a challenge")
}

func TestPayloadWithholding_D7Off_LeaseReleasedAndReacquired(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps := harness.BootPayloadWithholdingStack(t, "citest-payload-withholding-d7off-*", harness.PayloadWithholdingBootOpts{
		PayloadHTTPStatus: "500",
		VoteFalse:         "false",
	})
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "versiond-0", "versiond-1", "versiond-2", "versiond-3", "devshard-postgres")
		}
	})

	bootPayloadWithholdingReady(t, stack, cfg, eps, client)
	model := config.PrimaryModelID(cfg)

	harness.Step(t, "drive chat so HA validators acquire while D7 keeps fetch failure as an error")
	drivePayloadWithholdingChats(t, client, eps.GatewayHTTP, model, 6)

	first := harness.WaitLeasePending(t, stack, cfg, 1, 45*time.Second)
	require.Equal(t, 0, first.DuplicateGroups)
	harness.Step(t, "observed pending=%d; waiting for Release to delete the row", first.Pending)

	released := harness.WaitLeasePendingZero(t, stack, cfg, 45*time.Second)
	require.Equal(t, 0, released.Submitted,
		"D7 off must not publish a vote (submitted=%d skipped=%d)", released.Submitted, released.Skipped)
	harness.Step(t, "lease row gone well inside 30m TTL (total=%d)", released.Total)

	harness.Step(t, "more traffic after cooldown; a later attempt must be able to Acquire again")
	time.Sleep(35 * time.Second)
	drivePayloadWithholdingChats(t, client, eps.GatewayHTTP, model, 6)
	second := harness.WaitLeasePending(t, stack, cfg, 1, 90*time.Second)
	require.Equal(t, 0, second.DuplicateGroups)
	harness.Step(t, "re-acquired pending=%d inside TTL (not parked for 30m)", second.Pending)
}

func bootPayloadWithholdingReady(t *testing.T, stack *harness.Stack, cfg *config.File, eps harness.Endpoints, client *http.Client) {
	t.Helper()
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)
	harness.WaitGETOK(t, client, eps.RouterHTTP+"/"+cfg.Versiond.VersionName+"/healthz", 5*time.Minute, "devshardd health", stack)

	dapi := harness.MockDAPIFromEndpoints(eps)
	harness.SetValidationRate100(t, client, dapi.HTTP)

	model := config.PrimaryModelID(cfg)
	seed := harness.ChatCompletionRequest{
		Model:     model,
		Messages:  []harness.ChatMessage{{Role: "user", Content: "citest payload withholding seed"}},
		MaxTokens: 16,
	}
	harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey, seed)
	escrow := harness.GetGatewaySessionSnapshot(t, client, eps.GatewayHTTP, harness.TestenvAdminAPIKey).EscrowID
	require.NotEmpty(t, escrow)
	harness.WarmEscrowOnBothReplicas(t, stack, cfg, escrow)
}

func drivePayloadWithholdingChats(t *testing.T, client *http.Client, gatewayURL, model string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest payload withholding chat %d", i)},
			},
			MaxTokens: 16,
		}
		if _, err := harness.TryPostGatewayChatCompletion(client, gatewayURL, harness.TestenvAdminAPIKey, req); err != nil {
			t.Logf("citest: payload withholding chat %d: %v", i, err)
		}
	}
}

func driveUntilInferenceStatus(t *testing.T, client *http.Client, gatewayURL, model, status string, timeout time.Duration, extra ...string) map[string]harness.GatewayInference {
	t.Helper()
	want := append([]string{status}, extra...)
	deadline := time.Now().Add(timeout)
	var last map[string]harness.GatewayInference
	i := 0
	for time.Now().Before(deadline) {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest payload withholding until %s %d", status, i)},
			},
			MaxTokens: 16,
		}
		if _, err := harness.TryPostGatewayChatCompletion(client, gatewayURL, harness.TestenvAdminAPIKey, req); err != nil {
			t.Logf("citest: chat %d: %v", i, err)
		}
		last = harness.GetGatewayInferences(t, client, gatewayURL, harness.TestenvAdminAPIKey)
		for _, s := range want {
			if harness.CountGatewayInferenceStatus(last, s) >= 1 {
				return last
			}
		}
		i++
		time.Sleep(time.Second)
	}
	t.Fatalf("citest: no inference reached %v after %s (total=%d challenged=%d invalidated=%d finished=%d)",
		want, timeout, len(last),
		harness.CountGatewayInferenceStatus(last, "challenged"),
		harness.CountGatewayInferenceStatus(last, "invalidated"),
		harness.CountGatewayInferenceStatus(last, "finished"))
	return last
}
