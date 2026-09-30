//go:build testenvci

package citest

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"

	"github.com/stretchr/testify/require"
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

	// Acquire inserts a pending row and Release deletes it before Validate
	// returns, so the row often never overlaps a poll. payload_fetch_err is
	// the attempt: it is logged only after Acquire succeeded.
	waitPayloadFetchErrCounts(t, stack)
	released := harness.WaitLeasePendingZero(t, stack, cfg, 45*time.Second)
	require.Equal(t, 0, released.Submitted,
		"D7 off must not publish a vote (submitted=%d skipped=%d)", released.Submitted, released.Skipped)
	require.Equal(t, 0, released.DuplicateGroups)
	require.Equal(t, 0, released.Total, "fetch error must delete the lease instead of parking it for 30m")
	harness.Step(t, "payload fetch failed and the lease row is gone (total=%d)", released.Total)

	harness.Step(t, "more traffic after cooldown; the same inference must be fetched again")
	time.Sleep(35 * time.Second)
	// Snapshot after the cooldown, so the first wave's errors are the baseline
	// and only a later fetch of one of those inferences counts as a re-acquire.
	before := payloadFetchErrCounts(t, stack)
	require.NotEmpty(t, before)
	drivePayloadWithholdingChats(t, client, eps.GatewayHTTP, model, 6)

	var again string
	ok := harness.AssertEventually(t, 90*time.Second, time.Second, func() bool {
		snap := stack.PostgresLeaseSnapshot(t, cfg)
		require.Equal(t, 0, snap.Submitted, "D7 off must not publish a vote")
		require.Equal(t, 0, snap.DuplicateGroups)
		again = repeatedPayloadFetchErr(before, payloadFetchErrCounts(t, stack))
		return again != ""
	})
	require.True(t, ok, "inference that already failed payload fetch was not attempted again; before=%v", before)
	harness.Step(t, "inference %s fetched again after cooldown", again)

	settled := harness.WaitLeasePendingZero(t, stack, cfg, 15*time.Second)
	require.Equal(t, 0, settled.Submitted, "D7 off must not publish a vote")
	require.Equal(t, 0, settled.Total, "released retry must not leave a parked lease (pending=%d skipped=%d)", settled.Pending, settled.Skipped)
}

var payloadFetchErrInference = regexp.MustCompile(`inference_id=(\d+)`)

func payloadWithholdingHosts() []string {
	return []string{"versiond-0", "versiond-1", "versiond-2", "versiond-3"}
}

func payloadFetchErrCounts(t *testing.T, stack *harness.Stack) map[string]int {
	t.Helper()
	logs, err := stack.ComposeLogsAll(payloadWithholdingHosts()...)
	require.NoError(t, err)
	counts := map[string]int{}
	for _, line := range strings.Split(logs, "\n") {
		if !strings.Contains(line, "payload_fetch_err") {
			continue
		}
		m := payloadFetchErrInference.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		counts[m[1]]++
	}
	return counts
}

func waitPayloadFetchErrCounts(t *testing.T, stack *harness.Stack) map[string]int {
	t.Helper()
	var counts map[string]int
	ok := harness.AssertEventually(t, 45*time.Second, time.Second, func() bool {
		counts = payloadFetchErrCounts(t, stack)
		return len(counts) > 0
	})
	require.True(t, ok, "no payload_fetch_err in versiond logs")
	return counts
}

func repeatedPayloadFetchErr(before, after map[string]int) string {
	for id, n := range before {
		if after[id] > n {
			return id
		}
	}
	return ""
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
