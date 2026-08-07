//go:build testenvci

package citest

import (
	"fmt"
	"testing"
	"time"

	"devshard/testenv/citest/harness"
	"devshard/testenv/config"
)

// TestDispositionTraceGhost asserts a ghost-burn disposition reaches both the
// Prometheus counter and the trace for the request that caused it.
//
// A 2-host multi stack is HA-only: both versiond hosts share one on-chain
// participant behind the router, so StopService("versiond-1") never produces
// ghosts. Use HA+solo (3 hosts) and stop the solo executor (versiond-2): its
// InferenceURL is direct, so transport failures quarantine that participant
// and later slots burn ghostThrottled.
func TestDispositionTraceGhost(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootObservabilityStackHASolo(t, "citest-disp-ghost-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-2", "tempo", "alloy", "loki", "prometheus")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	harness.Step(t, "warm chat before solo host stop")
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest disposition ghost warm"},
		},
		MaxTokens: 16,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	harness.Step(t, "stop versiond-2 solo executor and drive traffic")
	stack.StopService(t, "versiond-2")

	// EscrowSlots=4 with HA+solo identities → enough rounds for the dead
	// solo participant to take transport quarantine and burn ghosts.
	for i := 0; i < len(cfg.Hosts)*4; i++ {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest disposition ghost traffic %d", i+1)},
			},
			MaxTokens: 16,
		}
		_, _ = harness.PostGatewayChatSoft(t, client, eps.GatewayHTTP, adminKey, req)
	}

	harness.WaitLokiLogQL(t, obs,
		`{compose_service="devshardctl"} | json | stage="nonce_disposition" | disposition="ghost"`,
		3*time.Minute,
	)
	ids := harness.WaitTraceByAttr(t, obs,
		`{ span.devshard.disposition = "ghost" }`,
		3*time.Minute,
	)
	t.Logf("citest: ghost disposition traces=%v", ids)
	harness.RequireSpanAttrs(t, obs, ids[0], map[string]string{
		"devshard.disposition": "ghost",
	})
	harness.RequireLogsForTrace(t, obs, ids[0], []string{"devshardctl"}, 2*time.Minute)
}

// TestDispositionTraceUnfinishedRefused would assert the late-path dispositions
// (unfinished_* and refused) on the trace. It stays skipped because host-side
// refusal/execution timeout knobs are not plumbed through versiond→devshardd in
// testenv compose: PatchAdversarialFastTimeouts only updates mock-dapi params
// while the gateway still waits ~32m of host ExecutionTimeout before emitting
// unfinished_*. A receipt-delay knob is required as well for the end-to-end
// LiveSendTimeout / NoReceiptTimeout patterns.
func TestDispositionTraceUnfinishedRefused(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	t.Skip("blocked on missing testenv knobs: host protocol timeout / receipt delay (see docs/observability-test-plan.md)")
}

// TestDispositionLabelValuesMatchSpanAttrs is the stack-level check that every
// positive Prometheus disposition label value is also present as a span attribute.
func TestDispositionLabelValuesMatchSpanAttrs(t *testing.T) {
	harness.SkipUnlessEnv(t, "TESTENV_CITEST")
	harness.RequireDocker(t)

	stack, cfg, eps, obs := harness.BootObservabilityStackHASolo(t, "citest-disp-label-attrs-*")
	client := harness.GatewayChatClient()
	t.Cleanup(func() {
		if t.Failed() {
			harness.DumpComposeLogs(t, stack, "devshardctl", "versiond-0", "versiond-1", "versiond-2", "prometheus", "tempo", "alloy")
		}
	})

	harness.WaitObservabilityReady(t, obs, 3*time.Minute)
	harness.WaitStackHealthy(t, stack, eps)
	harness.WaitGatewayChatReady(t, client, eps.GatewayHTTP, 3*time.Minute, stack)

	model := config.PrimaryModelID(cfg)
	adminKey := harness.TestenvAdminAPIKey

	harness.Step(t, "produce finished_used via happy-path chat")
	resp := harness.PostGatewayChatCompletion(t, client, eps.GatewayHTTP, adminKey, harness.ChatCompletionRequest{
		Model: model,
		Messages: []harness.ChatMessage{
			{Role: "user", Content: "citest disposition c4 finished"},
		},
		MaxTokens: 16,
	})
	harness.RequireMockOpenAIContent(t, resp.Choices[0].Message.Content)

	harness.Step(t, "produce ghost via stopped solo host")
	stack.StopService(t, "versiond-2")
	for i := 0; i < len(cfg.Hosts)*4; i++ {
		req := harness.ChatCompletionRequest{
			Model: model,
			Messages: []harness.ChatMessage{
				{Role: "user", Content: fmt.Sprintf("citest disposition c4 ghost %d", i+1)},
			},
			MaxTokens: 16,
		}
		_, _ = harness.PostGatewayChatSoft(t, client, eps.GatewayHTTP, adminKey, req)
	}

	_ = harness.WaitTraceByAttr(t, obs, `{ span.devshard.disposition = "finished_used" }`, 2*time.Minute)
	harness.WaitLokiLogQL(t, obs,
		`{compose_service="devshardctl"} | json | stage="nonce_disposition" | disposition="ghost"`,
		3*time.Minute,
	)
	_ = harness.WaitTraceByAttr(t, obs, `{ span.devshard.disposition = "ghost" }`, 3*time.Minute)

	harness.Step(t, "scrape Prometheus disposition labels and match span attrs")
	var series []map[string]string
	ok := harness.AssertEventually(t, 2*time.Minute, 5*time.Second, func() bool {
		got, ready := harness.TryQueryPrometheusInstant(obs, `devshard_accounting_disposition > 0`)
		if !ready || len(got) == 0 {
			return false
		}
		series = got
		return true
	})
	if !ok {
		harness.RequireMetricsBody(t, client, eps.GatewayHTTP+"/metrics", "devshard_accounting_disposition")
		t.Fatalf("prometheus returned no positive devshard_accounting_disposition series")
	}

	seen := map[string]bool{}
	for _, metric := range series {
		for _, dim := range []struct {
			label string
			attr  string
		}{
			{"disposition", "devshard.disposition"},
			{"no_send_reason", "devshard.no_send_reason"},
			{"quarantine_mode", "devshard.quarantine_mode"},
			{"failure_origin", "devshard.failure_origin"},
			{"dispatch_phase", "devshard.dispatch_phase"},
		} {
			val := metric[dim.label]
			if val == "" {
				continue
			}
			key := dim.attr + "=" + val
			if seen[key] {
				continue
			}
			seen[key] = true
			query := fmt.Sprintf(`{ span.%s = %q }`, dim.attr, val)
			ids := harness.WaitTraceByAttr(t, obs, query, 2*time.Minute)
			harness.RequireSpanAttrs(t, obs, ids[0], map[string]string{dim.attr: val})
		}
	}
	requireDispositionSeen(t, seen, "devshard.disposition=finished_used")
	requireDispositionSeen(t, seen, "devshard.disposition=ghost")
}

func requireDispositionSeen(t *testing.T, seen map[string]bool, key string) {
	t.Helper()
	if !seen[key] {
		t.Fatalf("expected Prometheus series to include %s; saw %v", key, seen)
	}
}
